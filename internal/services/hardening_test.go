package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/store"
)

// closeRecorder wraps a store and records Close.
type closeRecorder struct {
	store.Store
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return c.Store.Close()
}

// Item 2 and 5: a blocked keychain must not block startup, and a launch that
// was overtaken by a Stop must not start a run.
func TestServiceStartupDoesNotWaitForKeychain(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	sec := fx.svc.sec.(*fakeSecrets)
	sec.getGate = make(chan struct{})
	fx.svc.autostartWait = time.Minute

	returned := make(chan struct{})
	go func() {
		_ = fx.svc.ServiceStartup(context.Background(), serviceOptions())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("ServiceStartup blocked on the keychain")
	}

	close(sec.getGate) // keychain answers later; the autostart then runs
	waitUntil(t, "autostart after the keychain answered", func() bool { return fx.svc.Status().Running })
	_ = fx.svc.Stop()
}

func TestAutostartTimeoutDropsLateLaunch(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	sec := fx.svc.sec.(*fakeSecrets)
	sec.getGate = make(chan struct{})
	fx.svc.autostartWait = 30 * time.Millisecond

	if err := fx.svc.ServiceStartup(context.Background(), serviceOptions()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // autostart gives up while the keychain is blocked
	close(sec.getGate)
	time.Sleep(150 * time.Millisecond) // the late launch must find its generation stale

	if fx.svc.Status().Running || fx.f.count() != 0 {
		t.Fatalf("late autostart started a run: running=%v clients=%d", fx.svc.Status().Running, fx.f.count())
	}
}

func TestStopDuringCredentialReadDropsLaunch(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	sec := fx.svc.sec.(*fakeSecrets)
	sec.getGate = make(chan struct{})
	sec.getEntered = make(chan struct{}, 1)

	startErr := make(chan error, 1)
	go func() { startErr <- fx.svc.Start() }()
	<-sec.getEntered // Start has read the generation and is inside the keychain read
	if err := fx.svc.Stop(); err != nil {
		t.Fatal(err)
	}
	close(sec.getGate)
	if err := <-startErr; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if fx.svc.Status().Running || fx.f.count() != 0 {
		t.Fatalf("launch overtaken by Stop still started: running=%v clients=%d", fx.svc.Status().Running, fx.f.count())
	}
}

// Item 5: a restart starts exactly one run, with credentials read after the
// restart.
func TestRestartStartsOneRunWithFreshCredentials(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "first run", func() bool { return fx.f.count() == 1 })

	fx.svc.sec.(*fakeSecrets).pw["main"] = "NEW-password"
	fx.svc.restart()
	close(fx.client.gate)

	waitUntil(t, "restarted run to finish", func() bool { return !fx.svc.Status().Running && fx.f.count() == 2 })
	fx.client.mu.Lock()
	last := fx.client.passwords[len(fx.client.passwords)-1]
	fx.client.mu.Unlock()
	if last != "NEW-password" {
		t.Fatalf("restarted run used a stale password")
	}
	if fx.f.count() != 2 {
		t.Fatalf("runs = %d, want exactly 2 (first, restarted)", fx.f.count())
	}
}

// Item 6: a stuck sync is reported in Status and keeps the cache open.
func TestStopTimeoutIsReportedAndKeepsCacheOpen(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{}) // never released until the test ends
	fx.client.stubborn = true            // and it ignores cancellation, like a hung socket
	fx.svc.stopWait = 50 * time.Millisecond
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "run to start", func() bool { return fx.f.count() == 1 })

	if err := fx.svc.Stop(); !errors.Is(err, ErrSyncStopTimeout) {
		t.Fatalf("Stop err = %v, want ErrSyncStopTimeout", err)
	}
	st := fx.svc.Status()
	if !st.StopTimedOut || !st.Running {
		t.Fatalf("status must show a stuck stop: %+v", st)
	}

	mailSvc := NewMailService(newFakeStore(), fx.cfg, fx.svc.sec, fx.f.new)
	rec := &closeRecorder{Store: fx.st}
	if err := Shutdown(fx.svc, mailSvc, rec); err == nil {
		t.Fatal("Shutdown must fail while the sync is stuck")
	}
	if rec.closed {
		t.Fatal("cache closed while a sync run may still write to it")
	}

	close(fx.client.gate)
	waitUntil(t, "stuck run to end", func() bool { return !fx.svc.Status().Running })
	if fx.svc.Status().StopTimedOut {
		t.Fatal("StopTimedOut must clear once the run has ended")
	}
	if err := Shutdown(fx.svc, mailSvc, rec); err != nil {
		t.Fatalf("Shutdown after the run ended: %v", err)
	}
	if !rec.closed {
		t.Fatal("cache not closed after a clean shutdown")
	}
}

