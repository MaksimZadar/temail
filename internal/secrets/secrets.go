// Package secrets stores Bridge passwords and LLM API keys in the OS keyring
// (Windows Credential Manager, Linux Secret Service). There is no file
// fallback: if the keyring is unusable, Probe reports ErrKeyringUnavailable.
//
// Reads check environment variables first, then the keyring:
//   - TEMAIL_BRIDGE_PASSWORD: single-account override for the Bridge password.
//   - TEMAIL_LLM_<NAME>_KEY: per-provider override, NAME is the provider name
//     uppercased with every non-alphanumeric character replaced by "_".
//
// Environment values are never written back to the keyring.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

// ServiceName is the keyring service name for every temail entry.
const ServiceName = "temail"

// EnvBridgePassword overrides the Bridge password for any account. It is meant
// for single-account setups.
const EnvBridgePassword = "TEMAIL_BRIDGE_PASSWORD"

// ErrNotFound means no secret exists in the environment or the keyring. For LLM
// keys it is not a failure when the provider needs no key; callers check it
// with errors.Is.
var ErrNotFound = errors.New("secret not found")

// ErrKeyringUnavailable means the OS keyring cannot be used. Its message is
// meant for the user.
var ErrKeyringUnavailable = errors.New("the OS keychain is not available")

// Store is the secret storage interface. Get methods return ErrNotFound when
// the secret is absent from both the environment and the keyring.
type Store interface {
	GetBridgePassword(accountID string) (string, error)
	SetBridgePassword(accountID, password string) error
	DeleteBridgePassword(accountID string) error
	GetLLMKey(provider string) (string, error)
	SetLLMKey(provider, key string) error
	DeleteLLMKey(provider string) error
}

// KeyringStore implements Store on top of go-keyring.
type KeyringStore struct {
	service string
	// getenv is os.Getenv in production; tests may replace it.
	getenv func(string) string
}

// NewKeyringStore returns a Store that uses the "temail" keyring service.
func NewKeyringStore() *KeyringStore {
	return &KeyringStore{service: ServiceName, getenv: os.Getenv}
}

var _ Store = (*KeyringStore)(nil)

// BridgeUser returns the keyring user key for a Bridge account.
func BridgeUser(accountID string) string { return "bridge:" + accountID }

// LLMUser returns the keyring user key for an LLM provider.
func LLMUser(provider string) string { return "llm:" + provider }

// LLMEnvVar returns the environment variable that overrides the key for a
// provider, e.g. "open-router" becomes TEMAIL_LLM_OPEN_ROUTER_KEY.
func LLMEnvVar(provider string) string {
	var b strings.Builder
	b.WriteString("TEMAIL_LLM_")
	for _, r := range strings.ToUpper(provider) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	b.WriteString("_KEY")
	return b.String()
}

// GetBridgePassword returns the Bridge password for accountID. The
// TEMAIL_BRIDGE_PASSWORD environment variable takes precedence over the keyring.
func (s *KeyringStore) GetBridgePassword(accountID string) (string, error) {
	return s.get(EnvBridgePassword, BridgeUser(accountID))
}

// SetBridgePassword stores the Bridge password for accountID in the keyring.
func (s *KeyringStore) SetBridgePassword(accountID, password string) error {
	return s.set(BridgeUser(accountID), password)
}

// DeleteBridgePassword removes the keyring entry for accountID. Deleting a
// missing entry is not an error.
func (s *KeyringStore) DeleteBridgePassword(accountID string) error {
	return s.del(BridgeUser(accountID))
}

// StoredLLMKey returns the key that is stored in the keyring, ignoring the
// environment. Rollback needs this: GetLLMKey would return an environment value
// and write it into the keyring. ErrNotFound means the keyring has no entry.
func (s *KeyringStore) StoredLLMKey(provider string) (string, error) {
	v, err := keyring.Get(s.service, LLMUser(provider))
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read %s from keyring: %w", LLMUser(provider), err)
	}
	return v, nil
}

// GetLLMKey returns the API key for provider. The per-provider environment
// variable takes precedence over the keyring. An empty result with ErrNotFound
// means no key is configured.
func (s *KeyringStore) GetLLMKey(provider string) (string, error) {
	return s.get(LLMEnvVar(provider), LLMUser(provider))
}

// SetLLMKey stores the API key for provider in the keyring.
func (s *KeyringStore) SetLLMKey(provider, key string) error {
	return s.set(LLMUser(provider), key)
}

// DeleteLLMKey removes the keyring entry for provider. Deleting a missing entry
// is not an error.
func (s *KeyringStore) DeleteLLMKey(provider string) error {
	return s.del(LLMUser(provider))
}

// Probe reports whether the keyring can be used. It only reads a probe entry
// that is never written, so it does not change any stored secret. A missing
// entry means the keyring works. Any other error returns an error wrapping
// ErrKeyringUnavailable.
func (s *KeyringStore) Probe() error {
	_, err := keyring.Get(s.service, "probe")
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: no working Secret Service (Linux: start a provider such as gnome-keyring or KeePassXC) "+
		"or Credential Manager (Windows). Details: %v", ErrKeyringUnavailable, err)
}

func (s *KeyringStore) get(envVar, user string) (string, error) {
	if v := s.getenv(envVar); v != "" {
		return v, nil
	}
	v, err := keyring.Get(s.service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read %s from keyring: %w", user, err)
	}
	return v, nil
}

func (s *KeyringStore) set(user, value string) error {
	if err := keyring.Set(s.service, user, value); err != nil {
		return fmt.Errorf("write %s to keyring: %w", user, err)
	}
	return nil
}

func (s *KeyringStore) del(user string) error {
	err := keyring.Delete(s.service, user)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete %s from keyring: %w", user, err)
	}
	return nil
}
