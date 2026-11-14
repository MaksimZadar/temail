package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/MaksimZadar/temail/internal/config"
)

const (
	testUsername = "alice@proton.test"
	testPassword = "bridge-secret-password"
)

// writeRecorder counts what the client asked the server to do. It is filled by
// recordingSession on the server side, so it sees the real commands rather
// than parsed wire text.
type writeRecorder struct {
	mu        sync.Mutex
	writes    []string
	selectsRO []bool
	nonPeek   int
	fetches   int
}

// recorderStats is a copy of writeRecorder's counters.
type recorderStats struct {
	writes    []string
	selectsRO []bool
	nonPeek   int
	fetches   int
}

func (r *writeRecorder) stats() recorderStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return recorderStats{
		writes:    append([]string(nil), r.writes...),
		selectsRO: append([]bool(nil), r.selectsRO...),
		nonPeek:   r.nonPeek,
		fetches:   r.fetches,
	}
}

func (r *writeRecorder) addWrite(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, name)
}

// recordingSession wraps the in-memory server session. Every method that would
// change mailbox state records its name and returns an error, so a write
// command makes the operation fail. Select and Fetch record how they were
// called.
type recordingSession struct {
	imapserver.Session
	rec *writeRecorder
}

func (s *recordingSession) reject(name string) error {
	s.rec.addWrite(name)
	return errors.New("write command rejected by test server: " + name)
}

func (s *recordingSession) Store(*imapserver.FetchWriter, imap.NumSet, *imap.StoreFlags, *imap.StoreOptions) error {
	return s.reject("STORE")
}

func (s *recordingSession) Copy(imap.NumSet, string) (*imap.CopyData, error) {
	return nil, s.reject("COPY")
}

func (s *recordingSession) Move(*imapserver.MoveWriter, imap.NumSet, string) error {
	return s.reject("MOVE")
}

func (s *recordingSession) Expunge(*imapserver.ExpungeWriter, *imap.UIDSet) error {
	return s.reject("EXPUNGE")
}

func (s *recordingSession) Append(string, imap.LiteralReader, *imap.AppendOptions) (*imap.AppendData, error) {
	return nil, s.reject("APPEND")
}

func (s *recordingSession) Create(string, *imap.CreateOptions) error {
	return s.reject("CREATE")
}

func (s *recordingSession) Delete(string) error {
	return s.reject("DELETE")
}

func (s *recordingSession) Rename(string, string, *imap.RenameOptions) error {
	return s.reject("RENAME")
}

func (s *recordingSession) Subscribe(string) error {
	return s.reject("SUBSCRIBE")
}

func (s *recordingSession) Unsubscribe(string) error {
	return s.reject("UNSUBSCRIBE")
}

func (s *recordingSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	s.rec.mu.Lock()
	s.rec.selectsRO = append(s.rec.selectsRO, options != nil && options.ReadOnly)
	s.rec.mu.Unlock()
	return s.Session.Select(mailbox, options)
}

func (s *recordingSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	s.rec.mu.Lock()
	s.rec.fetches++
	if options != nil {
		for _, bs := range options.BodySection {
			if !bs.Peek {
				s.rec.nonPeek++
			}
		}
	}
	s.rec.mu.Unlock()
	return s.Session.Fetch(w, numSet, options)
}

// quietLogger silences the server's connection-level logging in tests.
type quietLogger struct{}

func (quietLogger) Printf(string, ...any) {}

// bridgeFixture is an in-memory IMAP server that stands in for Proton Bridge.
// It uses a self-signed certificate on 127.0.0.1.
type bridgeFixture struct {
	t           *testing.T
	host        string
	port        int
	mode        string
	fingerprint string
	user        *imapmemserver.User
	rec         *writeRecorder
}

// newBridgeFixture starts a server speaking tlsMode (starttls or ssl) with an
// empty INBOX.
func newBridgeFixture(t *testing.T, tlsMode string) *bridgeFixture {
	t.Helper()
	cert, fp := selfSignedCert(t)
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUsername, testPassword)
	mem.AddUser(user)
	rec := &writeRecorder{}

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &recordingSession{Session: mem.NewSession(), rec: rec}, nil, nil
		},
		TLSConfig: cfg,
		Logger:    quietLogger{},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var serve net.Listener = ln
	if tlsMode == config.TLSModeSSL {
		serve = tls.NewListener(ln, cfg)
	}
	go func() { _ = srv.Serve(serve) }()
	t.Cleanup(func() { _ = srv.Close() })

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	fx := &bridgeFixture{t: t, host: host, port: port, mode: tlsMode, fingerprint: fp, user: user, rec: rec}
	fx.ensureMailbox("INBOX")
	return fx
}

// account returns a loopback account for this fixture with the given pin.
func (fx *bridgeFixture) account(pin string) config.BridgeAccount {
	return config.BridgeAccount{
		ID:              "acc1",
		Host:            fx.host,
		IMAPPort:        fx.port,
		SMTPPort:        1025,
		TLSMode:         fx.mode,
		Username:        testUsername,
		CertFingerprint: pin,
	}
}

// connect returns a connected client, or the Connect error. The client is
// closed at the end of the test.
func (fx *bridgeFixture) connect(pin string) (Client, error) {
	fx.t.Helper()
	c := NewClient()
	fx.t.Cleanup(func() { _ = c.Close() })
	if err := c.Connect(context.Background(), fx.account(pin), testPassword); err != nil {
		return c, err
	}
	return c, nil
}

// mustConnect pins the fixture's certificate and connects.
func (fx *bridgeFixture) mustConnect() Client {
	fx.t.Helper()
	c, err := fx.connect(fx.fingerprint)
	if err != nil {
		fx.t.Fatalf("connect: %v", err)
	}
	return c
}

// ensureMailbox creates a mailbox if it does not exist.
func (fx *bridgeFixture) ensureMailbox(name string) {
	fx.t.Helper()
	if err := fx.user.Create(name, nil); err != nil {
		var imapErr *imap.Error
		if errors.As(err, &imapErr) && imapErr.Code == imap.ResponseCodeAlreadyExists {
			return
		}
		fx.t.Fatalf("create %s: %v", name, err)
	}
}

// appendMessage stores raw in mailbox, optionally with flags.
func (fx *bridgeFixture) appendMessage(mailbox string, raw []byte, flags ...imap.Flag) {
	fx.t.Helper()
	opts := &imap.AppendOptions{Flags: flags}
	if _, err := fx.user.Append(mailbox, bytes.NewReader(raw), opts); err != nil {
		fx.t.Fatalf("append to %s: %v", mailbox, err)
	}
}

// selfSignedCert returns a self-signed ECDSA certificate for 127.0.0.1 and the
// fingerprint a client should pin.
func selfSignedCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "Proton Bridge test", Organization: []string{"temail tests"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, fingerprintOf(der)
}
