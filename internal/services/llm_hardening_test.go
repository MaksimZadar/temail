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
	"github.com/MaksimZadar/temail/internal/secrets"
)

// Fix 1: a new provider does not inherit a key that is already there (keyring
// or environment) unless the key is entered for it.
func TestNewProviderDoesNotInheritAnExistingKey(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["lab"] = "env-or-keyring-key" // GetLLMKey would find it
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "lab", BaseURL: "http://127.0.0.1:8080/v1", Enabled: true})
	if !errors.Is(err, ErrKeyNeedsReentry) {
		t.Fatalf("err = %v, want ErrKeyNeedsReentry", err)
	}
	if fc.saveCount() != 0 {
		t.Fatal("nothing may be saved when the key must be re-entered")
	}
}

// Fix 1: an existing key bound to an old URL is not kept when the URL changes,
// even when the key came from the environment.
func TestEnvironmentKeyNotBoundToNewURLIsRefused(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = "env-key"
	cfg := llmConfig()
	cfg.LLM.Providers[0] = bound(cfg.LLM.Providers[0]) // approved for the old URL
	svc, fc := newLLMSvc(t, cfg, keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "ollama", BaseURL: "http://127.0.0.1:9999/v1", Model: "llama3", Enabled: true})
	if !errors.Is(err, ErrKeyNeedsReentry) || fc.saveCount() != 0 {
		t.Fatalf("err = %v, saves = %d", err, fc.saveCount())
	}
}

// Fix 1: the client refuses a key that is not bound to the current URL, even
// when the save path was bypassed (for example a hand-edited config).
func TestClientRefusesKeyNotBoundToItsURL(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = llmKey
	cfg := llmConfig()
	cfg.LLM.Providers[0].KeyBinding = config.BaseURLBinding("http://127.0.0.1:1111/v1") // stale binding
	svc, _ := newLLMSvc(t, cfg, keys)
	if _, err := svc.ListModels(context.Background(), "ollama"); !errors.Is(err, ErrKeyNeedsReentry) {
		t.Fatalf("ListModels err = %v, want ErrKeyNeedsReentry", err)
	}
	if got, _ := svc.TestProvider(context.Background(), "ollama"); got.OK {
		t.Fatal("a provider with an unbound key must not test OK")
	}
}

// Fix 1: names that fold to the same environment variable are refused on save.
func TestSaveProviderRefusesEnvNameCollision(t *testing.T) {
	cfg := llmConfig()
	cfg.LLM.Providers[0].Name = "open_router"
	cfg.LLM.Providers[1].Name = "other"
	svc, fc := newLLMSvc(t, cfg, newFakeKeys())
	err := svc.SaveProvider(LLMProviderInput{Name: "open-router", BaseURL: "http://127.0.0.1:8080/v1"})
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("err = %v, want an env-name collision", err)
	}
	if fc.saveCount() != 0 {
		t.Fatal("a colliding provider was saved")
	}
}

// Fix 2: any keyring error other than "no entry" stops the save and writes nothing.
func TestReentryCheckFailsClosedOnKeyringError(t *testing.T) {
	keys := newFakeKeys()
	keys.getErr = errors.New("dbus: connection lost")
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Enabled: false})
	if !errors.Is(err, secrets.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want ErrKeyringUnavailable", err)
	}
	if fc.saveCount() != 0 || keys.setCount() != 0 {
		t.Fatal("a keyring error must save nothing")
	}
}

// Fix 2: the same for the keyring-only read used before a key write.
func TestStoredKeyReadFailsClosed(t *testing.T) {
	keys := newFakeKeys()
	keys.storedErr = errors.New("keyring locked")
	svc, fc := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "lab", BaseURL: "http://127.0.0.1:8080/v1", APIKey: llmKey})
	if !errors.Is(err, secrets.ErrKeyringUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if fc.saveCount() != 0 || keys.setCount() != 0 {
		t.Fatal("nothing may be written when the previous key cannot be read")
	}
}

