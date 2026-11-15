package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func validConfig() Config {
	c := Default()
	c.Bridge = []BridgeAccount{DefaultBridgeAccount("acc1", "alice@proton.me")}
	c.LLM = LLMConfig{
		DefaultProvider: "openrouter",
		Providers: []LLMProvider{
			{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "m"},
			{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Model: "llama"},
		},
	}
	return c
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; empty means valid
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "empty config is valid", mutate: func(c *Config) { *c = Default() }},
		{name: "wrong version", mutate: func(c *Config) { c.Version = 99 }, wantErr: "unsupported version"},
		{name: "duplicate bridge id", mutate: func(c *Config) {
			c.Bridge = append(c.Bridge, DefaultBridgeAccount("acc1", "bob"))
		}, wantErr: `duplicate id "acc1"`},
		{name: "empty bridge id", mutate: func(c *Config) { c.Bridge[0].ID = " " }, wantErr: "id is required"},
		{name: "empty bridge host", mutate: func(c *Config) { c.Bridge[0].Host = "" }, wantErr: "host is required"},
		{name: "imap port zero", mutate: func(c *Config) { c.Bridge[0].IMAPPort = 0 }, wantErr: "imapPort 0"},
		{name: "imap port too high", mutate: func(c *Config) { c.Bridge[0].IMAPPort = 65536 }, wantErr: "imapPort 65536"},
		{name: "smtp port negative", mutate: func(c *Config) { c.Bridge[0].SMTPPort = -1 }, wantErr: "smtpPort -1"},
		{name: "port boundary 65535 ok", mutate: func(c *Config) { c.Bridge[0].IMAPPort = 65535 }},
		{name: "tls ssl ok", mutate: func(c *Config) { c.Bridge[0].TLSMode = "ssl" }},
		{name: "tls invalid", mutate: func(c *Config) { c.Bridge[0].TLSMode = "plain" }, wantErr: `tlsMode "plain"`},
		{name: "tls empty invalid", mutate: func(c *Config) { c.Bridge[0].TLSMode = "" }, wantErr: "tlsMode"},
		{name: "fingerprint empty (not pinned) ok", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = "" }},
		{name: "fingerprint lowercase ok", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = strings.Repeat("ab", 32) }},
		{name: "fingerprint uppercase ok", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = strings.Repeat("AB", 32) }},
		{name: "fingerprint too short", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = strings.Repeat("ab", 31) }, wantErr: "certFingerprint must be 64 hex"},
		{name: "fingerprint too long", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = strings.Repeat("ab", 33) }, wantErr: "certFingerprint must be 64 hex"},
		{name: "fingerprint not hex", mutate: func(c *Config) { c.Bridge[0].CertFingerprint = strings.Repeat("zz", 32) }, wantErr: "certFingerprint must be 64 hex"},
		{name: "duplicate provider", mutate: func(c *Config) {
			c.LLM.Providers = append(c.LLM.Providers, LLMProvider{Name: "ollama", BaseURL: "http://x", Model: "m"})
		}, wantErr: `duplicate name "ollama"`},
		{name: "empty provider name", mutate: func(c *Config) { c.LLM.Providers[0].Name = "" }, wantErr: "name is required"},
		{name: "default provider missing", mutate: func(c *Config) { c.LLM.DefaultProvider = "nope" }, wantErr: `defaultProvider "nope"`},
		{name: "default provider empty ok", mutate: func(c *Config) { c.LLM.DefaultProvider = "" }},
		{name: "baseURL not a URL", mutate: func(c *Config) { c.LLM.Providers[0].BaseURL = "::bad" }, wantErr: "baseURL"},
		{name: "baseURL wrong scheme", mutate: func(c *Config) { c.LLM.Providers[0].BaseURL = "ftp://x/v1" }, wantErr: "scheme must be http or https"},
		{name: "baseURL no host", mutate: func(c *Config) { c.LLM.Providers[0].BaseURL = "https:///v1" }, wantErr: "missing host"},
		{name: "baseURL empty", mutate: func(c *Config) { c.LLM.Providers[0].BaseURL = "" }, wantErr: "required"},
		{name: "multiple problems reported together", mutate: func(c *Config) {
			c.Bridge[0].IMAPPort = 0
			c.LLM.DefaultProvider = "nope"
		}, wantErr: "imapPort 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)
			err := Validate(c)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error does not wrap ErrInvalid: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestMultipleProblemsJoined(t *testing.T) {
	c := validConfig()
	c.Bridge[0].IMAPPort = 0
	c.LLM.DefaultProvider = "nope"
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "imapPort") || !strings.Contains(err.Error(), "defaultProvider") {
		t.Fatalf("expected both problems in error, got %v", err)
	}
}

