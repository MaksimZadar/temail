package services

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/secrets"
)

const (
	pinnedFP  = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	presentFP = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

// dialTo returns a Dialer that succeeds (with a closed pipe) or fails.
func dialTo(ok bool) Dialer {
	return func(context.Context, string, string) (net.Conn, error) {
		if !ok {
			return nil, errors.New("connection refused")
		}
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}
}

func newSetup(t *testing.T, cfg *fakeConfig, sec *fakeSecrets, c *fakeClient, reach bool) (*SetupService, *clientFactory) {
	t.Helper()
	f := &clientFactory{c: c}
	s := NewSetupService(cfg, sec, f.new, nil)
	s.dial = dialTo(reach)
	return s, f
}

func TestGetStatus(t *testing.T) {
	pinned := readyConfig()
	unpinned := readyConfig()
	unpinned.Bridge[0].CertFingerprint = ""
	tests := []struct {
		name       string
		cfg        config.Config
		probeErr   error
		reach      bool
		want       SetupStatus
		wantKeyErr bool
	}{
		{
			name:  "nothing configured, keyring ok",
			cfg:   config.Default(),
			reach: true,
			want:  SetupStatus{KeyringOK: true},
		},
		{
			name:  "configured but not pinned, bridge down",
			cfg:   unpinned,
			reach: false,
			want:  SetupStatus{Configured: true, KeyringOK: true, AccountID: "main"},
		},
		{
			name:  "pinned and reachable",
			cfg:   pinned,
			reach: true,
			want:  SetupStatus{Configured: true, KeyringOK: true, CertPinned: true, BridgeReachable: true, AccountID: "main"},
		},
		{
			name:       "keyring broken is reported with a message",
			cfg:        pinned,
			probeErr:   secrets.ErrKeyringUnavailable,
			reach:      true,
			wantKeyErr: true,
			want:       SetupStatus{Configured: true, CertPinned: true, BridgeReachable: true, AccountID: "main"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec := newFakeSecrets()
			sec.probeErr = tt.probeErr
			s, _ := newSetup(t, newFakeConfig(tt.cfg), sec, &fakeClient{}, tt.reach)
			got, err := s.GetStatus()
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantKeyErr {
				if got.KeyringOK || got.KeyringMessage == "" {
					t.Fatalf("want keyring failure with a message, got %+v", got)
				}
				got.KeyringOK, got.KeyringMessage = false, ""
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGetStatusNeverDialsNonLoopback(t *testing.T) {
	c := readyConfig()
	c.Bridge[0].Host = "mail.example.com"
	dialed := false
	s, _ := newSetup(t, newFakeConfig(c), newFakeSecrets(), &fakeClient{}, true)
	s.dial = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("should not be called")
	}
	got, err := s.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	if dialed || got.BridgeReachable {
		t.Fatalf("non-loopback host was dialled or reported reachable: %+v", got)
	}
}

func TestSaveAccountValidation(t *testing.T) {
	tests := []struct {
		name                    string
		host                    string
		imap, smtp              int
		tls, username, password string
	}{
		{"remote host refused", "mail.example.com", 1143, 1025, "starttls", "me", testPassword},
		{"empty host", "", 1143, 1025, "starttls", "me", testPassword},
		{"empty username", "127.0.0.1", 1143, 1025, "starttls", "  ", testPassword},
		{"empty password", "127.0.0.1", 1143, 1025, "starttls", "me", ""},
		{"port zero", "127.0.0.1", 0, 1025, "starttls", "me", testPassword},
		{"smtp port out of range", "127.0.0.1", 1143, 70000, "starttls", "me", testPassword},
		{"unknown tls mode", "127.0.0.1", 1143, 1025, "plain", "me", testPassword},
		{"oversized password", "127.0.0.1", 1143, 1025, "starttls", "me", strings.Repeat("x", maxPasswordLen+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newFakeConfig(config.Default())
			sec := newFakeSecrets()
			s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)
			err := s.SaveAccount(tt.host, tt.imap, tt.smtp, tt.tls, tt.username, tt.password)
			if err == nil {
				t.Fatal("want an error")
			}
			if cfg.saveCount() != 0 || sec.sets != 0 {
				t.Fatalf("nothing may be written on invalid input (config saves %d, keyring sets %d)", cfg.saveCount(), sec.sets)
			}
		})
	}
}

func TestSaveAccountCreatesAccountAndKeepsSecretOutOfConfig(t *testing.T) {
	dir := t.TempDir()
	mgr := config.NewManager(dir)
	sec := newFakeSecrets()
	s := NewSetupService(mgr, sec, (&clientFactory{c: &fakeClient{}}).new, nil)

	if err := s.SaveAccount("127.0.0.1", 1143, 1025, "starttls", " me@proton.test ", testPassword); err != nil {
		t.Fatalf("SaveAccount: %v", err)
	}
	if sec.pw["main"] != testPassword {
		t.Fatal("password was not stored under account main")
	}
	raw, err := os.ReadFile(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), testPassword) {
		t.Fatal("password was written to config.json")
	}
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Bridge) != 1 || loaded.Bridge[0].ID != "main" || loaded.Bridge[0].Username != "me@proton.test" {
		t.Fatalf("unexpected account: %+v", loaded.Bridge)
	}
	if loaded.Bridge[0].CertFingerprint != "" {
		t.Fatal("new account must not be pinned")
	}
}

func TestSaveAccountUpdatesExistingAccount(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		imapPort int
		wantPin  string
	}{
		{"same endpoint keeps pin", "127.0.0.1", 1143, pinnedFP},
		{"changed port clears pin", "127.0.0.1", 2143, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newFakeConfig(readyConfig())
			sec := newFakeSecrets()
			s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)
			if err := s.SaveAccount(tt.host, tt.imapPort, 1025, "starttls", "new@proton.test", testPassword); err != nil {
				t.Fatal(err)
			}
			got := cfg.current()
			if len(got.Bridge) != 1 {
				t.Fatalf("want one account, got %d", len(got.Bridge))
			}
			a := got.Bridge[0]
			if a.ID != "main" || a.Username != "new@proton.test" || a.CertFingerprint != tt.wantPin {
				t.Fatalf("unexpected account %+v", a)
			}
			if sec.pw["main"] != testPassword {
				t.Fatal("password not updated")
			}
		})
	}
}

