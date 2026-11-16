package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
)

// ---- reasoning_effort on chat completions ----

func TestReasoningEffortSentWhenSet(t *testing.T) {
	for _, want := range []string{config.ThinkingNone, config.ThinkingLow, config.ThinkingHigh} {
		t.Run(want, func(t *testing.T) {
			f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
				contentReply(w, `{"label":"finance"}`)
			})
			c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{Thinking: want})
			if _, err := Complete(context.Background(), c, req(), labelSchemaFor()); err != nil {
				t.Fatal(err)
			}
			body := f.last().body
			if got := body["reasoning_effort"]; got != want {
				t.Fatalf("reasoning_effort = %v, want %q", got, want)
			}
		})
	}
}

func TestReasoningEffortAbsentByDefault(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"finance"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})
	if _, err := Complete(context.Background(), c, req(), labelSchemaFor()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last().body["reasoning_effort"]; ok {
		t.Fatal("reasoning_effort must be omitted when thinking is empty")
	}
}

func TestProviderThinkingAppliesToCalls(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"finance"}`)
	})
	p := Provider{Name: "test", BaseURL: f.srv.URL, Model: "m-test", Kind: config.KindLocal, Enabled: true,
		Capabilities: config.LLMCapabilities{StructuredOutput: true}, Thinking: config.ThinkingNone}
	c, err := NewClient(p, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Complete(context.Background(), c, req(), labelSchemaFor()); err != nil {
		t.Fatal(err)
	}
	if got := f.last().body["reasoning_effort"]; got != config.ThinkingNone {
		t.Fatalf("reasoning_effort = %v, want none", got)
	}
}

func TestNewClientRejectsUnknownThinking(t *testing.T) {
	_, err := NewClient(Provider{Name: "test", BaseURL: "http://127.0.0.1:1/v1", Model: "m", Kind: config.KindLocal},
		"", Options{Thinking: "max"})
	if err == nil {
		t.Fatal("unknown thinking must be refused")
	}
	_, err = NewClient(Provider{Name: "test", BaseURL: "http://127.0.0.1:1/v1", Model: "m", Kind: config.KindLocal,
		Thinking: "max"}, "", Options{})
	if err == nil {
		t.Fatal("unknown provider thinking must be refused")
	}
}

// ---- Ollama /api/show ----

func TestModelThinkingReadsShowEndpoint(t *testing.T) {
	var (
		gotPath, gotAuth, gotModel string
	)
	srv := newShowServer(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools","thinking"],"thinking":{"values":[false,true],"default":true}}`))
	})
	c := newShowClient(t, srv+"/v1", config.KindLocal, "qwen3:8b")

	got, err := c.ModelThinking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/show" {
		t.Errorf("path = %q, want /api/show", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q; /api/show must not send a key", gotAuth)
	}
	if gotModel != "qwen3:8b" {
		t.Errorf("model = %q", gotModel)
	}
	if !got.Supported {
		t.Fatal("thinking must be supported")
	}
	if len(got.Values) != 2 || got.Values[0] != config.ThinkingNone || got.Values[1] != "" {
		t.Errorf("values = %v, want [none, \"\"]", got.Values)
	}
	if got.Default != "" {
		t.Errorf("default = %q, want provider default", got.Default)
	}
}

func TestModelThinkingWithoutThinkingObject(t *testing.T) {
	srv := newShowServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
	})
	c := newShowClient(t, srv+"/v1", config.KindLocal, "llama3")

	got, err := c.ModelThinking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Supported || got.Values == nil || len(got.Values) != 0 {
		t.Fatalf("got %+v, want unsupported with an empty list", got)
	}
}

func TestModelThinkingLevelsKeepOnlyKnownValues(t *testing.T) {
	srv := newShowServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = w.Write([]byte(`{"thinking":{"values":["none","low","high","max"],"default":"medium"}}`))
	})
	c := newShowClient(t, srv+"/v1", config.KindLocal, "gpt-oss")

	got, err := c.ModelThinking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{config.ThinkingNone, config.ThinkingLow, config.ThinkingHigh}
	if len(got.Values) != len(want) {
		t.Fatalf("values = %v, want %v", got.Values, want)
	}
	for i := range want {
		if got.Values[i] != want[i] {
			t.Fatalf("values = %v, want %v", got.Values, want)
		}
	}
	if got.Default != config.ThinkingMedium {
		t.Errorf("default = %q, want medium", got.Default)
	}
}

func TestModelThinkingDefaultOff(t *testing.T) {
	srv := newShowServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		_, _ = w.Write([]byte(`{"thinking":{"values":[false,true],"default":false}}`))
	})
	c := newShowClient(t, srv+"/v1", config.KindLocal, "m")
	got, err := c.ModelThinking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != config.ThinkingNone {
		t.Errorf("default = %q, want none", got.Default)
	}
}

func TestModelThinkingNonLocalIsUnsupported(t *testing.T) {
	srv := newShowServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("/api/show must not be called for a non-local provider")
	})
	c := newShowClient(t, srv+"/v1", config.KindCloud, "m")
	if _, err := c.ModelThinking(context.Background()); !errors.Is(err, ErrModelCapabilitiesUnsupported) {
		t.Fatalf("err = %v, want ErrModelCapabilitiesUnsupported", err)
	}
}

func TestModelThinkingEndpointWithoutShowIsUnsupported(t *testing.T) {
	srv := newShowServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	c := newShowClient(t, srv+"/v1", config.KindLocal, "m")
	if _, err := c.ModelThinking(context.Background()); !errors.Is(err, ErrModelCapabilitiesUnsupported) {
		t.Fatalf("err = %v, want ErrModelCapabilitiesUnsupported", err)
	}
}

func TestShowEndpoint_trailingSlashAndV1(t *testing.T) {
	tests := map[string]string{
		"http://127.0.0.1:11434/v1":  "http://127.0.0.1:11434/api/show",
		"http://127.0.0.1:11434/v1/": "http://127.0.0.1:11434/api/show",
		"http://127.0.0.1:11434":     "http://127.0.0.1:11434/api/show",
	}
	for in, want := range tests {
		got, err := showEndpoint(in)
		if err != nil || got != want {
			t.Errorf("showEndpoint(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// ---- helpers ----

// newShowServer is an Ollama-like server for /api/show.
func newShowServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body map[string]any)) string {
	t.Helper()
	srv := newFake(t, func(w http.ResponseWriter, r *http.Request, body map[string]any, _ int) {
		handle(w, r, body)
	})
	return srv.srv.URL
}

func newShowClient(t *testing.T, base, kind, model string) *Client {
	t.Helper()
	c, err := NewClient(Provider{Name: "test", BaseURL: base, Model: model, Kind: kind}, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
