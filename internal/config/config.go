// Package config holds the non-secret settings of temail: Bridge accounts and
// LLM provider definitions. Secrets never live here; see internal/secrets.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// SchemaVersion is the only config schema version this build reads and writes.
const SchemaVersion = 1

// Bridge defaults. Proton Bridge listens on 127.0.0.1 with these ports unless
// the user changed them in Bridge's settings.
const (
	DefaultBridgeHost = "127.0.0.1"
	DefaultIMAPPort   = 1143
	DefaultSMTPPort   = 1025
)

// TLS modes accepted for Bridge connections.
const (
	TLSModeSTARTTLS = "starttls"
	TLSModeSSL      = "ssl"
)

// ErrInvalid is wrapped by every error returned from Validate and Save.
var ErrInvalid = errors.New("invalid config")

// Config is the on-disk configuration document. It must not contain secrets:
// the JSON encoder only sees the fields below, and Load rejects unknown fields
// so a stray "password" key is an error rather than silently kept.
type Config struct {
	Version int             `json:"version"`
	Bridge  []BridgeAccount `json:"bridge"`
	LLM     LLMConfig       `json:"llm"`
}

// BridgeAccount describes one Proton Bridge login. The account ID is the key
// used for the keyring entry ("bridge:<id>"). The password is not stored here.
type BridgeAccount struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	IMAPPort int    `json:"imapPort"`
	SMTPPort int    `json:"smtpPort"`
	TLSMode  string `json:"tlsMode"`
	Username string `json:"username"`
}

// LLMConfig lists the configured OpenAI-compatible providers.
type LLMConfig struct {
	DefaultProvider string        `json:"defaultProvider"`
	Providers       []LLMProvider `json:"providers"`
}

// LLMProvider describes one LLM endpoint. The API key (if any) is not stored
// here; it lives in the keyring under "llm:<name>" or in TEMAIL_LLM_<NAME>_KEY.
type LLMProvider struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"`
	Model   string `json:"model"`
}

// Default returns the configuration used when no config file exists.
func Default() Config {
	return Config{Version: SchemaVersion, Bridge: []BridgeAccount{}, LLM: LLMConfig{Providers: []LLMProvider{}}}
}

// DefaultBridgeAccount returns an account with the standard Bridge host, ports
// and TLS mode. Callers set ID and Username.
func DefaultBridgeAccount(id, username string) BridgeAccount {
	return BridgeAccount{
		ID:       id,
		Host:     DefaultBridgeHost,
		IMAPPort: DefaultIMAPPort,
		SMTPPort: DefaultSMTPPort,
		TLSMode:  TLSModeSTARTTLS,
		Username: username,
	}
}

// applyDefaults fills zero-valued Bridge fields so a hand-edited file may omit
// them. It runs on Load only; Save writes exactly what it is given.
func applyDefaults(c *Config) {
	for i := range c.Bridge {
		a := &c.Bridge[i]
		if a.Host == "" {
			a.Host = DefaultBridgeHost
		}
		if a.IMAPPort == 0 {
			a.IMAPPort = DefaultIMAPPort
		}
		if a.SMTPPort == 0 {
			a.SMTPPort = DefaultSMTPPort
		}
		if a.TLSMode == "" {
			a.TLSMode = TLSModeSTARTTLS
		}
	}
}

// Validate checks every field and returns all problems at once, joined. The
// returned error wraps ErrInvalid.
func Validate(c Config) error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...))
	}

	if c.Version != SchemaVersion {
		add("unsupported version %d (want %d)", c.Version, SchemaVersion)
	}

	ids := make(map[string]bool, len(c.Bridge))
	for i, a := range c.Bridge {
		where := fmt.Sprintf("bridge[%d]", i)
		if strings.TrimSpace(a.ID) == "" {
			add("%s: id is required", where)
		} else if ids[a.ID] {
			add("%s: duplicate id %q", where, a.ID)
		}
		ids[a.ID] = true
		if strings.TrimSpace(a.Host) == "" {
			add("%s: host is required", where)
		}
		if !validPort(a.IMAPPort) {
			add("%s: imapPort %d out of range 1-65535", where, a.IMAPPort)
		}
		if !validPort(a.SMTPPort) {
			add("%s: smtpPort %d out of range 1-65535", where, a.SMTPPort)
		}
		if a.TLSMode != TLSModeSTARTTLS && a.TLSMode != TLSModeSSL {
			add("%s: tlsMode %q must be %q or %q", where, a.TLSMode, TLSModeSTARTTLS, TLSModeSSL)
		}
	}

	names := make(map[string]bool, len(c.LLM.Providers))
	for i, p := range c.LLM.Providers {
		where := fmt.Sprintf("llm.providers[%d]", i)
		if strings.TrimSpace(p.Name) == "" {
			add("%s: name is required", where)
		} else if names[p.Name] {
			add("%s: duplicate name %q", where, p.Name)
		}
		names[p.Name] = true
		if err := validBaseURL(p.BaseURL); err != nil {
			add("%s: baseURL %q: %v", where, p.BaseURL, err)
		}
	}
	if c.LLM.DefaultProvider != "" && !names[c.LLM.DefaultProvider] {
		add("llm.defaultProvider %q does not match any provider", c.LLM.DefaultProvider)
	}

	return errors.Join(errs...)
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

func validBaseURL(raw string) error {
	if raw == "" {
		return errors.New("required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("missing host")
	}
	return nil
}
