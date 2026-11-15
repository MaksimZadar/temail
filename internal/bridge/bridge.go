// Package bridge holds the connect, pin and password flow shared by the app
// services and the manual tools (cmd/mailsync, cmd/mailprobe). It sits between
// internal/config, internal/secrets and internal/mail and has no UI or Wails
// knowledge. See README.md.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
)

// ErrNotConfigured means config.json has no Bridge account.
var ErrNotConfigured = errors.New("no Bridge account is configured")

// ErrUnknownAccount means the requested account ID is not in config.json.
var ErrUnknownAccount = errors.New("no such Bridge account")

// ErrNotPinned means the account has no pinned certificate yet.
var ErrNotPinned = errors.New("the Bridge certificate is not trusted yet")

// ErrNoPassword means the keyring (and the environment) hold no Bridge
// password for the account.
var ErrNoPassword = errors.New("no Bridge password is stored")

// ConfigSource loads the non-secret configuration. *config.Manager satisfies it.
type ConfigSource interface {
	Load() (config.Config, error)
}

// PasswordSource reads Bridge passwords. secrets.Store satisfies it.
type PasswordSource interface {
	GetBridgePassword(accountID string) (string, error)
}

// PickAccount returns the index of the account with the given ID, or of the
// first account when id is empty.
func PickAccount(cfg config.Config, id string) (int, error) {
	if len(cfg.Bridge) == 0 {
		return 0, fmt.Errorf("%w; add one first", ErrNotConfigured)
	}
	if id == "" {
		return 0, nil
	}
	for i, a := range cfg.Bridge {
		if a.ID == id {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q is not in config.json", ErrUnknownAccount, id)
}

// Password reads the Bridge password for accountID. A missing password is
// ErrNoPassword. Any other keyring read failure is reported as
// secrets.ErrKeyringUnavailable. The password is never part of an error.
func Password(sec PasswordSource, accountID string) (string, error) {
	pw, err := sec.GetBridgePassword(accountID)
	switch {
	case err == nil:
		return pw, nil
	case errors.Is(err, secrets.ErrNotFound):
		return "", fmt.Errorf("%w for account %q: store it in the keyring or set %s",
			ErrNoPassword, accountID, secrets.EnvBridgePassword)
	default:
		return "", fmt.Errorf("%w: read Bridge password: %v", secrets.ErrKeyringUnavailable, err)
	}
}

// Credentials loads config.json, picks the first account and reads its
// password. It does not check the pin.
func Credentials(cfg ConfigSource, sec PasswordSource) (config.BridgeAccount, string, error) {
	c, err := cfg.Load()
	if err != nil {
		return config.BridgeAccount{}, "", fmt.Errorf("load config: %w", err)
	}
	idx, err := PickAccount(c, "")
	if err != nil {
		return config.BridgeAccount{}, "", err
	}
	acct := c.Bridge[idx]
	pw, err := Password(sec, acct.ID)
	if err != nil {
		return config.BridgeAccount{}, "", err
	}
	return acct, pw, nil
}

// ProbeCert reveals the certificate that Bridge presents without logging in.
// It connects with the pin cleared, so the handshake always stops at the
// certificate check with *mail.UntrustedCertError, and no password is sent.
// The client is closed before returning.
func ProbeCert(ctx context.Context, client mail.Client, acct config.BridgeAccount) (fingerprint, subject string, err error) {
	defer func() { _ = client.Close() }()
	acct.CertFingerprint = ""
	err = client.Connect(ctx, acct, "")
	var untrusted *mail.UntrustedCertError
	if errors.As(err, &untrusted) {
		return untrusted.Fingerprint, untrusted.Subject, nil
	}
	if err == nil {
		return "", "", errors.New("bridge accepted a connection without a pinned certificate")
	}
	return "", "", err
}

// EnsurePinned makes one connection attempt to see whether the Bridge
// certificate is pinned. If it is not, and trust is set, it saves the presented
// fingerprint into cfg[idx] and writes config.json. report, if non-nil,
// receives the presented fingerprint and subject so the caller can show them.
// The caller must compare the fingerprint with Proton Bridge before trusting it.
func EnsurePinned(ctx context.Context, mgr *config.Manager, cfg *config.Config, idx int, password string,
	trust bool, report func(fingerprint, subject string)) error {
	c := mail.NewClient()
	err := c.Connect(ctx, cfg.Bridge[idx], password)
	_ = c.Close()
	if err == nil {
		return nil
	}
	var untrusted *mail.UntrustedCertError
	if !errors.As(err, &untrusted) {
		return err
	}
	if report != nil {
		report(untrusted.Fingerprint, untrusted.Subject)
	}
	if !trust {
		return errors.New("certificate not trusted; re-run with -trust after checking the fingerprint")
	}
	cfg.Bridge[idx].CertFingerprint = untrusted.Fingerprint
	if err := mgr.Save(*cfg); err != nil {
		return fmt.Errorf("save pinned fingerprint: %w", err)
	}
	return nil
}

// ErrSessionClosed is returned by Session.With after Close.
var ErrSessionClosed = errors.New("bridge session is closed")

// Session keeps one lazily connected mail.Client for on-demand reads (for
// example a body that is not cached yet). Calls are serialised: a call waits
// for its turn, and the wait ends when its context does. A call that fails
// closes the connection, so the next call reconnects. Close is final: it may be
// called while a call is running, and no client is stored after it.
type Session struct {
	resolve   func(context.Context) (config.BridgeAccount, string, error)
	newClient func() mail.Client

	// slot has capacity 1. Holding a token is the right to run With.
	slot chan struct{}

	stMu   sync.Mutex // guards client and closed
	client mail.Client
	closed bool
}

// NewSession returns a Session. resolve supplies the account and password for
// each (re)connect; newClient makes the client.
func NewSession(resolve func(context.Context) (config.BridgeAccount, string, error), newClient func() mail.Client) *Session {
	return &Session{resolve: resolve, newClient: newClient, slot: make(chan struct{}, 1)}
}

// With runs fn on a connected client, connecting first if needed. If ctx ends
// while the call is still queued, With returns ctx.Err() without running fn.
func (s *Session) With(ctx context.Context, fn func(ctx context.Context, c mail.Client) error) error {
	select {
	case s.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.slot }()

	c, err := s.connected(ctx)
	if err != nil {
		return err
	}
	if err := fn(ctx, c); err != nil {
		s.drop(c)
		return err
	}
	return nil
}

func (s *Session) connected(ctx context.Context) (mail.Client, error) {
	s.stMu.Lock()
	if s.closed {
		s.stMu.Unlock()
		return nil, ErrSessionClosed
	}
	c := s.client
	s.stMu.Unlock()
	if c != nil {
		return c, nil
	}

	acct, pw, err := s.resolve(ctx)
	if err != nil {
		return nil, err
	}
	c = s.newClient()
	if err := c.Connect(ctx, acct, pw); err != nil {
		_ = c.Close()
		return nil, err
	}
	s.stMu.Lock()
	if s.closed {
		// Close ran while we were connecting: do not keep the new client.
		s.stMu.Unlock()
		_ = c.Close()
		return nil, ErrSessionClosed
	}
	s.client = c
	s.stMu.Unlock()
	return c, nil
}

// drop forgets c if it is still the current client and closes it.
func (s *Session) drop(c mail.Client) {
	s.stMu.Lock()
	if s.client == c {
		s.client = nil
	}
	s.stMu.Unlock()
	_ = c.Close()
}

// Close marks the session closed and closes the current connection, if any. It
// does not wait for a running call; that call fails once its client is closed.
func (s *Session) Close() error {
	s.stMu.Lock()
	s.closed = true
	c := s.client
	s.client = nil
	s.stMu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}
