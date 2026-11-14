package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// Keyring access goes through a process-global mock, so these tests must not
// run in parallel.

func TestLLMEnvVar(t *testing.T) {
	tests := []struct {
		provider string
		want     string
	}{
		{"openrouter", "TEMAIL_LLM_OPENROUTER_KEY"},
		{"OpenRouter", "TEMAIL_LLM_OPENROUTER_KEY"},
		{"open-router", "TEMAIL_LLM_OPEN_ROUTER_KEY"},
		{"my.provider 2", "TEMAIL_LLM_MY_PROVIDER_2_KEY"},
		{"ollama", "TEMAIL_LLM_OLLAMA_KEY"},
		{"naïve", "TEMAIL_LLM_NA_VE_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			if got := LLMEnvVar(tt.provider); got != tt.want {
				t.Errorf("LLMEnvVar(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestKeyringUserKeys(t *testing.T) {
	if got := BridgeUser("acc1"); got != "bridge:acc1" {
		t.Errorf("BridgeUser = %q", got)
	}
	if got := LLMUser("openrouter"); got != "llm:openrouter" {
		t.Errorf("LLMUser = %q", got)
	}
}

func TestBridgePasswordRoundTrip(t *testing.T) {
	keyring.MockInit()
	s := NewKeyringStore()

	if _, err := s.GetBridgePassword("acc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v, want ErrNotFound", err)
	}
	if err := s.SetBridgePassword("acc1", "pw1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.GetBridgePassword("acc1")
	if err != nil || got != "pw1" {
		t.Fatalf("Get = %q, %v; want pw1", got, err)
	}
	if err := s.DeleteBridgePassword("acc1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetBridgePassword("acc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err = %v, want ErrNotFound", err)
	}
	// Deleting a missing entry is not an error.
	if err := s.DeleteBridgePassword("acc1"); err != nil {
		t.Errorf("Delete missing: %v", err)
	}
}

func TestLLMKeyRoundTrip(t *testing.T) {
	keyring.MockInit()
	s := NewKeyringStore()

	if err := s.SetLLMKey("openrouter", "sk-test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.GetLLMKey("openrouter")
	if err != nil || got != "sk-test" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := s.DeleteLLMKey("openrouter"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetLLMKey("openrouter"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: err = %v, want ErrNotFound", err)
	}
}

func TestEnvPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		keyring  bool   // store a value in the keyring first
		envSet   bool   // set the env var
		envValue string // value of the env var when set
		want     string
		wantErr  error
	}{
		{name: "env only", envSet: true, envValue: "from-env", want: "from-env"},
		{name: "keyring only", keyring: true, want: "from-keyring"},
		{name: "env beats keyring", keyring: true, envSet: true, envValue: "from-env", want: "from-env"},
		{name: "empty env falls back to keyring", keyring: true, envSet: true, envValue: "", want: "from-keyring"},
		{name: "neither set", wantErr: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyring.MockInit()
			s := NewKeyringStore()

			if tt.keyring {
				if err := s.SetBridgePassword("acc1", "from-keyring"); err != nil {
					t.Fatal(err)
				}
				if err := s.SetLLMKey("openrouter", "from-keyring"); err != nil {
					t.Fatal(err)
				}
			}
			if tt.envSet {
				t.Setenv(EnvBridgePassword, tt.envValue)
				t.Setenv(LLMEnvVar("openrouter"), tt.envValue)
			}

			check := func(kind string, get func() (string, error)) {
				t.Helper()
				got, err := get()
				if tt.wantErr != nil {
					if !errors.Is(err, tt.wantErr) || got != "" {
						t.Errorf("%s: got %q, %v; want empty and %v", kind, got, err, tt.wantErr)
					}
					return
				}
				if err != nil || got != tt.want {
					t.Errorf("%s: got %q, %v; want %q", kind, got, err, tt.want)
				}
			}
			check("bridge", func() (string, error) { return s.GetBridgePassword("acc1") })
			check("llm", func() (string, error) { return s.GetLLMKey("openrouter") })
		})
	}
}

func TestEnvOverrideIsNotPersisted(t *testing.T) {
	keyring.MockInit()
	s := NewKeyringStore()
	t.Setenv(EnvBridgePassword, "env-only")
	if _, err := s.GetBridgePassword("acc1"); err != nil {
		t.Fatal(err)
	}
	// The env value must not be written to the keyring.
	t.Setenv(EnvBridgePassword, "")
	if _, err := s.GetBridgePassword("acc1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("env value leaked into keyring: err = %v", err)
	}
}

func TestMissingLLMKeyIsErrNotFound(t *testing.T) {
	keyring.MockInit()
	s := NewKeyringStore()
	got, err := s.GetLLMKey("ollama")
	if got != "" {
		t.Errorf("key = %q, want empty", got)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestKeyringErrorIsNotErrNotFound(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus gone"))
	t.Cleanup(keyring.MockInit)
	s := NewKeyringStore()
	if _, err := s.GetBridgePassword("acc1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want a non-ErrNotFound keyring error", err)
	}
}

func TestProbe(t *testing.T) {
	tests := []struct {
		name    string
		mockErr error
		wantErr bool
	}{
		{name: "usable keyring", wantErr: false},
		{name: "keyring unavailable", mockErr: errors.New("org.freedesktop.secrets was not provided"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mockErr != nil {
				keyring.MockInitWithError(tt.mockErr)
			} else {
				keyring.MockInit()
			}
			t.Cleanup(keyring.MockInit)

			err := NewKeyringStore().Probe()
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("Probe: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrKeyringUnavailable) {
				t.Fatalf("Probe err = %v, want ErrKeyringUnavailable", err)
			}
			if !strings.Contains(err.Error(), "Secret Service") {
				t.Errorf("message not actionable: %v", err)
			}
		})
	}
}

func TestProbeDoesNotWrite(t *testing.T) {
	keyring.MockInit()
	if err := NewKeyringStore().Probe(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKeyringStore().GetBridgePassword("probe"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Probe wrote a secret: err = %v", err)
	}
}
