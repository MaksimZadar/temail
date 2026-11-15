package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"github.com/MaksimZadar/temail/internal/config"
)

// unwrapChain returns err and everything reachable with Unwrap.
func unwrapChain(err error) []error {
	var out []error
	for err != nil {
		out = append(out, err)
		err = errors.Unwrap(err)
	}
	return out
}

// Fix 5: no returned error reaches the request or the response body.
func TestErrorsNeverCarryTheKeyOrRequest(t *testing.T) {
	const body = "echo " + testKey + " and the prompt secret-email-body"
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		http.Error(w, body, http.StatusUnauthorized)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	errs := []error{c.Ping(context.Background())}
	_, err := Complete(context.Background(), c, Request{User: "secret-email-body"}, labelSchemaFor())
	errs = append(errs, err)

	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error from a 401 server")
		}
		for _, e := range unwrapChain(err) {
			var oai *openai.Error
			if errors.As(e, &oai) {
				t.Fatal("an SDK error (with the request) is in the error chain")
			}
			for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				s := fmt.Sprintf(verb, e)
				if strings.Contains(s, testKey) || strings.Contains(s, "secret-email-body") || strings.Contains(s, "Authorization") {
					t.Fatalf("%s of %T leaked: %q", verb, e, s)
				}
			}
		}
	}
}

// Fix 5: the key still goes out as the bearer token (the fix removes it from
// errors, not from requests).
func TestKeyStillSentAsBearer(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) { okReply(w) })
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{}, Options{})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.last().auth != "Bearer "+testKey {
		t.Fatalf("auth = %q", f.last().auth)
	}
}

// Fix 6: a base URL with userinfo, a query or a fragment is refused.
func TestNewClientRefusesURLsWithUserinfoQueryFragment(t *testing.T) {
	for _, raw := range []string{
		"https://user:pw@openrouter.ai/api/v1",
		"https://openrouter.ai/api/v1?key=abc",
		"https://openrouter.ai/api/v1#x",
	} {
		if _, err := NewClient(Provider{Name: "x", BaseURL: raw, Kind: config.KindCloud}, "", Options{}); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}

// Fix 7: redirects are not followed; the key is not forwarded to another host.
func TestRedirectIsNotFollowed(t *testing.T) {
	target := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		okReply(w)
	})
	redirector := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		http.Redirect(w, r, target.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	c := newTestClient(t, redirector.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})

	err := c.Ping(context.Background())
	if CategoryOf(err) != CategoryOther {
		t.Fatalf("category = %s (%v), want other for an unfollowed redirect", CategoryOf(err), err)
	}
	if target.count() != 0 {
		t.Fatalf("the redirect target received %d requests (the key would have been sent there)", target.count())
	}
}

// Fix 7: oversized replies fail closed, for chat and for models.
func TestOversizeBodyFailsClosed(t *testing.T) {
	pad := strings.Repeat("a", completionsMaxBody+1024)
	big := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"`+pad+`"}`)
	})
	c := newTestClient(t, big.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})
	out, err := Complete(context.Background(), c, req(), labelSchemaFor())
	if err == nil || out != (labelOut{}) {
		t.Fatalf("oversize completion: out=%q err=%v", out.Label, err)
	}
	if CategoryOf(err) != CategoryOther {
		t.Fatalf("category = %s", CategoryOf(err))
	}

	hugeModels := newFake(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any, _ int) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		_, _ = w.Write([]byte(strings.Repeat(" ", modelsMaxBody+1024)))
	})
	c2 := newTestClient(t, hugeModels.srv.URL, config.LLMCapabilities{}, Options{})
	ids, err := c2.ListModels(context.Background())
	if err == nil || len(ids) != 0 {
		t.Fatalf("oversize model list: ids=%v err=%v", ids, err)
	}
}

// Fix 7: a normal-sized reply is not affected by the limit.
func TestNormalReplyUnderLimitSucceeds(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, `{"label":"fine"}`)
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})
	if out, err := Complete(context.Background(), c, req(), labelSchemaFor()); err != nil || out.Label != "fine" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

// Fix 7: a local provider dials loopback only. A non-loopback address is refused
// before any connection is attempted.
func TestLoopbackOnlyDialRefusesRemoteAddresses(t *testing.T) {
	dial := loopbackOnlyDial(nil2Dialer())
	for _, addr := range []string{"203.0.113.7:443", "10.0.0.1:80", "0.0.0.0:80", "[2001:db8::1]:80"} {
		start := time.Now()
		_, err := dial(context.Background(), "tcp", addr)
		if !errors.Is(err, errNotLoopback) {
			t.Fatalf("%s: err = %v, want errNotLoopback", addr, err)
		}
		if time.Since(start) > time.Second {
			t.Fatalf("%s: refusal took too long", addr)
		}
	}
}

// Fix 7: a loopback host still works through the local transport.
func TestLocalTransportReachesLoopback(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) { okReply(w) })
	c, err := NewClient(Provider{Name: "ollama", BaseURL: f.srv.URL, Model: "m", Kind: config.KindLocal, Enabled: true,
		Capabilities: config.LLMCapabilities{StructuredOutput: true}}, "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Fix 8: the correction message is counted against the context limit. The
// first request fits; the correction pushes it over, so nothing more is sent.
func TestCorrectionCountsAgainstContextLimit(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, "not json")
	})
	// 400 characters of user text is about 100 tokens: under a 110-token limit.
	// The correction message is longer than the budget left.
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true, MaxContext: 110}, Options{})
	long := Request{System: "Classify.", User: strings.Repeat("u", 300)}
	_, err := Complete(context.Background(), c, long, labelSchemaFor())
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge from the correction", err)
	}
	if f.count() != 1 {
		t.Fatalf("requests = %d, want 1 (the retry must not be sent)", f.count())
	}
}

// Fix 8: a decoder that echoes a huge string does not flood the correction.
func TestCorrectionTextIsTruncated(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any, _ int) {
		contentReply(w, "bad")
	})
	c := newTestClient(t, f.srv.URL, config.LLMCapabilities{StructuredOutput: true}, Options{})
	s := labelSchemaFor()
	s.Decode = func(raw []byte) (labelOut, error) {
		return labelOut{}, errors.New(strings.Repeat("x", 5000))
	}
	_, _ = Complete(context.Background(), c, req(), s)
	if f.count() != 2 {
		t.Fatalf("requests = %d", f.count())
	}
	msgs, _ := f.last().body["messages"].([]any)
	correction, _ := msgs[len(msgs)-1].(map[string]any)["content"].(string)
	if len(correction) > maxReasonChars+300 {
		t.Fatalf("correction is %d characters; the decoder text was not truncated", len(correction))
	}
}

func nil2Dialer() *net.Dialer { return &net.Dialer{Timeout: time.Second} }

// okReply writes a valid ping reply.
func okReply(w http.ResponseWriter) { contentReply(w, `{"ok":true}`) }