func TestSaveAccountKeyringFailureWritesNoConfig(t *testing.T) {
	cfg := newFakeConfig(config.Default())
	sec := newFakeSecrets()
	sec.setErr = secrets.ErrKeyringUnavailable
	s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)
	if err := s.SaveAccount("127.0.0.1", 1143, 1025, "starttls", "me", testPassword); !errors.Is(err, secrets.ErrKeyringUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if cfg.saveCount() != 0 {
		t.Fatal("config written although the keyring failed")
	}
}

func TestProbeCertReturnsPresentedCertificateWithoutPassword(t *testing.T) {
	c := &fakeClient{connectErr: &mail.UntrustedCertError{Fingerprint: presentFP, Subject: "CN=Bridge"}}
	s, _ := newSetup(t, newFakeConfig(readyConfig()), newFakeSecrets(), c, true)
	got, err := s.ProbeCert()
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != presentFP || got.Subject != "CN=Bridge" {
		t.Fatalf("got %+v", got)
	}
	for _, pw := range c.passwords {
		if pw != "" {
			t.Fatal("probe must not send a password")
		}
	}
}

func TestTrustCertPinsOnlyTheFreshFingerprint(t *testing.T) {
	tests := []struct {
		name    string
		given   string
		present string
		wantErr error
		wantPin string
	}{
		{"matching fingerprint is pinned", presentFP, presentFP, nil, presentFP},
		{"case-insensitive match is pinned", strings.ToUpper(presentFP), presentFP, nil, presentFP},
		{"mismatch is refused", pinnedFP, presentFP, ErrFingerprintChanged, ""},
		{"malformed fingerprint is refused", "zz", presentFP, ErrInvalidFingerprint, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newFakeConfig(readyConfig())
			cfg.cfg.Bridge[0].CertFingerprint = ""
			c := &fakeClient{connectErr: &mail.UntrustedCertError{Fingerprint: tt.present, Subject: "CN=Bridge"}}
			s, _ := newSetup(t, cfg, newFakeSecrets(), c, true)
			err := s.TrustCert(tt.given)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if cfg.saveCount() != 0 || cfg.current().Bridge[0].CertFingerprint != "" {
					t.Fatal("a refused pin must not be written")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.current().Bridge[0].CertFingerprint; got != tt.wantPin {
				t.Fatalf("pin = %q, want %q", got, tt.wantPin)
			}
		})
	}
}

