package bridge

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/secrets"
)

const testPassword = "bridge-pw-SECRET-123"

// fakeClient implements mail.Client. connect decides the Connect outcome.
type fakeClient struct {
	mu        sync.Mutex
	connect   func(acct config.BridgeAccount, password string) error
	connects  int
	closes    int
	lastPass  string
	lastAcct  config.BridgeAccount
	mailboxes []model.Mailbox
}

func (c *fakeClient) Connect(_ context.Context, acct config.BridgeAccount, password string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connects++
	c.lastPass = password
	c.lastAcct = acct
	if c.connect == nil {
		return nil
	}
	return c.connect(acct, password)
}

func (c *fakeClient) ListMailboxes(context.Context) ([]model.Mailbox, error) {
	return c.mailboxes, nil
}
func (c *fakeClient) MailboxInfo(context.Context, string) (mail.MailboxInfo, error) {
	return mail.MailboxInfo{}, nil
}
func (c *fakeClient) FetchHeaders(context.Context, string, mail.UIDRange) ([]model.MessageHeader, error) {
	return nil, nil
}
func (c *fakeClient) ListUIDs(context.Context, string) (mail.UIDListing, error) {
	return mail.UIDListing{}, nil
}
func (c *fakeClient) FetchMessage(context.Context, string, uint32) (model.Message, error) {
	return model.Message{}, nil
}
func (c *fakeClient) FetchAttachment(context.Context, string, uint32, string) ([]byte, error) {
	return nil, nil
}
func (c *fakeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}

type fakeSecrets struct {
	pw  string
	err error
}

func (f fakeSecrets) GetBridgePassword(string) (string, error) { return f.pw, f.err }

type fakeConfig struct{ cfg config.Config }

func (f fakeConfig) Load() (config.Config, error) { return f.cfg, nil }

func oneAccount() config.Config {
	c := config.Default()
	c.Bridge = []config.BridgeAccount{config.DefaultBridgeAccount("main", "me@proton")}
	return c
}

func TestPickAccount(t *testing.T) {
	two := oneAccount()
	two.Bridge = append(two.Bridge, config.DefaultBridgeAccount("second", "two@proton"))
	tests := []struct {
		name    string
		cfg     config.Config
		id      string
		want    int
		wantErr error
	}{
		{"empty config", config.Default(), "", 0, ErrNotConfigured},
		{"empty id takes first", two, "", 0, nil},
		{"by id", two, "second", 1, nil},
		{"unknown id", two, "nope", 0, ErrUnknownAccount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PickAccount(tt.cfg, tt.id)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

func TestPasswordErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		sec     fakeSecrets
		wantErr error
		wantOK  bool
	}{
		{"found", fakeSecrets{pw: testPassword}, nil, true},
		{"missing", fakeSecrets{err: secrets.ErrNotFound}, ErrNoPassword, false},
		{"keyring broken", fakeSecrets{err: errors.New("dbus: no service")}, secrets.ErrKeyringUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pw, err := Password(tt.sec, "main")
			if tt.wantOK {
				if err != nil || pw != testPassword {
					t.Fatalf("got %q, %v", pw, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), testPassword) {
				t.Fatal("password leaked into error")
			}
		})
	}
}

func TestCredentialsNeedsAccountAndPassword(t *testing.T) {
	if _, _, err := Credentials(fakeConfig{config.Default()}, fakeSecrets{pw: testPassword}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty config: %v", err)
	}
	if _, _, err := Credentials(fakeConfig{oneAccount()}, fakeSecrets{err: secrets.ErrNotFound}); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("no password: %v", err)
	}
	acct, pw, err := Credentials(fakeConfig{oneAccount()}, fakeSecrets{pw: testPassword})
	if err != nil || acct.ID != "main" || pw != testPassword {
		t.Fatalf("got %+v %q %v", acct, pw, err)
	}
}

func TestProbeCertNeverSendsPassword(t *testing.T) {
	const fp = "ab00000000000000000000000000000000000000000000000000000000000000"
	c := &fakeClient{connect: func(acct config.BridgeAccount, _ string) error {
		if acct.CertFingerprint != "" {
			return errors.New("probe must clear the pin")
		}
		return &mail.UntrustedCertError{Fingerprint: fp, Subject: "CN=Bridge"}
	}}
	acct := oneAccount().Bridge[0]
	acct.CertFingerprint = strings.Repeat("cd", 32)

	got, subject, err := ProbeCert(context.Background(), c, acct)
	if err != nil {
		t.Fatalf("ProbeCert: %v", err)
	}
	if got != fp || subject != "CN=Bridge" {
		t.Fatalf("got %q %q", got, subject)
	}
	if c.lastPass != "" {
		t.Fatal("probe sent a password")
	}
	if c.closes == 0 {
		t.Fatal("probe did not close the client")
	}
}

func TestProbeCertPassesOtherErrors(t *testing.T) {
	boom := errors.New("connection refused")
	c := &fakeClient{connect: func(config.BridgeAccount, string) error { return boom }}
	if _, _, err := ProbeCert(context.Background(), c, oneAccount().Bridge[0]); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsurePinnedUnreachableSavesNothing(t *testing.T) {
	// Grab a free loopback port and close it, so the dial is refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	acct := oneAccount().Bridge[0]
	acct.IMAPPort = port
	cfg := oneAccount()
	cfg.Bridge[0] = acct
	mgr := config.NewManager(t.TempDir())
	if err := EnsurePinned(context.Background(), mgr, &cfg, 0, testPassword, true, nil); err == nil {
		t.Fatal("want an error for a refused connection")
	}
	if _, err := os.Stat(mgr.Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("config was written after a failed connection: %v", err)
	}
}

func TestSessionConnectsOnceAndReconnectsAfterFailure(t *testing.T) {
	var clients []*fakeClient
	resolve := func(context.Context) (config.BridgeAccount, string, error) {
		return oneAccount().Bridge[0], testPassword, nil
	}
	newClient := func() mail.Client {
		c := &fakeClient{}
		clients = append(clients, c)
		return c
	}
	s := NewSession(resolve, newClient)
	ctx := context.Background()

	calls := 0
	ok := func(context.Context, mail.Client) error { calls++; return nil }
	for i := 0; i < 2; i++ {
		if err := s.With(ctx, ok); err != nil {
			t.Fatal(err)
		}
	}
	if len(clients) != 1 || clients[0].connects != 1 {
		t.Fatalf("want one connection for two calls, got %d clients", len(clients))
	}
	if clients[0].lastPass != testPassword {
		t.Fatal("session did not use the resolved password")
	}

	fail := errors.New("transport gone")
	if err := s.With(ctx, func(context.Context, mail.Client) error { return fail }); !errors.Is(err, fail) {
		t.Fatalf("err = %v", err)
	}
	if clients[0].closes == 0 {
		t.Fatal("failed call did not close the connection")
	}
	if err := s.With(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if len(clients) != 2 {
		t.Fatalf("want a reconnect after failure, got %d clients", len(clients))
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if clients[1].closes == 0 {
		t.Fatal("Close did not close the connection")
	}
}

func TestSessionResolveErrorIsReturned(t *testing.T) {
	s := NewSession(func(context.Context) (config.BridgeAccount, string, error) {
		return config.BridgeAccount{}, "", ErrNoPassword
	}, func() mail.Client { t.Fatal("no client without credentials"); return nil })
	if err := s.With(context.Background(), func(context.Context, mail.Client) error { return nil }); !errors.Is(err, ErrNoPassword) {
		t.Fatalf("err = %v", err)
	}
}
