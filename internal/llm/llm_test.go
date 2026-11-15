package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
)

const testKey = "sk-SECRET-KEY-42"

// fakeServer is an OpenAI-compatible server. handle writes the reply for the
// n-th request (1-based). Every request is recorded.
type fakeServer struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []recorded
	handle func(w http.ResponseWriter, r *http.Request, body map[string]any, n int)
}

type recorded struct {
	path string
	auth string
	body map[string]any
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any, n int)) *fakeServer {
	t.Helper()
	f := &fakeServer{handle: handle}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		n := len(f.reqs)
		f.mu.Unlock()
		if f.handle == nil {
			http.Error(w, "no handler", http.StatusInternalServerError)
			return
		}
		f.handle(w, r, body, n)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeServer) last() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeServer) nth(i int) recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[i]
}

// chatReply writes a chat-completions response with the given message object.
func chatReply(w http.ResponseWriter, message map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "cmpl-1", "object": "chat.completion", "created": 1, "model": "m",
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}},
	})
}

func contentReply(w http.ResponseWriter, content string) {
	chatReply(w, map[string]any{"role": "assistant", "content": content})
}

func toolReply(w http.ResponseWriter, name, args string) {
	chatReply(w, map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
		map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": name, "arguments": args}},
	}})
}

// newTestClient returns a client for a loopback test server. The kind is
// local, so the cloud gate does not apply unless a test sets it.
func newTestClient(t *testing.T, base string, caps config.LLMCapabilities, opts Options) *Client {
	t.Helper()
	c, err := NewClient(Provider{
		Name: "test", BaseURL: base, Model: "m-test", Kind: config.KindLocal, Enabled: true, Capabilities: caps,
	}, testKey, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type labelOut struct {
	Label string `json:"label"`
}

var labelSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"label": map[string]any{"type": "string"}},
	"required":             []string{"label"},
	"additionalProperties": false,
}

func labelSchemaFor() Schema[labelOut] {
	return Schema[labelOut]{
		Name:        "label_mail",
		Description: "one label",
		JSONSchema:  labelSchema,
		Decode:      DecodeStrict[labelOut],
	}
}

func req() Request {
	return Request{System: "Classify.", User: "Subject: invoice due Friday"}
}

// ---- structured modes ----

func TestNativeModeSendsJSONSchema(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"finance"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "finance" {
		t.Fatalf("label = %q", out.Label)
	}
	body := f.last().body
	rf, _ := body["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "label_mail" || js["strict"] != true {
		t.Fatalf("response_format = %v", rf)
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("native mode must not send tools")
	}
}

func TestToolsModeForcesFunctionCall(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		toolReply(w, "label_mail", `{"label":"travel"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{Tools: true}, Options{})

	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "travel" {
		t.Fatalf("label = %q", out.Label)
	}
	body := f.last().body
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "label_mail" {
		t.Fatalf("function name = %v", fn["name"])
	}
	choice, _ := body["tool_choice"].(map[string]any)
	if choice["type"] != "function" {
		t.Fatalf("tool_choice = %v", body["tool_choice"])
	}
	if _, ok := body["response_format"]; ok {
		t.Fatal("tools mode must not send response_format")
	}
}

func TestPromptModePutsSchemaInSystemAndParsesText(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"work"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{}, Options{})

	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "work" {
		t.Fatalf("label = %q", out.Label)
	}
	body := f.last().body
	if _, ok := body["response_format"]; ok {
		t.Fatal("prompt mode must not send response_format")
	}
	if _, ok := body["tools"]; ok {
		t.Fatal("prompt mode must not send tools")
	}
	msgs, _ := body["messages"].([]any)
	system, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, `"label"`) || !strings.Contains(system, "JSON") {
		t.Fatalf("system prompt does not carry the schema: %q", system)
	}
}

// ---- retry ----

func TestRetryOnceThenSucceeds(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, n int) {
		if n == 1 {
			contentReply(w, "Sure! Here is the label: finance")
			return
		}
		contentReply(w, `{"label":"finance"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if err != nil {
		t.Fatal(err)
	}
	if out.Label != "finance" || f.count() != 2 {
		t.Fatalf("label %q after %d requests", out.Label, f.count())
	}
	msgs, _ := f.last().body["messages"].([]any)
	lastUser, _ := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	if !strings.Contains(lastUser, "not valid") {
		t.Fatalf("retry did not carry the validation error: %q", lastUser)
	}
}

func TestRetryThenFailsClosed(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"finance","extra":1}`) // unknown field: always invalid
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if !errors.Is(err, ErrInvalidOutput) || CategoryOf(err) != CategoryInvalidOutput {
		t.Fatalf("err = %v (%s), want invalid output", err, CategoryOf(err))
	}
	if out != (labelOut{}) {
		t.Fatalf("partial result returned: %+v", out)
	}
	if f.count() != 2 {
		t.Fatalf("requests = %d, want exactly 2 (one retry)", f.count())
	}
}

func TestAPIErrorIsNotRetried(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	_, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if CategoryOf(err) != CategoryOther {
		t.Fatalf("category = %s", CategoryOf(err))
	}
	var e *Error
	if !errors.As(err, &e) || e.Status != 500 {
		t.Fatalf("status = %+v", e)
	}
	if f.count() != 1 {
		t.Fatalf("API error retried: %d requests", f.count())
	}
}

// ---- ListModels ----

func TestListModelsSortedAndDegradesOn404(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"zeta","object":"model","created":1,"owned_by":"x"},{"id":"alpha","object":"model","created":1,"owned_by":"x"}]}`)
			return
		}
		http.NotFound(w, r)
	})
	c := newTestClient(t, f.srv.URL+"/v1", config.LLMCapabilities{}, Options{})
	ids, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "alpha,zeta" {
		t.Fatalf("ids = %v", ids)
	}
	if f.last().auth != "Bearer "+testKey {
		t.Fatal("key not sent as bearer token")
	}

	missing := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		http.NotFound(w, r)
	})
	c2 := newTestClient(t, missing.srv.URL, config.LLMCapabilities{}, Options{})
	ids, err = c2.ListModels(context.Background())
	if !errors.Is(err, ErrModelListUnsupported) {
		t.Fatalf("err = %v, want ErrModelListUnsupported", err)
	}
	if ids == nil || len(ids) != 0 {
		t.Fatalf("degraded list must be empty and non-nil, got %v", ids)
	}
}

