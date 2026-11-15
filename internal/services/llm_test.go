package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/llm"
	"github.com/MaksimZadar/temail/internal/secrets"
)

const llmKey = "sk-LLM-CANARY-9f2e"

// fakeKeys is an in-memory LLMKeyStore.
type fakeKeys struct {
	mu     sync.Mutex
	keys   map[string]string
	setErr error
	delErr error
	getErr error
	sets   int
}

func newFakeKeys() *fakeKeys { return &fakeKeys{keys: map[string]string{}} }

func (f *fakeKeys) GetLLMKey(p string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.keys[p]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeys) SetLLMKey(p, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.sets++
	f.keys[p] = key
	return nil
}

func (f *fakeKeys) DeleteLLMKey(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	delete(f.keys, p)
	return nil
}

func (f *fakeKeys) setCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sets
}

// llmConfig has two providers: a local one (enabled) and a cloud one (disabled).
func llmConfig() config.Config {
	off := false
	c := config.Default()
	c.LLM = config.LLMConfig{
		DefaultProvider: "ollama",
		Providers: []config.LLMProvider{
			{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Model: "llama3"},
			{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m", Kind: config.KindCloud, Enabled: &off},
		},
	}
	return c
}

// fakeLLM serves the OpenAI-compatible routes that the tests use. Its handler
// receives the request count and writes the reply.
func fakeLLM(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handle))
	t.Cleanup(srv.Close)
	return srv
}

func okReply(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "c", "object": "chat.completion", "created": 1, "model": "m",
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": `{"ok":true}`}}},
	})
}

func newLLMSvc(t *testing.T, cfg config.Config, keys *fakeKeys) (*LLMService, *fakeConfig) {
	t.Helper()
	fc := newFakeConfig(cfg)
	return NewLLMService(fc, keys), fc
}

// ---- reads and DTO shape ----