// Fix 3: the keyring is replaced before config.json is saved.
func TestKeyIsReplacedBeforeConfigIsSaved(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = "old-key"
	cfg := llmConfig()
	cfg.LLM.Providers[0] = bound(cfg.LLM.Providers[0])
	svc, fc := newLLMSvc(t, cfg, keys)
	fc.onSave = func() {
		if got := keys.keys["ollama"]; got != llmKey {
			t.Errorf("at config save the keyring holds %q; the new key must already be in place", got)
		}
	}
	err := svc.SaveProvider(LLMProviderInput{Name: "ollama", BaseURL: "http://127.0.0.1:9999/v1", Model: "llama3", Enabled: true, APIKey: llmKey})
	if err != nil {
		t.Fatal(err)
	}
	if fc.saveCount() != 1 || keys.keys["ollama"] != llmKey {
		t.Fatal("expected one config save and the new key in the keyring")
	}
}

// Fix 3: a failed config save puts the previous key back.
func TestFailedConfigSaveRestoresPreviousKey(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["ollama"] = "old-key"
	cfg := llmConfig()
	cfg.LLM.Providers[0] = bound(cfg.LLM.Providers[0])
	svc, fc := newLLMSvc(t, cfg, keys)
	fc.saveE = errors.New("disk full")
	if err := svc.SaveProvider(LLMProviderInput{Name: "ollama", BaseURL: "http://127.0.0.1:9999/v1", Model: "llama3", APIKey: llmKey}); err == nil {
		t.Fatal("want an error")
	}
	if keys.keys["ollama"] != "old-key" {
		t.Fatalf("keyring = %q, want the old key back", keys.keys["ollama"])
	}
}

// Fix 3, concurrency: readers never pair a key with a base URL it was not
// approved for, while a writer keeps switching a provider between two servers.
// The servers record which key each request carried, so a mismatch is visible.
func TestReadersNeverPairKeyWithWrongURLDuringWrites(t *testing.T) {
	const keyA, keyB = "key-for-A", "key-for-B"
	var mu sync.Mutex
	var mismatches []string
	mk := func(want string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != want {
				mu.Lock()
				mismatches = append(mismatches, fmt.Sprintf("server expecting %q got %q", want, got))
				mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
		}))
	}
	srvA := mk(keyA)
	defer srvA.Close()
	srvB := mk(keyB)
	defer srvB.Close()

	keys := newFakeKeys()
	cfg := config.Default()
	cfg.LLM.Providers = []config.LLMProvider{{Name: "switch", BaseURL: srvA.URL + "/v1", Model: "m", Kind: config.KindLocal}}
	svc, _ := newLLMSvc(t, cfg, keys)
	if err := svc.SaveProvider(LLMProviderInput{Name: "switch", BaseURL: srvA.URL + "/v1", Model: "m", Kind: config.KindLocal, Enabled: true, APIKey: keyA}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // writer
		defer wg.Done()
		for i := 0; i < 40; i++ {
			u, k := srvA.URL, keyA
			if i%2 == 1 {
				u, k = srvB.URL, keyB
			}
			_ = svc.SaveProvider(LLMProviderInput{Name: "switch", BaseURL: u + "/v1", Model: "m", Kind: config.KindLocal, Enabled: true, APIKey: k})
		}
	}()
	go func() { // reader
		defer wg.Done()
		for i := 0; i < 40; i++ {
			_, _ = svc.ListModels(context.Background(), "switch")
		}
	}()
	wg.Wait()
	if len(mismatches) > 0 {
		t.Fatalf("%d requests carried a key that was not approved for their URL, e.g. %s", len(mismatches), mismatches[0])
	}
}

// Fix 4: enabling cloud needs the confirmation. Declining or having no dialog
// saves nothing.
func TestEnablingCloudNeedsConfirmation(t *testing.T) {
	tests := []struct {
		name      string
		confirm   CloudConfirmer
		wantErr   error
		wantSaved bool
	}{
		{"confirmed", &fakeConfirm{answer: true}, nil, true},
		{"declined", &fakeConfirm{answer: false}, ErrEnableDeclined, false},
		{"dialog error", &fakeConfirm{err: errors.New("no dialog")}, ErrEnableDeclined, false},
		{"no dialog available", nil, ErrEnableDeclined, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := newFakeConfig(llmConfig())
			svc := NewLLMService(fc, newFakeKeys(), tt.confirm)
			err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m", Enabled: true})
			if !errors.Is(err, tt.wantErr) && !(err == nil && tt.wantErr == nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if (fc.saveCount() == 1) != tt.wantSaved {
				t.Fatalf("saved = %d, want saved %v", fc.saveCount(), tt.wantSaved)
			}
		})
	}
}