func TestTrustCertRefusesWhenNoAccount(t *testing.T) {
	s, _ := newSetup(t, newFakeConfig(config.Default()), newFakeSecrets(), &fakeClient{}, true)
	if err := s.TrustCert(presentFP); !errors.Is(err, bridge.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestTestConnectionCategories(t *testing.T) {
	cacheErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	tests := []struct {
		name    string
		cfg     config.Config
		noPw    bool
		pwErr   error
		connErr error
		wantCat string
		wantOK  bool
	}{
		{"success", readyConfig(), false, nil, nil, CategoryOK, true},
		{"no account", config.Default(), false, nil, nil, CategoryNotConfigured, false},
		{"no password", readyConfig(), true, nil, nil, CategoryNoPassword, false},
		{"keyring broken", readyConfig(), false, secrets.ErrKeyringUnavailable, nil, CategoryKeyringDown, false},
		{"bridge unreachable", readyConfig(), false, nil, cacheErr, CategoryBridgeUnreach, false},
		{"untrusted cert", readyConfig(), false, nil, &mail.UntrustedCertError{Fingerprint: presentFP}, CategoryUntrustedCert, false},
		{"cert mismatch", readyConfig(), false, nil, &mail.CertMismatchError{Pinned: pinnedFP, Fingerprint: presentFP}, CategoryCertMismatch, false},
		{"auth failed", readyConfig(), false, nil, mail.ErrAuthFailed, CategoryAuthFailed, false},
		{"invalid config", readyConfig(), false, nil, mail.ErrNotLoopback, CategoryInvalidConfig, false},
		{"other", readyConfig(), false, nil, errors.New("tls handshake exploded"), CategoryOther, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sec := newFakeSecrets()
			if !tt.noPw && tt.cfg.Bridge != nil {
				sec.pw["main"] = testPassword
			}
			sec.getErr = tt.pwErr
			c := &fakeClient{connectErr: tt.connErr}
			s, _ := newSetup(t, newFakeConfig(tt.cfg), sec, c, true)
			got, err := s.TestConnection()
			if err != nil {
				t.Fatal(err)
			}
			if got.Category != tt.wantCat || got.OK != tt.wantOK || got.Message == "" {
				t.Fatalf("got %+v, want category %s", got, tt.wantCat)
			}
			out, _ := json.Marshal(got)
			if strings.Contains(string(out), testPassword) {
				t.Fatal("password leaked into the connection test result")
			}
			for _, pw := range c.passwords {
				if pw != testPassword && pw != "" {
					t.Fatalf("unexpected password %q", pw)
				}
			}
		})
	}
}

func TestTestConnectionUsesStoredPasswordReadOnly(t *testing.T) {
	sec := newFakeSecrets()
	sec.pw["main"] = testPassword
	c := &fakeClient{}
	s, _ := newSetup(t, newFakeConfig(readyConfig()), sec, c, true)
	if _, err := s.TestConnection(); err != nil {
		t.Fatal(err)
	}
	if len(c.passwords) != 1 || c.passwords[0] != testPassword {
		t.Fatalf("login did not use the stored password: %v", c.passwords)
	}
	if m, a := c.fetches(); m != 0 || a != 0 {
		t.Fatal("connection test must not fetch mail")
	}
}

func TestCategorizeNil(t *testing.T) {
	if got := categorize(nil); !got.OK || got.Category != CategoryOK {
		t.Fatalf("got %+v", got)
	}
}

func TestErrorsMentionNoSecret(t *testing.T) {
	sec := newFakeSecrets()
	sec.pw["main"] = testPassword
	sec.getErr = errors.New("dbus: " + "no service")
	c := &fakeClient{}
	s, _ := newSetup(t, newFakeConfig(readyConfig()), sec, c, true)
	res, _ := s.TestConnection()
	if strings.Contains(res.Message, testPassword) {
		t.Fatal("secret in message")
	}
}