func TestListProvidersNeverIncludesTheKey(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["openrouter"] = llmKey
	svc, _ := newLLMSvc(t, llmConfig(), keys)

	got, err := svc.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("providers = %d", len(got))
	}
	byName := map[string]LLMProviderDTO{}
	for _, p := range got {
		byName[p.Name] = p
	}
	if !byName["ollama"].IsDefault || byName["ollama"].Kind != config.KindLocal || !byName["ollama"].Enabled || byName["ollama"].HasKey {
		t.Fatalf("ollama = %+v", byName["ollama"])
	}
	if byName["openrouter"].Kind != config.KindCloud || byName["openrouter"].Enabled || !byName["openrouter"].HasKey {
		t.Fatalf("openrouter = %+v", byName["openrouter"])
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), llmKey) {
		t.Fatal("the API key appeared in the provider list")
	}
	for _, field := range []string{`"hasKey"`, `"baseURL"`, `"isDefault"`, `"capabilities"`, `"structuredOutput"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("camelCase field %s missing from %s", field, raw)
		}
	}
}

// ---- SaveProvider ----

func TestSaveProviderValidationWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		in   LLMProviderInput
	}{
		{"name with a space", LLMProviderInput{Name: "bad name", BaseURL: "http://127.0.0.1:1/v1"}},
		{"empty name", LLMProviderInput{Name: "  ", BaseURL: "http://127.0.0.1:1/v1"}},
		{"local label on a remote host", LLMProviderInput{Name: "x", BaseURL: "https://openrouter.ai/api/v1", Kind: config.KindLocal}},
		{"bad base URL", LLMProviderInput{Name: "x", BaseURL: "ftp://nope"}},
		{"unknown kind", LLMProviderInput{Name: "x", BaseURL: "https://a.example/v1", Kind: "remote"}},
		{"negative context", LLMProviderInput{Name: "x", BaseURL: "https://a.example/v1", Capabilities: LLMCapabilitiesDTO{MaxContext: -5}}},
		{"oversized key", LLMProviderInput{Name: "x", BaseURL: "https://a.example/v1", APIKey: strings.Repeat("k", maxAPIKeyLen+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := newFakeKeys()
			svc, fc := newLLMSvc(t, llmConfig(), keys)
			if err := svc.SaveProvider(tt.in); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
			if fc.saveCount() != 0 || keys.setCount() != 0 {
				t.Fatal("nothing may be written for invalid input")
			}
		})
	}
}

func TestSaveProviderCreatesWithKeyAndKeepsItOutOfConfig(t *testing.T) {
	keys := newFakeKeys()
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{
		Name: "lab", BaseURL: "http://127.0.0.1:8080/v1", Model: "m2", Enabled: true,
		Capabilities: LLMCapabilitiesDTO{StructuredOutput: true, MaxContext: 8000}, APIKey: llmKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if keys.keys["lab"] != llmKey {
		t.Fatal("key not stored under the provider name")
	}
	raw, _ := json.Marshal(fc.current())
	if strings.Contains(string(raw), llmKey) {
		t.Fatal("the API key was written to config.json")
	}
	p := fc.current().LLM.Providers[2]
	if p.Name != "lab" || !p.IsEnabled() || p.Capabilities.MaxContext != 8000 {
		t.Fatalf("provider not saved: %+v", p)
	}
}

func TestSaveProviderEmptyKeyKeepsStoredKey(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["openrouter"] = llmKey
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m3", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if keys.setCount() != 0 || keys.keys["openrouter"] != llmKey {
		t.Fatal("an empty apiKey must keep the stored key")
	}
	if fc.current().LLM.Providers[1].Model != "m3" {
		t.Fatal("other settings were not saved")
	}
}

func TestSaveProviderBaseURLChangeNeedsReentry(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["openrouter"] = llmKey
	svc, fc := newLLMSvc(t, llmConfig(), keys)

	err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://attacker.example/v1", Model: "m", Enabled: true})
	if !errors.Is(err, ErrKeyNeedsReentry) {
		t.Fatalf("err = %v, want ErrKeyNeedsReentry", err)
	}
	if fc.saveCount() != 0 || fc.current().LLM.Providers[1].BaseURL != "https://openrouter.ai/api/v1" {
		t.Fatal("the stored key must not follow a changed base URL")
	}

	err = svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://attacker.example/v1", Model: "m", Enabled: true, APIKey: "new-key"})
	if err != nil {
		t.Fatalf("re-entering the key should allow the change: %v", err)
	}
	if keys.keys["openrouter"] != "new-key" {
		t.Fatal("new key not stored")
	}
}

func TestSaveProviderKeyringFailureRestoresConfig(t *testing.T) {
	keys := newFakeKeys()
	keys.setErr = secrets.ErrKeyringUnavailable
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "lab", BaseURL: "http://127.0.0.1:8080/v1", APIKey: llmKey})
	if !errors.Is(err, secrets.ErrKeyringUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(fc.current().LLM.Providers) != 2 {
		t.Fatal("config not restored after the keyring failed")
	}
}

func TestSaveProviderConfigFailureLeavesKeyringAlone(t *testing.T) {
	keys := newFakeKeys()
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	fc.saveE = errors.New("disk full")
	if err := svc.SaveProvider(LLMProviderInput{Name: "lab", BaseURL: "http://127.0.0.1:8080/v1", APIKey: llmKey}); err == nil {
		t.Fatal("want an error")
	}
	if keys.setCount() != 0 {
		t.Fatal("keyring written although the config write failed")
	}
}

// ---- DeleteProvider and SetDefaultProvider ----

func TestDeleteProviderRemovesKeyAndClearsDefault(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = llmKey
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	if err := svc.DeleteProvider("ollama"); err != nil {
		t.Fatal(err)
	}
	cfg := fc.current()
	if len(cfg.LLM.Providers) != 1 || cfg.LLM.DefaultProvider != "" {
		t.Fatalf("config after delete: %+v", cfg.LLM)
	}
	if _, ok := keys.keys["ollama"]; ok {
		t.Fatal("key not deleted")
	}
	if err := svc.DeleteProvider("ollama"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteProviderKeyringFailureRestoresConfig(t *testing.T) {
	keys := newFakeKeys()
	keys.delErr = secrets.ErrKeyringUnavailable
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	if err := svc.DeleteProvider("ollama"); err == nil {
		t.Fatal("want an error")
	}
	if len(fc.current().LLM.Providers) != 2 || fc.current().LLM.DefaultProvider != "ollama" {
		t.Fatal("config not restored after the keyring delete failed")
	}
}

func TestSetDefaultProvider(t *testing.T) {
	svc, fc := newLLMSvc(t, llmConfig(), newFakeKeys())
	if err := svc.SetDefaultProvider("nobody"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("unknown default: %v", err)
	}
	if err := svc.SetDefaultProvider("openrouter"); err != nil {
		t.Fatal(err)
	}
	if fc.current().LLM.DefaultProvider != "openrouter" {
		t.Fatal("default not set")
	}
	if err := svc.SetDefaultProvider(""); err != nil || fc.current().LLM.DefaultProvider != "" {
		t.Fatalf("clearing the default failed: %v", err)
	}
}

// ---- TestProvider and ListModels against a fake endpoint ----

func TestTestProviderCategories(t *testing.T) {
	okSrv := fakeLLM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+llmKey {
			http.Error(w, "no key", http.StatusUnauthorized)
			return
		}
		okReply(w)
	})
	authSrv := fakeLLM(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad key echoed: "+llmKey, http.StatusUnauthorized)
	})
	tests := []struct {
		name     string
		cfg      func() config.Config
		keys     func(*fakeKeys)
		provider string
		want     string
		wantOK   bool
	}{
		{"ok", func() config.Config {
			c := llmConfig()
			c.LLM.Providers[0].BaseURL = okSrv.URL + "/v1"
			c.LLM.Providers[0].Capabilities.StructuredOutput = true
			return c
		}, func(k *fakeKeys) { k.keys["ollama"] = llmKey }, "ollama", string(llm.CategoryOK), true},
		{"unauthorized", func() config.Config {
			c := llmConfig()
			c.LLM.Providers[0].BaseURL = authSrv.URL + "/v1"
			c.LLM.Providers[0].Capabilities.StructuredOutput = true
			return c
		}, func(k *fakeKeys) { k.keys["ollama"] = llmKey }, "ollama", string(llm.CategoryUnauthorized), false},
		{"cloud disabled sends nothing", llmConfig, func(*fakeKeys) {}, "openrouter", CategoryCloudDisabled, false},
		{"no model", func() config.Config {
			c := llmConfig()
			c.LLM.Providers[0].Model = ""
			return c
		}, func(*fakeKeys) {}, "ollama", CategoryNoModel, false},
		{"keyring unavailable", llmConfig, func(k *fakeKeys) { k.getErr = errors.New("dbus gone") }, "ollama", CategoryKeyringDown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := newFakeKeys()
			tt.keys(keys)
			svc, _ := newLLMSvc(t, tt.cfg(), keys)
			got, err := svc.TestProvider(context.Background(), tt.provider)
			if err != nil {
				t.Fatal(err)
			}
			if got.Category != tt.want || got.OK != tt.wantOK || got.Message == "" {
				t.Fatalf("got %+v, want %s", got, tt.want)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), llmKey) {
				t.Fatal("the key appeared in the test result")
			}
		})
	}
}

func TestTestProviderUnknownNameIsAnError(t *testing.T) {
	svc, _ := newLLMSvc(t, llmConfig(), newFakeKeys())
	if _, err := svc.TestProvider(context.Background(), "nobody"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestListModelsSortedAndUnsupported(t *testing.T) {
	listSrv := fakeLLM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"b","object":"model","created":1,"owned_by":"x"},{"id":"a","object":"model","created":1,"owned_by":"x"}]}`)
			return
		}
		http.NotFound(w, r)
	})
	c := llmConfig()
	c.LLM.Providers[0].BaseURL = listSrv.URL + "/v1"
	svc, _ := newLLMSvc(t, c, newFakeKeys())
	ids, err := svc.ListModels(context.Background(), "ollama")
	if err != nil || strings.Join(ids, ",") != "a,b" {
		t.Fatalf("ids = %v, err = %v", ids, err)
	}

	noList := fakeLLM(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	c2 := llmConfig()
	c2.LLM.Providers[0].BaseURL = noList.URL + "/v1"
	svc2, _ := newLLMSvc(t, c2, newFakeKeys())
	ids, err = svc2.ListModels(context.Background(), "ollama")
	if !errors.Is(err, llm.ErrModelListUnsupported) || ids == nil || len(ids) != 0 {
		t.Fatalf("unsupported: ids=%v err=%v", ids, err)
	}
}

func TestListModelsRefusesDisabledCloud(t *testing.T) {
	svc, _ := newLLMSvc(t, llmConfig(), newFakeKeys())
	if _, err := svc.ListModels(context.Background(), "openrouter"); !errors.Is(err, llm.ErrCloudDisabled) {
		t.Fatalf("err = %v", err)
	}
}

func TestEveryReturnedValueIsKeyFree(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = llmKey
	keys.keys["openrouter"] = llmKey
	svc, _ := newLLMSvc(t, llmConfig(), keys)
	list, _ := svc.ListProviders()
	test, _ := svc.TestProvider(context.Background(), "openrouter")
	dump := fmt.Sprintf("%+v %+v", list, test)
	if strings.Contains(dump, llmKey) {
		t.Fatal("key in a returned value")
	}
}
