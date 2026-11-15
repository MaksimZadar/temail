// Package config holds the non-secret settings of temail: Bridge accounts and
// LLM provider definitions. Secrets never live here; see internal/secrets.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/MaksimZadar/temail/internal/secrets"
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
	// CertFingerprint is the pinned SHA-256 fingerprint (64 hex characters) of
	// the Bridge leaf certificate. Empty means not yet pinned (TOFU): the first
	// connection reports the fingerprint so the user can confirm it. Not a
	// secret.
	CertFingerprint string `json:"certFingerprint,omitempty"`
}

// LLMConfig lists the configured OpenAI-compatible providers.
type LLMConfig struct {
	DefaultProvider string        `json:"defaultProvider"`
	Providers       []LLMProvider `json:"providers"`
}

// Provider kinds. A "cloud" provider receives content only when enabled. A
// "local" provider runs on this machine (loopback base URL) and is enabled by
// default.
const (
	KindCloud = "cloud"
	KindLocal = "local"
)

// LLMProvider describes one LLM endpoint. The API key (if any) is not stored
// here; it lives in the keyring under "llm:<name>" or in TEMAIL_LLM_<NAME>_KEY.
//
// The fields after Model are optional and were added without a schema bump, so
// older files load unchanged:
//   - Kind: "cloud" or "local". Empty means derived from BaseURL (loopback host
//     is local, anything else cloud). See EffectiveKind.
//   - Enabled: nil means the default for the kind (local on, cloud off). A
//     cloud provider must be enabled before any content is sent to it.
//   - Capabilities: what the endpoint supports. Unset flags mean "no".
type LLMProvider struct {
	Name         string          `json:"name"`
	BaseURL      string          `json:"baseURL"`
	Model        string          `json:"model"`
	Kind         string          `json:"kind,omitempty"`
	Enabled      *bool           `json:"enabled,omitempty"`
	Capabilities LLMCapabilities `json:"capabilities,omitempty"`
	// KeyBinding is the BaseURLBinding of the base URL that the stored API key
	// was approved for. It is not a secret. A key is used only while the base
	// URL still has this binding (see KeyBoundTo), so a key never follows a URL
	// change. Empty means no key was approved for this provider.
	KeyBinding string `json:"keyBinding,omitempty"`
}

// BaseURLBinding returns the key-binding fingerprint of a base URL: the SHA-256
// of its normalised form (lowercase scheme and host, no trailing slash on the
// path). The URL is validated first, so it has no userinfo, query or fragment.
// The result is not a secret.
func BaseURLBinding(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	norm := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// KeyBoundTo reports whether a stored key may be used with this provider: the
// provider has a binding and it matches the current base URL.
func (p LLMProvider) KeyBoundTo() bool {
	return p.KeyBinding != "" && p.KeyBinding == BaseURLBinding(p.BaseURL)
}

// LLMCapabilities are the structured-output features an endpoint supports.
// MaxContext is an approximate size limit in tokens; 0 means unknown.
type LLMCapabilities struct {
	StructuredOutput bool `json:"structuredOutput,omitempty"`
	Tools            bool `json:"tools,omitempty"`
	MaxContext       int  `json:"maxContext,omitempty"`
}

// EffectiveKind returns the provider's kind. An explicit Kind wins. Otherwise a
// loopback base URL host is local and any other host is cloud. An unparseable
// base URL is cloud, which is the safe default.
func (p LLMProvider) EffectiveKind() string {
	if p.Kind != "" {
		return p.Kind
	}
	if isLoopbackURL(p.BaseURL) {
		return KindLocal
	}
	return KindCloud
}

// IsEnabled reports whether the provider may receive content. Cloud providers
// are off unless Enabled is set to true. Local providers are on unless it is
// set to false.
func (p LLMProvider) IsEnabled() bool {
	if p.Enabled != nil {
		return *p.Enabled
	}
	return p.EffectiveKind() == KindLocal
}

// isLoopbackURL reports whether the URL's host is localhost or a loopback IP.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
		if a.CertFingerprint != "" && !ValidFingerprint(a.CertFingerprint) {
			add("%s: certFingerprint must be 64 hex characters (SHA-256)", where)
		}
	}

	names := make(map[string]bool, len(c.LLM.Providers))
	envNames := make(map[string]string, len(c.LLM.Providers))
	for i, p := range c.LLM.Providers {
		where := fmt.Sprintf("llm.providers[%d]", i)
		if strings.TrimSpace(p.Name) == "" {
			add("%s: name is required", where)
		} else if names[p.Name] {
			add("%s: duplicate name %q", where, p.Name)
		}
		names[p.Name] = true
		// Two names that map to the same environment variable (case and "-"/"_"
		// fold together) would share one override. Refuse that.
		if env := secrets.LLMEnvVar(p.Name); p.Name != "" {
			if other, ok := envNames[env]; ok && other != p.Name {
				add("%s: name %q collides with %q (both read %s)", where, p.Name, other, env)
			}
			envNames[env] = p.Name
		}
		if p.KeyBinding != "" && !ValidFingerprint(p.KeyBinding) {
			add("%s: keyBinding must be a 64-character SHA-256 binding", where)
		}
		if err := validBaseURL(p.BaseURL); err != nil {
			add("%s: baseURL %q: %v", where, p.BaseURL, err)
		}
		switch p.Kind {
		case "", KindCloud, KindLocal:
		default:
			add("%s: kind %q must be %q, %q or empty", where, p.Kind, KindCloud, KindLocal)
		}
		if p.Kind == KindLocal && !isLoopbackURL(p.BaseURL) {
			// A "local" label on a remote host would send content off the machine
			// with the local defaults, so refuse it.
			add("%s: kind %q needs a loopback baseURL", where, KindLocal)
		}
		if p.Capabilities.MaxContext < 0 {
			add("%s: capabilities.maxContext must not be negative", where)
		}
	}
	if c.LLM.DefaultProvider != "" && !names[c.LLM.DefaultProvider] {
		add("llm.defaultProvider %q does not match any provider", c.LLM.DefaultProvider)
	}

	return errors.Join(errs...)
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }

// ValidFingerprint reports whether s is a SHA-256 fingerprint written as 64
// hexadecimal characters (either case).
func ValidFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// ValidBaseURL reports whether raw is an acceptable provider base URL. See
// validBaseURL.
func ValidBaseURL(raw string) error { return validBaseURL(raw) }

// validBaseURL requires an http(s) URL with a host and no userinfo, query or
// fragment. Credentials in the URL, or a query carrying a token, would be sent
// to the provider and kept in its logs, so those are refused.
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
	if u.User != nil {
		return errors.New("must not contain user information")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return errors.New("must not contain a query")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return errors.New("must not contain a fragment")
	}
	return nil
}