// Item 6: shutdown waits for in-flight reads, refuses new ones, and a read
// that outlives the wait keeps the cache open.
func TestMailShutdownDrainsInFlightAndRefusesNewCalls(t *testing.T) {
	fx := newMailFixture(t)
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 8}
	fx.st.headers[key] = header(8)
	fx.client.fetchGate = make(chan struct{})
	fx.client.fetchEntered = make(chan struct{}, 1)
	fx.client.stubborn = true // a read that ignores cancellation
	fx.svc.drainWait = 30 * time.Millisecond

	res := make(chan error, 1)
	go func() {
		_, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 8)
		res <- err
	}()
	<-fx.client.fetchEntered

	if err := fx.svc.shutdown(); !errors.Is(err, ErrInFlightTimeout) {
		t.Fatalf("shutdown err = %v, want ErrInFlightTimeout", err)
	}
	if _, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 8); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after shutdown: %v, want ErrClosed", err)
	}
	close(fx.client.fetchGate) // the stuck read finishes; the cache must still be open for it
	<-res
}

// A read that finishes within the wait lets shutdown complete cleanly.
func TestMailShutdownCancelsCooperativeReads(t *testing.T) {
	fx := newMailFixture(t)
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 9}
	fx.st.headers[key] = header(9)
	fx.client.fetchGate = make(chan struct{})
	fx.client.fetchEntered = make(chan struct{}, 1)
	defer close(fx.client.fetchGate)

	res := make(chan error, 1)
	go func() {
		_, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 9)
		res <- err
	}()
	<-fx.client.fetchEntered
	if err := fx.svc.shutdown(); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if err := <-res; err == nil {
		t.Fatal("in-flight read should have been cancelled")
	}
}

// Item 7: bound calls wait for the session with their own context.
func TestMailCallHonoursContextWhileQueued(t *testing.T) {
	fx := newMailFixture(t)
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 5}
	fx.st.headers[key] = header(5)
	fx.client.fetchGate = make(chan struct{})
	fx.client.fetchEntered = make(chan struct{}, 1)
	defer close(fx.client.fetchGate)

	go func() { _, _ = fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 5) }()
	<-fx.client.fetchEntered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := fx.svc.GetMessage(ctx, "INBOX", testEpoch, 5)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued call err = %v, want its own deadline", err)
	}
}

// Item 2: a locked keychain yields a status, not a hang, and the result is cached.
func TestKeyringProbeTimeoutAndTTLCache(t *testing.T) {
	cfg := newFakeConfig(readyConfig())
	sec := newFakeSecrets()
	sec.probeGate = make(chan struct{})
	s := NewSetupService(cfg, sec, (&clientFactory{c: &fakeClient{}}).new, nil)
	s.dial = dialTo(true)
	s.keyringWait = 20 * time.Millisecond

	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.KeyringOK || !strings.Contains(st.KeyringMessage, "locked") {
		t.Fatalf("locked keychain must be reported, got %+v", st)
	}

	close(sec.probeGate)
	waitUntil(t, "probe result", func() bool {
		st, _ := s.GetStatus(context.Background())
		return st.KeyringOK
	})
	before := sec.probes()
	for i := 0; i < 3; i++ {
		if _, err := s.GetStatus(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if sec.probes() != before {
		t.Fatalf("probe ran again within the TTL (%d -> %d)", before, sec.probes())
	}
}

// Item 3: the config is re-read after the probe, and a changed endpoint refuses the pin.
func TestTrustCertRefusesEndpointChangedDuringProbe(t *testing.T) {
	cfg := newFakeConfig(readyConfig())
	cfg.cfg.Bridge[0].CertFingerprint = ""
	c := &fakeClient{connectErr: &mail.UntrustedCertError{Fingerprint: presentFP, Subject: "CN=Bridge"}}
	c.onConnect = func() {
		cfg.mu.Lock()
		cfg.cfg.Bridge[0].IMAPPort = 2143
		cfg.mu.Unlock()
	}
	s, _ := newSetup(t, cfg, newFakeSecrets(), c, true)
	if err := s.TrustCert(context.Background(), presentFP, false); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("err = %v, want ErrAccountChanged", err)
	}
	if cfg.saveCount() != 0 {
		t.Fatal("pin written although the endpoint changed")
	}
}

// Item 4: an existing, different pin needs replace=true.
func TestTrustCertAlreadyPinnedNeedsReplace(t *testing.T) {
	tests := []struct {
		name      string
		given     string
		presented string
		replace   bool
		wantErr   error
		wantPin   string
		wantProbe bool
	}{
		{"different pin without replace is refused before probing", presentFP, presentFP, false, ErrAlreadyPinned, pinnedFP, false},
		{"replace re-pins the fresh fingerprint", presentFP, presentFP, true, nil, presentFP, true},
		{"replace when the presented certificate changed is refused", presentFP, pinnedFP, true, ErrFingerprintChanged, pinnedFP, true},
		{"pinning the stored value again is a no-op", pinnedFP, pinnedFP, false, nil, pinnedFP, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newFakeConfig(readyConfig()) // pinned to pinnedFP
			c := &fakeClient{connectErr: &mail.UntrustedCertError{Fingerprint: tt.presented, Subject: "CN=Bridge"}}
			s, _ := newSetup(t, cfg, newFakeSecrets(), c, true)
			err := s.TrustCert(context.Background(), tt.given, tt.replace)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := cfg.current().Bridge[0].CertFingerprint; got != tt.wantPin {
				t.Fatalf("pin = %q, want %q", got, tt.wantPin)
			}
			if probed := c.connects > 0; probed != tt.wantProbe {
				t.Fatalf("probed = %v, want %v", probed, tt.wantProbe)
			}
		})
	}
}