// ---- Ping error categories ----

func TestPingCategories(t *testing.T) {
	native := config.LLMCapabilities{StructuredOutput: true}
	tests := []struct {
		name    string
		caps    config.LLMCapabilities
		handle  func(w http.ResponseWriter, r *http.Request)
		want    Category
		wantOK  bool
		timeout time.Duration
	}{
		{"ok", native, func(w http.ResponseWriter, _ *http.Request) { contentReply(w, `{"ok":true}`) }, CategoryOK, true, 0},
		{"unauthorized", native, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad key "+testKey, http.StatusUnauthorized) // the body must not leak
		}, CategoryUnauthorized, false, 0},
		{"model not found", native, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "no such model", http.StatusNotFound)
		}, CategoryModelNotFound, false, 0},
		{"structured output rejected by native mode", native, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "response_format not supported", http.StatusBadRequest)
		}, CategoryStructuredUnsupported, false, 0},
		{"reply ignores the format", native, func(w http.ResponseWriter, _ *http.Request) {
			contentReply(w, "I cannot do JSON")
		}, CategoryStructuredUnsupported, false, 0},
		{"server error is other", native, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusBadGateway)
		}, CategoryOther, false, 0},
		{"slow server times out", native, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(3 * time.Second):
			}
		}, CategoryTimeout, false, 100 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) { tt.handle(w, r) })
			c := newTestClient(t, f.srv.URL, tt.caps, Options{Timeout: tt.timeout})
			err := c.Ping(context.Background())
			if CategoryOf(err) != tt.want {
				t.Fatalf("category = %s (%v), want %s", CategoryOf(err), err, tt.want)
			}
			if (err == nil) != tt.wantOK {
				t.Fatalf("err = %v, wantOK %v", err, tt.wantOK)
			}
			if err != nil && strings.Contains(err.Error()+fmt.Sprintf("%+v", err), testKey) {
				t.Fatal("key leaked into an error")
			}
		})
	}
}

func TestPingUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	_ = l.Close() // nothing listens now
	c := newTestClient(t, base, config.LLMCapabilities{}, Options{Timeout: time.Second})
	if got := CategoryOf(c.Ping(context.Background())); got != CategoryUnreachable {
		t.Fatalf("category = %s, want unreachable", got)
	}
}

// ---- cloud gate, cancel, key handling ----

