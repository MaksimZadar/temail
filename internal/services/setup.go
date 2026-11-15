package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
)

// DefaultAccountID is the ID given to the account created by SaveAccount.
const DefaultAccountID = "main"

// SetupService covers first-run setup and Bridge connection checks. Its
// SaveAccount is the only bound method that accepts a secret, and the secret
// goes straight to the keyring.
//
// Every config change (SaveAccount, TrustCert) holds cfgMu for its whole
// load-check-save sequence, so two calls cannot interleave and lose an update.
type SetupService struct {
	cfg       ConfigStore
	sec       SecretStore
	newClient ClientFactory
	dial      Dialer
	sync      *SyncService // may be nil; restarted after account or pin changes

	cfgMu sync.Mutex
	kr    keyringCache
	// keyringWait overrides keyringProbeWait in tests. Zero means the default.
	keyringWait time.Duration
}

// NewSetupService returns a SetupService. sync, if non-nil, is restarted after
// SaveAccount and TrustCert succeed.
func NewSetupService(cfg ConfigStore, sec SecretStore, newClient ClientFactory, sync *SyncService) *SetupService {
	return &SetupService{cfg: cfg, sec: sec, newClient: newClient, dial: defaultDialer, sync: sync}
}

// keyringCache holds the last keyring probe result for keyringTTL. Probes run
// in a goroutine, so a locked keychain cannot hang a status call.
type keyringCache struct {
	mu        sync.Mutex
	checkedAt time.Time
	err       error
	inflight  chan struct{}
}

// SetupStatus describes what is configured and working. It holds no secrets.
type SetupStatus struct {
	Configured      bool   `json:"configured"`
	KeyringOK       bool   `json:"keyringOk"`
	KeyringMessage  string `json:"keyringMessage"`
	BridgeReachable bool   `json:"bridgeReachable"`
	CertPinned      bool   `json:"certPinned"`
	AccountID       string `json:"accountId"`
}

// CertProbe is the certificate Bridge presents. Compare it with Proton Bridge
// before calling TrustCert.
type CertProbe struct {
	Fingerprint string `json:"fingerprint"`
	Subject     string `json:"subject"`
}