// Item 8: an empty password keeps the stored one; without one it fails.
func TestSaveAccountKeepsStoredPassword(t *testing.T) {
	cfg := newFakeConfig(readyConfig())
	sec := newFakeSecrets()
	sec.pw["main"] = testPassword
	s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)

	if err := s.SaveAccount(context.Background(), "127.0.0.1", 1143, 1025, "starttls", "renamed@proton.test", ""); err != nil {
		t.Fatalf("SaveAccount with empty password: %v", err)
	}
	if sec.sets != 0 || sec.pw["main"] != testPassword {
		t.Fatal("empty password must not touch the keyring")
	}
	if cfg.current().Bridge[0].Username != "renamed@proton.test" {
		t.Fatal("other settings were not saved")
	}
}

func TestSaveAccountEmptyPasswordWithoutStoredFails(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"no account yet", config.Default()},
		{"account exists but keyring has no password", readyConfig()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newFakeConfig(tt.cfg)
			s, _ := newSetup(t, cfg, newFakeSecrets(), &fakeClient{}, true)
			err := s.SaveAccount(context.Background(), "127.0.0.1", 1143, 1025, "starttls", "me", "")
			if !errors.Is(err, bridge.ErrNoPassword) {
				t.Fatalf("err = %v, want no_password", err)
			}
			if cfg.saveCount() != 0 {
				t.Fatal("config written without a password")
			}
		})
	}
}

// Item 8: config first. A failed config write leaves the keyring alone.
func TestSaveAccountConfigFailureLeavesKeyringAlone(t *testing.T) {
	cfg := newFakeConfig(config.Default())
	cfg.saveE = errors.New("disk full")
	sec := newFakeSecrets()
	s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)
	if err := s.SaveAccount(context.Background(), "127.0.0.1", 1143, 1025, "starttls", "me", testPassword); err == nil {
		t.Fatal("want an error")
	}
	if sec.sets != 0 {
		t.Fatal("keyring written although the config write failed")
	}
}

// Item 8: a keyring failure restores the previous config values.
func TestSaveAccountKeyringFailureRestoresExistingConfig(t *testing.T) {
	cfg := newFakeConfig(readyConfig())
	sec := newFakeSecrets()
	sec.setErr = errors.New("keyring locked")
	s, _ := newSetup(t, cfg, sec, &fakeClient{}, true)
	if err := s.SaveAccount(context.Background(), "127.0.0.1", 1143, 1025, "starttls", "new@proton.test", testPassword); err == nil {
		t.Fatal("want an error")
	}
	if got := cfg.current().Bridge[0].Username; got != "me@proton.test" {
		t.Fatalf("username = %q, previous config not restored", got)
	}
}

// Item 1: attachment cap is 10 MiB.
func TestAttachmentCapIs10MiB(t *testing.T) {
	if MaxAttachmentBytes != 10<<20 {
		t.Fatalf("MaxAttachmentBytes = %d", MaxAttachmentBytes)
	}
}