func TestCloudDisabledRefusesEverythingBeforeSending(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"x"}`)
	})
	c, err := NewClient(Provider{Name: "or", BaseURL: f.srv.URL, Model: "m", Kind: config.KindCloud, Enabled: false}, testKey, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), c, req(), labelSchemaFor()); !errors.Is(err, ErrCloudDisabled) {
		t.Fatalf("Complete err = %v", err)
	}
	if _, err := c.ListModels(context.Background()); !errors.Is(err, ErrCloudDisabled) {
		t.Fatalf("ListModels err = %v", err)
	}
	if err := c.Ping(context.Background()); !errors.Is(err, ErrCloudDisabled) {
		t.Fatalf("Ping err = %v", err)
	}
	if f.count() != 0 {
		t.Fatalf("%d requests were sent to a disabled cloud provider", f.count())
	}
}

func TestEnabledCloudProviderMayCall(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"ok"}`)
	})
	c, err := NewClient(Provider{Name: "or", BaseURL: f.srv.URL, Model: "m", Kind: config.KindCloud, Enabled: true,
		Capabilities: config.LLMCapabilities{StructuredOutput: true}}, testKey, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), c, req(), labelSchemaFor()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledContextStopsTheCall(t *testing.T) {
	release := make(chan struct{})
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Complete(ctx, c, req(), labelSchemaFor())
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("cancelled call returned %v after %v", err, time.Since(start))
	}
	if CategoryOf(err) != CategoryTimeout {
		t.Fatalf("deadline should be a timeout, got %s", CategoryOf(err))
	}
}

func TestCallerCancelIsNotATimeoutCategory(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		<-r.Context().Done()
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	_, err := Complete(ctx, c, req(), labelSchemaFor())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
}

func TestKeyIsSentButNeverInErrors(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		// A hostile server echoes the key and the request in its error body.
		http.Error(w, "invalid key "+testKey+" for prompt secret-email-body", http.StatusUnauthorized)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{}, Options{})
	err := c.Ping(context.Background())
	if f.last().auth != "Bearer "+testKey {
		t.Fatal("the key must be sent as the bearer token")
	}
	for _, s := range []string{err.Error(), fmt.Sprintf("%v %+v %#v", err, err, err)} {
		if strings.Contains(s, testKey) || strings.Contains(s, "secret-email-body") {
			t.Fatalf("error text leaked the key or the body: %q", s)
		}
	}
}

func TestEmptyKeyDoesNotLeakEnvironmentKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key-LEAK")
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"ok":true}`)
	})
	c, err := NewClient(Provider{Name: "ollama", BaseURL: f.srv.URL, Model: "m", Kind: config.KindLocal, Enabled: true,
		Capabilities: config.LLMCapabilities{StructuredOutput: true}}, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if auth := f.last().auth; auth != "" {
		t.Fatalf("an Authorization header was sent with no key configured: %q", auth)
	}
}

// ---- limits and inputs ----

func TestContextLimitRefusesBeforeSending(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"x"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true, MaxContext: 10}, Options{})
	big := Request{User: strings.Repeat("a", 1000)}
	if _, err := Complete(context.Background(), c, big, labelSchemaFor()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if f.count() != 0 {
		t.Fatal("an oversized request was sent")
	}
}

func TestBadInputsAreRejected(t *testing.T) {
	f := newFake(t, nil)
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	bad := labelSchemaFor()
	bad.Name = "has spaces"
	if _, err := Complete(context.Background(), c, req(), bad); !errors.Is(err, ErrBadSchema) {
		t.Fatalf("bad name: %v", err)
	}
	nodecode := labelSchemaFor()
	nodecode.Decode = nil
	if _, err := Complete(context.Background(), c, req(), nodecode); !errors.Is(err, ErrBadSchema) {
		t.Fatalf("no decoder: %v", err)
	}
	noModel, err := NewClient(Provider{Name: "x", BaseURL: f.srv.URL, Kind: config.KindLocal, Enabled: true}, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), noModel, req(), labelSchemaFor()); !errors.Is(err, ErrNoModel) {
		t.Fatalf("no model: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("a rejected input reached the server")
	}
}

func TestNewClientRejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "ftp://x", "://nope", "http://"} {
		if _, err := NewClient(Provider{Name: "x", BaseURL: base}, "", Options{}); err == nil {
			t.Fatalf("base %q accepted", base)
		}
	}
}

func TestDecodeStrict(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"valid", `{"label":"a"}`, false},
		{"unknown field", `{"label":"a","x":1}`, true},
		{"wrong type", `{"label":1}`, true},
		{"trailing data", `{"label":"a"} {"label":"b"}`, true},
		{"empty", ``, true},
		{"not json", `hello`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeStrict[labelOut]([]byte(tt.raw))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), tt.raw) && tt.raw != "" {
				t.Fatalf("decode error echoes the input: %v", err)
			}
		})
	}
}

func TestProviderFromUsesConfigRules(t *testing.T) {
	p := ProviderFrom(config.LLMProvider{Name: "o", BaseURL: "https://openrouter.ai/api/v1", Model: "m"})
	if p.Kind != config.KindCloud || p.Enabled {
		t.Fatalf("cloud default not applied: %+v", p)
	}
	l := ProviderFrom(config.LLMProvider{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Model: "m"})
	if l.Kind != config.KindLocal || !l.Enabled {
		t.Fatalf("local default not applied: %+v", l)
	}
}
