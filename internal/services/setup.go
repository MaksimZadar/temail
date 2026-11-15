package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

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
type SetupService struct {
	cfg       ConfigStore
	sec       SecretStore
	newClient ClientFactory
	dial      Dialer
	sync      *SyncService // may be nil; restarted after account or pin changes
}

// NewSetupService returns a SetupService. sync, if non-nil, is restarted after
// SaveAccount and TrustCert succeed.
func NewSetupService(cfg ConfigStore, sec SecretStore, newClient ClientFactory, sync *SyncService) *SetupService {
	return &SetupService{cfg: cfg, sec: sec, newClient: newClient, dial: defaultDialer, sync: sync}
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
// are never dialled.
func (s *SetupService) GetStatus() (SetupStatus, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return SetupStatus{}, fmt.Errorf("load config: %w", err)
	}
	var st SetupStatus
	if kerr := s.sec.Probe(); kerr != nil {
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
	st.BridgeReachable = s.reachable(acct)
	return st, nil
}

func (s *SetupService) reachable(acct config.BridgeAccount) bool {
	if !mail.IsLoopbackHost(acct.Host) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), reachTimeout)
	defer cancel()
	addr := net.JoinHostPort(acct.Host, strconv.Itoa(acct.IMAPPort))
	conn, err := s.dial(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// SaveAccount creates or updates the Bridge account. It validates the input,
// stores the password in the keyring and writes the other settings to
// config.json. If the host or IMAP port changes, the pinned certificate is
// cleared, because it belongs to the old endpoint. The password is never
// returned or logged.
func (s *SetupService) SaveAccount(host string, imapPort, smtpPort int, tlsMode, username, password string) error {
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)
	switch {
	case host == "" || len(host) > maxHostLen:
		return fmt.Errorf("%w: host", ErrInvalidInput)
	case !mail.IsLoopbackHost(host):
		return fmt.Errorf("%w: the Bridge host must be 127.0.0.1, ::1 or localhost", ErrInvalidInput)
	case username == "" || len(username) > maxUsernameLen:
		return fmt.Errorf("%w: username", ErrInvalidInput)
	case password == "" || len(password) > maxPasswordLen:
		return fmt.Errorf("%w: password", ErrInvalidInput)
	}

	cfg, err := s.cfg.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx, perr := bridge.PickAccount(cfg, "")
	if perr != nil {
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

	if err := s.sec.SetBridgePassword(acct.ID, password); err != nil {
		return fmt.Errorf("store Bridge password: %w", err)
	}
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	s.restartSync()
	return nil
}

// ProbeCert shows the fingerprint and subject of the certificate Bridge
// presents. No password is sent. The result is public, and nothing is pinned.
func (s *SetupService) ProbeCert() (CertProbe, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return CertProbe{}, fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return CertProbe{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	fp, subject, err := bridge.ProbeCert(ctx, s.newClient(), cfg.Bridge[idx])
	if err != nil {
		return CertProbe{}, err
	}
	return CertProbe{Fingerprint: fp, Subject: subject}, nil
}

// TrustCert pins fingerprint for the account. It probes the certificate again
// and pins only if the fresh fingerprint equals the one given. Otherwise it
// returns ErrFingerprintChanged and nothing is written.
func (s *SetupService) TrustCert(fingerprint string) error {
	fp := strings.ToLower(strings.TrimSpace(fingerprint))
	if !config.ValidFingerprint(fp) {
		return ErrInvalidFingerprint
	}
	cfg, err := s.cfg.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	presented, _, err := bridge.ProbeCert(ctx, s.newClient(), cfg.Bridge[idx])
	if err != nil {
		return err
	}
	if presented != fp {
		return ErrFingerprintChanged
	}

	cfg.Bridge[idx].CertFingerprint = fp
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save pinned fingerprint: %w", err)
	}
	s.restartSync()
	return nil
}

// TestConnection logs in to Bridge with the stored settings and password,
// then closes the connection. It reads nothing from the mailbox. Failures are
// returned as a category, not as an error, so the UI can show a clear message.
func (s *SetupService) TestConnection() (ConnectionTest, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return ConnectionTest{}, fmt.Errorf("load config: %w", err)
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

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
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
