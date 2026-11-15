package services

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// syncFixture wires a SyncService to a real SQLite cache (in a temp dir) and
// the scriptable fake client.
type syncFixture struct {
	svc    *SyncService
	client *fakeClient
	f      *clientFactory
	ev     *fakeEmitter
	cfg    *fakeConfig
	st     store.Store
}

func newSyncFixture(t *testing.T, cfg config.Config) *syncFixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	client := &fakeClient{
		boxes: []model.Mailbox{{Name: "INBOX", DisplayName: "INBOX", Kind: model.KindInbox, UIDValidity: 7}},
		listing: mail.UIDListing{UIDValidity: 7, Messages: []mail.UIDState{
			{UID: 1}, {UID: 2}, {UID: 3},
		}},
		headers: []model.MessageHeader{
			{Mailbox: "INBOX", UIDValidity: 7, UID: 1, Subject: "one", Date: t0},
			{Mailbox: "INBOX", UIDValidity: 7, UID: 2, Subject: "two", Date: t0.Add(time.Minute)},
			{Mailbox: "INBOX", UIDValidity: 7, UID: 3, Subject: "three", Date: t0.Add(2 * time.Minute)},
		},
		message: model.Message{MessageHeader: model.MessageHeader{UIDValidity: 7}, TextBody: "body"},
	}
	f := &clientFactory{c: client}
	sec := newFakeSecrets()
	sec.pw["main"] = testPassword
	cfgStore := newFakeConfig(cfg)
	ev := &fakeEmitter{}
	svc := NewSyncService(st, cfgStore, sec, f.new, ev)
	svc.bodyDelay = time.Millisecond
	svc.backoff = time.Millisecond
	svc.stopWait = 5 * time.Second
	return &syncFixture{svc: svc, client: client, f: f, ev: ev, cfg: cfgStore, st: st}
}

// waitUntil polls cond until it holds or the test fails.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (fx *syncFixture) waitIdle(t *testing.T) {
	t.Helper()
	waitUntil(t, "sync to finish", func() bool { return !fx.svc.Status().Running })
}

// assertNoOutcomeEvent fails if sync:done or sync:error was emitted. Progress
// events are allowed.
func assertNoOutcomeEvent(t *testing.T, ev *fakeEmitter) {
	t.Helper()
	for _, n := range ev.names() {
		if n == EventSyncDone || n == EventSyncError {
			t.Fatalf("a stopped run must not emit %s; events = %v", n, ev.names())
		}
	}
}

func serviceOptions() application.ServiceOptions { return application.ServiceOptions{} }

func TestStartRefusesWhenNotReady(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr error
	}{
		{"no account", config.Default(), bridge.ErrNotConfigured},
		{"not pinned", func() config.Config {
			c := readyConfig()
			c.Bridge[0].CertFingerprint = ""
			return c
		}(), bridge.ErrNotPinned},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newSyncFixture(t, tt.cfg)
			if err := fx.svc.Start(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if fx.f.count() != 0 {
				t.Fatal("no client may be created when sync is not ready")
			}
			if fx.svc.Status().Running {
				t.Fatal("must not be running")
			}
		})
	}

	t.Run("no password", func(t *testing.T) {
		fx := newSyncFixture(t, readyConfig())
		fx.svc.sec.(*fakeSecrets).pw = map[string]string{}
		if err := fx.svc.Start(); !errors.Is(err, bridge.ErrNoPassword) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRunEmitsProgressThenDone(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	fx.waitIdle(t)

	names := fx.ev.names()
	if len(names) == 0 || names[len(names)-1] != EventSyncDone {
		t.Fatalf("events = %v, want progress then %s; lastError=%q", names, EventSyncDone, fx.svc.Status().LastError)
	}
	for _, n := range names[:len(names)-1] {
		if n != EventSyncProgress {
			t.Fatalf("unexpected event %q in %v", n, names)
		}
	}

	st := fx.svc.Status()
	if st.LastResult == nil || st.LastResult.HeadersFetched != 3 || st.LastResult.Mailboxes != 1 {
		t.Fatalf("unexpected result %+v", st.LastResult)
	}
	if st.LastError != "" || st.Running {
		t.Fatalf("status = %+v", st)
	}

	// The cache holds the synced messages, and the payload is the DTO.
	headers, err := fx.st.ListMessages(context.Background(), "main", "INBOX", store.ListOptions{Limit: 10})
	if err != nil || len(headers) != 3 {
		t.Fatalf("cached %d headers, err %v", len(headers), err)
	}
	fx.ev.mu.Lock()
	last := fx.ev.events[len(fx.ev.events)-1].data
	fx.ev.mu.Unlock()
	if _, ok := last.(SyncResult); !ok {
		t.Fatalf("done payload type %T", last)
	}
}

func TestSingleRunAtATime(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})

	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the run to start", func() bool { return fx.f.count() == 1 })
	if err := fx.svc.Start(); err != nil {
		t.Fatalf("second Start while running: %v", err)
	}
	if !fx.svc.Status().Running {
		t.Fatal("expected a running sync")
	}
	close(fx.client.gate)
	fx.waitIdle(t)
	if got := fx.f.count(); got != 1 {
		t.Fatalf("clients created = %d, want 1 (second Start must not start a run)", got)
	}
}