func TestSecretsNeverInConfigJSON(t *testing.T) {
	c := validConfig()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"bridge", "llm", "version"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("top-level keys = %v, want %v", keys, want)
	}

	// The struct has no secret fields. Check the full JSON key set as well.
	var all map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"password", "key", "apikey", "secret", "token"}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				for _, f := range forbidden {
					if strings.EqualFold(k, f) {
						t.Errorf("forbidden field %q in config JSON", k)
					}
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(all)
}

func TestCertFingerprintOmittedWhenEmpty(t *testing.T) {
	c := validConfig()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "certFingerprint") {
		t.Errorf("empty certFingerprint must be omitted from JSON: %s", data)
	}
	c.Bridge[0].CertFingerprint = strings.Repeat("0f", 32)
	data, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"certFingerprint":"`+strings.Repeat("0f", 32)+`"`) {
		t.Errorf("certFingerprint not encoded: %s", data)
	}
}

func TestValidFingerprint(t *testing.T) {
	if !ValidFingerprint(strings.Repeat("a1", 32)) {
		t.Error("valid fingerprint rejected")
	}
	for _, bad := range []string{"", "abc", strings.Repeat("a", 63), strings.Repeat("g", 64), strings.Repeat("a1", 32) + " "} {
		if ValidFingerprint(bad) {
			t.Errorf("ValidFingerprint(%q) = true", bad)
		}
	}
}

func TestDefaultBridgeAccount(t *testing.T) {
	a := DefaultBridgeAccount("id", "user")
	want := BridgeAccount{ID: "id", Host: "127.0.0.1", IMAPPort: 1143, SMTPPort: 1025, TLSMode: "starttls", Username: "user"}
	if a != want {
		t.Errorf("got %+v, want %+v", a, want)
	}
}

func TestEffectiveKindAndEnabledDefaults(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name        string
		p           LLMProvider
		wantKind    string
		wantEnabled bool
	}{
		{"loopback derives local, enabled", LLMProvider{BaseURL: "http://127.0.0.1:11434/v1"}, KindLocal, true},
		{"localhost name is local", LLMProvider{BaseURL: "http://localhost:1234/v1"}, KindLocal, true},
		{"remote host derives cloud, disabled", LLMProvider{BaseURL: "https://openrouter.ai/api/v1"}, KindCloud, false},
		{"explicit cloud on loopback stays cloud", LLMProvider{Kind: KindCloud, BaseURL: "http://127.0.0.1:1/v1"}, KindCloud, false},
		{"cloud enabled explicitly", LLMProvider{BaseURL: "https://openrouter.ai/api/v1", Enabled: &on}, KindCloud, true},
		{"local disabled explicitly", LLMProvider{BaseURL: "http://127.0.0.1:11434/v1", Enabled: &off}, KindLocal, false},
		{"unparseable URL is cloud", LLMProvider{BaseURL: "://bad"}, KindCloud, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.EffectiveKind(); got != tt.wantKind {
				t.Fatalf("kind = %q, want %q", got, tt.wantKind)
			}
			if got := tt.p.IsEnabled(); got != tt.wantEnabled {
				t.Fatalf("enabled = %v, want %v", got, tt.wantEnabled)
			}
		})
	}
}

func TestLLMProviderValidationNewFields(t *testing.T) {
	base := func(p LLMProvider) Config {
		c := Default()
		c.LLM.Providers = []LLMProvider{p}
		return c
	}
	tests := []struct {
		name    string
		p       LLMProvider
		wantErr bool
	}{
		{"local on loopback is fine", LLMProvider{Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Kind: KindLocal}, false},
		{"local label on a remote host is refused", LLMProvider{Name: "x", BaseURL: "https://openrouter.ai/api/v1", Kind: KindLocal}, true},
		{"unknown kind is refused", LLMProvider{Name: "x", BaseURL: "https://openrouter.ai/api/v1", Kind: "remote"}, true},
		{"negative maxContext is refused", LLMProvider{Name: "x", BaseURL: "https://openrouter.ai/api/v1", Capabilities: LLMCapabilities{MaxContext: -1}}, true},
		{"capabilities and enabled are accepted", LLMProvider{Name: "x", BaseURL: "https://openrouter.ai/api/v1", Enabled: new(bool),
			Capabilities: LLMCapabilities{StructuredOutput: true, Tools: true, MaxContext: 32000}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(base(tt.p))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOldConfigFileLoadsUnchanged(t *testing.T) {
	dir := t.TempDir()
	old := `{"version":1,"bridge":[],"llm":{"defaultProvider":"or","providers":[{"name":"or","baseURL":"https://openrouter.ai/api/v1","model":"m"}]}}`
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := NewManager(dir).Load()
	if err != nil {
		t.Fatalf("old file must still load: %v", err)
	}
	p := c.LLM.Providers[0]
	if p.EffectiveKind() != KindCloud || p.IsEnabled() {
		t.Fatalf("old cloud provider must default to disabled cloud, got kind=%s enabled=%v", p.EffectiveKind(), p.IsEnabled())
	}
}