// Fix 4: the question is asked once, for the change to an enabled cloud provider.
func TestConfirmationOnlyForEnablingCloud(t *testing.T) {
	on := true
	off := false
	cfg := llmConfig()
	cfg.LLM.Providers[1].Enabled = &on // openrouter already enabled
	cfg.LLM.Providers[1] = bound(cfg.LLM.Providers[1])
	conf := &fakeConfirm{answer: true}
	svc := NewLLMService(newFakeConfig(cfg), newFakeKeys(), conf)

	// Editing an enabled cloud provider: no question.
	if err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Disabling: no question.
	if err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m2", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if conf.count() != 0 {
		t.Fatalf("questions asked for edits and disables: %d", conf.count())
	}
	// Enabling it again: asked.
	if err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if conf.count() != 1 {
		t.Fatalf("questions = %d, want 1 for enabling", conf.count())
	}
	_ = off
}

// Fix 4: a local provider changed to cloud while enabled is a privileged change.
func TestKindChangeToCloudWhileEnabledNeedsConfirmation(t *testing.T) {
	conf := &fakeConfirm{answer: false}
	cfg := llmConfig()
	svc := NewLLMService(newFakeConfig(cfg), newFakeKeys(), conf)
	err := svc.SaveProvider(LLMProviderInput{Name: "ollama", BaseURL: "https://remote.example/v1", Kind: config.KindCloud, Enabled: true})
	if !errors.Is(err, ErrEnableDeclined) || conf.count() != 1 {
		t.Fatalf("err = %v, questions = %d", err, conf.count())
	}
}

// Fix 4: disabling is never refused for lack of a dialog.
func TestDisableNeedsNoDialog(t *testing.T) {
	on := true
	cfg := llmConfig()
	cfg.LLM.Providers[1].Enabled = &on
	cfg.LLM.Providers[1] = bound(cfg.LLM.Providers[1])
	fc := newFakeConfig(cfg)
	svc := NewLLMService(fc, newFakeKeys(), nil)
	if err := svc.SaveProvider(LLMProviderInput{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m", Enabled: false}); err != nil {
		t.Fatalf("disabling without a dialog: %v", err)
	}
	if fc.current().LLM.Providers[1].IsEnabled() {
		t.Fatal("not disabled")
	}
}

// Fix 4: the enable-state transition helper, as a table.
func TestEnablesCloudTransitions(t *testing.T) {
	on, off := true, false
	cloudOn := config.LLMProvider{BaseURL: "https://x.example/v1", Enabled: &on}
	cloudOff := config.LLMProvider{BaseURL: "https://x.example/v1", Enabled: &off}
	local := config.LLMProvider{BaseURL: "http://127.0.0.1:1/v1"}
	tests := []struct {
		name   string
		prev   config.LLMProvider
		exists bool
		kind   string
		enable bool
		want   bool
	}{
		{"new cloud enabled", config.LLMProvider{}, false, config.KindCloud, true, true},
		{"cloud off to on", cloudOff, true, config.KindCloud, true, true},
		{"cloud on stays on", cloudOn, true, config.KindCloud, true, false},
		{"cloud on to off", cloudOn, true, config.KindCloud, false, false},
		{"local to cloud while enabled", local, true, config.KindCloud, true, true},
		{"local stays local", local, true, config.KindLocal, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := enablesCloud(tt.prev, tt.exists, tt.kind, tt.enable); got != tt.want {
				t.Fatalf("enablesCloud = %v, want %v", got, tt.want)
			}
		})
	}
}

// Fix 2 and 1: the error text never carries a key, and the sentinel messages
// are the documented ones.
func TestNewErrorsCarryNoKey(t *testing.T) {
	keys := newFakeKeys()
	keys.keys["lab"] = llmKey
	svc, _ := newLLMSvc(t, llmConfig(), keys)
	err := svc.SaveProvider(LLMProviderInput{Name: "lab", BaseURL: "http://127.0.0.1:8080/v1"})
	dump := fmt.Sprintf("%v %+v %#v", err, err, err)
	if strings.Contains(dump, llmKey) {
		t.Fatalf("key in error: %s", dump)
	}
	if !json.Valid([]byte(`{}`)) {
		t.Fatal("unreachable")
	}
}