// ConnectionTest is the outcome of TestConnection. Message is for display and
// never contains a secret.
type ConnectionTest struct {
	OK       bool   `json:"ok"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// GetStatus reports the setup state. Reachability is a TCP dial to the
// configured loopback host and port with a short timeout. Non-loopback hosts
// are never dialled. The keyring probe is bounded by a timeout and cached.
func (s *SetupService) GetStatus(ctx context.Context) (SetupStatus, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return SetupStatus{}, err
	}
	var st SetupStatus
	if kerr := s.probeKeyring(ctx); kerr != nil {
		st.KeyringMessage = kerr.Error()
	} else {
		st.KeyringOK = true
	}

	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return st, nil
	}
	acct := cfg.Bridge[idx]
	st.Configured = true
	st.AccountID = acct.ID
	st.CertPinned = acct.CertFingerprint != ""
	st.BridgeReachable = s.reachable(ctx, acct)
	return st, nil
}

// probeKeyring returns the keyring state. A result is reused for keyringTTL. If
// no fresh result arrives within the wait, it returns ErrKeyringLocked. The
// probe keeps running and updates the cache when it finishes.
func (s *SetupService) probeKeyring(ctx context.Context) error {
	kc := &s.kr
	kc.mu.Lock()
	if !kc.checkedAt.IsZero() && time.Since(kc.checkedAt) < keyringTTL {
		err := kc.err
		kc.mu.Unlock()
		return err
	}
	ch := kc.inflight
	if ch == nil {
		ch = make(chan struct{})
		kc.inflight = ch
		go func() {
			err := s.sec.Probe()
			kc.mu.Lock()
			kc.err, kc.checkedAt, kc.inflight = err, time.Now(), nil
			kc.mu.Unlock()
			close(ch)
		}()
	}
	kc.mu.Unlock()

	wait := s.keyringWait
	if wait <= 0 {
		wait = keyringProbeWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
		kc.mu.Lock()
		err := kc.err
		kc.mu.Unlock()
		return err
	case <-timer.C:
		return ErrKeyringLocked
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *SetupService) loadConfig() (config.Config, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func (s *SetupService) reachable(ctx context.Context, acct config.BridgeAccount) bool {
	if !mail.IsLoopbackHost(acct.Host) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, reachTimeout)
	defer cancel()
	addr := net.JoinHostPort(acct.Host, strconv.Itoa(acct.IMAPPort))
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// SaveAccount creates or updates the Bridge account.
//
// Order: validate, then write config.json, then the keyring. If the keyring
// write fails, the previous config is restored, so config and keyring never
// disagree. An empty password keeps the password already in the keyring. If
// there is none, the call fails with no_password.
//
// If the host or IMAP port changes, the pinned certificate is cleared, because
// it belongs to the old endpoint. The password is never returned or logged.
func (s *SetupService) SaveAccount(ctx context.Context, host string, imapPort, smtpPort int, tlsMode, username, password string) error {
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	switch {
	case host == "" || len(host) > maxHostLen:
		return fmt.Errorf("%w: host", ErrInvalidInput)
	case !mail.IsLoopbackHost(host):
		return fmt.Errorf("%w: the Bridge host must be 127.0.0.1, ::1 or localhost", ErrInvalidInput)
	case username == "" || len(username) > maxUsernameLen:
		return fmt.Errorf("%w: username", ErrInvalidInput)
	case len(password) > maxPasswordLen:
		return fmt.Errorf("%w: password", ErrInvalidInput)
	}

	if err := s.saveAccountLocked(host, imapPort, smtpPort, tlsMode, username, password); err != nil {
		return err
	}
	s.restartSync()
	return nil
}

func (s *SetupService) saveAccountLocked(host string, imapPort, smtpPort int, tlsMode, username, password string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	old, err := s.loadConfig()
	if err != nil {
		return err
	}
	cfg := copyConfig(old)
	idx, perr := bridge.PickAccount(cfg, "")
	existed := perr == nil
	if !existed {
		cfg.Bridge = append(cfg.Bridge, config.DefaultBridgeAccount(DefaultAccountID, username))
		idx = len(cfg.Bridge) - 1
	}
	acct := cfg.Bridge[idx]
	if acct.Host != host || acct.IMAPPort != imapPort {
		acct.CertFingerprint = ""
	}
	acct.Host = host
	acct.IMAPPort = imapPort
	acct.SMTPPort = smtpPort
	acct.TLSMode = tlsMode
	acct.Username = username
	cfg.Bridge[idx] = acct
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	if password == "" {
		// Keep the stored password. Without one, the account cannot connect.
		if !existed {
			return fmt.Errorf("%w: enter the Bridge password", bridge.ErrNoPassword)
		}
		if _, err := bridge.Password(s.sec, acct.ID); err != nil {
			return err
		}
	}

	// Config first. Nothing secret is written before the config is saved.
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if password == "" {
		return nil
	}
	if err := s.sec.SetBridgePassword(acct.ID, password); err != nil {
		if rerr := s.cfg.Save(old); rerr != nil {
			return fmt.Errorf("store Bridge password: %w (restoring the previous config also failed: %v)", err, rerr)
		}
		return fmt.Errorf("store Bridge password: %w", err)
	}
	return nil
}

// ProbeCert shows the fingerprint and subject of the certificate Bridge
// presents. No password is sent. The result is public, and nothing is pinned.
func (s *SetupService) ProbeCert(ctx context.Context) (CertProbe, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return CertProbe{}, err
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return CertProbe{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	fp, subject, err := bridge.ProbeCert(ctx, s.newClient(), cfg.Bridge[idx])
	if err != nil {
		return CertProbe{}, err
	}
	return CertProbe{Fingerprint: fp, Subject: subject}, nil
}

// TrustCert pins fingerprint for the account.
//
// If the account already has a different pin, replace must be true, or the
// call fails with ErrAlreadyPinned (the UI then shows the re-pin flow). The
// certificate is probed again, and the pin is saved only if the fresh
// fingerprint equals fingerprint. The config is re-read after the probe. If the
// host, port, TLS mode or ID changed in the meantime, the call fails with
// ErrAccountChanged and nothing is written.
func (s *SetupService) TrustCert(ctx context.Context, fingerprint string, replace bool) error {
	fp := strings.ToLower(strings.TrimSpace(fingerprint))
	if !config.ValidFingerprint(fp) {
		return ErrInvalidFingerprint
	}
	snap, err := s.loadConfig()
	if err != nil {
		return err
	}
	idx, err := bridge.PickAccount(snap, "")
	if err != nil {
		return err
	}
	acct := snap.Bridge[idx]
	if err := checkPinReplace(acct, fp, replace); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	presented, _, err := bridge.ProbeCert(ctx, s.newClient(), acct)
	if err != nil {
		return err
	}
	if presented != fp {
		return ErrFingerprintChanged
	}

	if err := s.pinLocked(acct, fp, replace); err != nil {
		return err
	}
	s.restartSync()
	return nil
}

func (s *SetupService) pinLocked(probed config.BridgeAccount, fp string, replace bool) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cur, err := s.loadConfig()
	if err != nil {
		return err
	}
	idx, err := bridge.PickAccount(cur, "")
	if err != nil {
		return err
	}
	acct := cur.Bridge[idx]
	if acct.ID != probed.ID || acct.Host != probed.Host || acct.IMAPPort != probed.IMAPPort || acct.TLSMode != probed.TLSMode {
		return ErrAccountChanged
	}
	if err := checkPinReplace(acct, fp, replace); err != nil {
		return err
	}
	cfg := copyConfig(cur)
	cfg.Bridge[idx].CertFingerprint = fp
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save pinned fingerprint: %w", err)
	}
	return nil
}

// checkPinReplace refuses to overwrite a different pin unless replace is set.
// Pinning the value that is already stored is a no-op.
func checkPinReplace(acct config.BridgeAccount, fp string, replace bool) error {
	if acct.CertFingerprint != "" && acct.CertFingerprint != fp && !replace {
		return ErrAlreadyPinned
	}
	return nil
}

// TestConnection logs in to Bridge with the stored settings and password,
// then closes the connection. It reads nothing from the mailbox. Failures are
// returned as a category, not as an error, so the UI can show a clear message.
func (s *SetupService) TestConnection(ctx context.Context) (ConnectionTest, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return ConnectionTest{}, err
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return categorize(err), nil
	}
	acct := cfg.Bridge[idx]
	pw, err := bridge.Password(s.sec, acct.ID)
	if err != nil {
		return categorize(err), nil
	}

	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	client := s.newClient()
	err = client.Connect(ctx, acct, pw)
	_ = client.Close()
	if err != nil {
		return categorize(err), nil
	}
	return ConnectionTest{OK: true, Category: CategoryOK, Message: "Connected to Bridge and logged in."}, nil
}

func (s *SetupService) restartSync() {
	if s.sync != nil {
		s.sync.restart()
	}
}

// categorize maps an error to a connection-test category and a message.
// Passwords never reach this function, and the messages do not add them.
func categorize(err error) ConnectionTest {
	if err == nil {
		return ConnectionTest{OK: true, Category: CategoryOK, Message: "Connected to Bridge and logged in."}
	}
	cat, msg := CategoryOther, "Could not connect to Bridge: "+err.Error()
	var opErr *net.OpError
	switch {
	case errors.Is(err, bridge.ErrNotConfigured):
		cat, msg = CategoryNotConfigured, "No Bridge account is set up yet."
	case errors.Is(err, bridge.ErrNoPassword):
		cat, msg = CategoryNoPassword, "No Bridge password is stored. Enter it in setup."
	case errors.Is(err, secrets.ErrKeyringUnavailable):
		cat, msg = CategoryKeyringDown, "The OS keychain is not available. Start a Secret Service provider such as gnome-keyring or KeePassXC."
	case errors.Is(err, mail.ErrCertMismatch):
		cat, msg = CategoryCertMismatch, "The Bridge certificate changed since it was pinned. Check the new fingerprint in Proton Bridge before re-pinning."
	case errors.Is(err, mail.ErrUntrustedCert):
		cat, msg = CategoryUntrustedCert, "The Bridge certificate is not trusted yet. Confirm its fingerprint in Proton Bridge."
	case errors.Is(err, mail.ErrAuthFailed):
		cat, msg = CategoryAuthFailed, "Bridge refused the login. Check the username and the Bridge password."
	case errors.Is(err, mail.ErrNotLoopback), errors.Is(err, config.ErrInvalid):
		cat, msg = CategoryInvalidConfig, "The Bridge settings are not valid: "+err.Error()
	case errors.As(err, &opErr) && opErr.Op == "dial":
		cat, msg = CategoryBridgeUnreach, "Bridge is not reachable. Check that Proton Bridge is running and the ports are correct."
	}
	return ConnectionTest{OK: false, Category: cat, Message: msg}
}