func TestStopCancelsWithoutEvent(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{}) // Connect blocks until cancelled

	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st := fx.svc.Status()
	if st.Running {
		t.Fatal("still running after Stop")
	}
	if st.LastError != "sync stopped" {
		t.Fatalf("LastError = %q", st.LastError)
	}
	assertNoOutcomeEvent(t, fx.ev)
	if err := fx.svc.Stop(); err != nil {
		t.Fatalf("second Stop should be a no-op: %v", err)
	}
}

func TestShutdownStopsRun(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if fx.svc.Status().Running {
		t.Fatal("run still active after shutdown")
	}
}

func TestParentContextCancellationStopsRun(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fx.svc.ServiceStartup(ctx, serviceOptions()); err != nil {
		t.Fatal(err)
	}
	if !fx.svc.Status().Running {
		t.Fatal("ServiceStartup should autostart a ready account")
	}
	cancel() // app shutdown cancels the startup context
	fx.waitIdle(t)
	assertNoOutcomeEvent(t, fx.ev)
}

func TestServiceStartupAutostartsOnlyWhenReady(t *testing.T) {
	fx := newSyncFixture(t, config.Default())
	if err := fx.svc.ServiceStartup(context.Background(), serviceOptions()); err != nil {
		t.Fatalf("not-ready autostart must not fail startup: %v", err)
	}
	if fx.svc.Status().Running || fx.f.count() != 0 {
		t.Fatal("must not start without an account")
	}
}

func TestRunErrorEmitsSyncError(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.connectErr = mail.ErrAuthFailed
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	fx.waitIdle(t)

	names := fx.ev.names()
	if len(names) == 0 || names[len(names)-1] != EventSyncError {
		t.Fatalf("events = %v, want a final %s", names, EventSyncError)
	}
	st := fx.svc.Status()
	if st.LastError == "" || st.LastResult == nil {
		t.Fatalf("status = %+v", st)
	}
}

func TestPartialRunEmitsDoneWithMailboxErrors(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.listErr = errors.New("server NO: mailbox busy")
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	fx.waitIdle(t)

	names := fx.ev.names()
	if names[len(names)-1] != EventSyncDone {
		t.Fatalf("events = %v, want %s (a partial run is done with errors)", names, EventSyncDone)
	}
	res := fx.svc.Status().LastResult
	if res == nil || len(res.MailboxErrors) != 1 || res.MailboxErrors[0].Mailbox != "INBOX" {
		t.Fatalf("result = %+v", res)
	}
	if fx.svc.Status().LastError != "" {
		t.Fatal("partial run is not a run error")
	}
}

func TestRestartReplacesRunWithCurrentConfig(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	fx.svc.restart()
	close(fx.client.gate)
	waitUntil(t, "the restarted run to finish", func() bool { return !fx.svc.Status().Running && fx.f.count() == 2 })
	names := fx.ev.names()
	if len(names) == 0 || names[len(names)-1] != EventSyncDone {
		t.Fatalf("events after restart = %v; lastError=%q", names, fx.svc.Status().LastError)
	}
}

func TestRestartWithoutReadyAccountStops(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.gate = make(chan struct{})
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	fx.cfg.mu.Lock()
	fx.cfg.cfg = config.Default()
	fx.cfg.mu.Unlock()
	fx.svc.restart()
	if fx.svc.Status().Running {
		t.Fatal("restart with no account must leave sync stopped")
	}
}
