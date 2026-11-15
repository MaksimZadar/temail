package sync

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
)

// fakeIdler is the IDLE connection. kick delivers an unsolicited update. active
// counts running Idle calls, so a test can see that none is left behind.
type fakeIdler struct {
	caps       []string
	kick       chan struct{}
	active     atomic.Int32
	connects   atomic.Int32
	closes     atomic.Int32
	connectErr []error // consumed by Connect
	dropErr    chan error
	mailbox    atomic.Value
}

func newFakeIdler() *fakeIdler {
	return &fakeIdler{caps: []string{"IDLE", "IMAP4REV1"}, kick: make(chan struct{}, 8), dropErr: make(chan error, 4)}
}

func (f *fakeIdler) Connect(ctx context.Context, _ config.BridgeAccount, _ string) error {
	f.connects.Add(1)
	if len(f.connectErr) > 0 {
		err := f.connectErr[0]
		f.connectErr = f.connectErr[1:]
		return err
	}
	return ctx.Err()
}

func (f *fakeIdler) Capabilities() []string { return f.caps }

func (f *fakeIdler) Idle(ctx context.Context, mailbox string, started func(), onChange func()) error {
	f.active.Add(1)
	defer f.active.Add(-1)
	f.mailbox.Store(mailbox)
	if started != nil {
		started()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-f.dropErr:
			return err
		case <-f.kick:
			onChange()
		}
	}
}

func (f *fakeIdler) Close() error {
	f.closes.Add(1)
	return nil
}

// watchFixture is a Syncer on the fake Bridge, with a watcher built on fakeIdler.
type watchFixture struct {
	*fixture
	idler *fakeIdler
	w     *Watcher
	seen  chan []string
}

func newWatchFixture(t *testing.T, idler *fakeIdler, interval time.Duration) *watchFixture {
	t.Helper()
	fx := newFixture(t, nil)
	fx.bridge.addBox("All Mail", model.KindFolder, 1)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("INBOX")
	fx.mustRun()

	seen := make(chan []string, 16)
	connectIdle := func(ctx context.Context) error {
		return idler.Connect(ctx, config.BridgeAccount{}, "")
	}
	w := NewWatcher(fx.syn, idler, connectIdle, WatchOptions{
		Interval:     interval,
		PollInterval: interval,
		Debounce:     time.Millisecond,
		Backoff:      time.Millisecond,
		OnChanged:    func(names []string) { seen <- names },
	})
	return &watchFixture{fixture: fx, idler: idler, w: w, seen: seen}
}

// runWatch starts the watcher and returns the channel its Run result arrives on.
func (wf *watchFixture) runWatch(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- wf.w.Run(ctx) }()
	return done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func cachedInbox(t *testing.T, fx *fixture) int {
	t.Helper()
	st, err := fx.st.CacheStats(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	return int(st.Messages)
}

// An IDLE update triggers an incremental sync: only the new message is fetched.
func TestIdleEventTriggersIncrementalSync(t *testing.T) {
	idler := newFakeIdler()
	wf := newWatchFixture(t, idler, time.Hour) // only IDLE can trigger a check
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	waitFor(t, "IDLE to start", func() bool { return idler.active.Load() == 1 })
	if got := idler.mailbox.Load(); got != "INBOX" {
		t.Fatalf("idle on %v, want INBOX (All Mail is covered by the reconcile)", got)
	}
	before := wf.bridge.headerFetches("INBOX", 1) + wf.bridge.headerFetches("INBOX", 2)
	wf.bridge.appendMsg("INBOX")
	idler.kick <- struct{}{}

	select {
	case names := <-wf.seen:
		if len(names) != 1 || names[0] != "INBOX" {
			t.Fatalf("changed = %v, want [INBOX]", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no change notification after the IDLE update")
	}
	if got := cachedInbox(t, wf.fixture); got != 3 {
		t.Fatalf("cached INBOX messages = %d, want 3", got)
	}
	// Messages 1 and 2 were cached: they are not fetched again. Message 3 is new,
	// so it is fetched once.
	if got := wf.bridge.headerFetches("INBOX", 1) + wf.bridge.headerFetches("INBOX", 2); got != before {
		t.Fatalf("cached messages were fetched again (%d -> %d)", before, got)
	}
	if got := wf.bridge.headerFetches("INBOX", 3); got != 1 {
		t.Fatalf("new message fetched %d times, want 1", got)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
}

// The STATUS comparison: each kind of change is found, and an unchanged mailbox
// costs no SELECT and no UID listing.
func TestStatusDiffCases(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(fx *watchFixture)
		want   bool
	}{
		{"nothing changed", func(*watchFixture) {}, false},
		{"new mail (UIDNEXT up)", func(fx *watchFixture) { fx.bridge.appendMsg("INBOX") }, true},
		{"a message removed (MESSAGES down)", func(fx *watchFixture) { fx.bridge.expunge("INBOX", 1) }, true},
		{"unseen count changed", func(fx *watchFixture) { fx.bridge.setFlags("INBOX", 1, `\Seen`) }, true},
		{"mailbox recreated (UIDVALIDITY up)", func(fx *watchFixture) { fx.bridge.recreate("INBOX") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := newWatchFixture(t, newFakeIdler(), time.Hour)
			ctx := context.Background()
			listBefore := wf.bridge.listCount()
			tt.mutate(wf)
			ch, err := wf.syn.Check(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got := len(ch.Mailboxes) > 0
			if got != tt.want || ch.Full {
				t.Fatalf("changes = %+v, want changed=%v", ch, tt.want)
			}
			if !tt.want && wf.bridge.listCount() != listBefore {
				t.Fatal("an unchanged check issued a UID listing")
			}
		})
	}
}

// A mailbox that appears or disappears on the server needs a full run.
func TestCheckFullOnMailboxListChange(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	wf.bridge.addBox("Labels/New", model.KindLabel, 1)
	ch, err := wf.syn.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Full {
		t.Fatalf("changes = %+v, want Full", ch)
	}
}

// Check never SELECTs All Mail: it issues LIST and STATUS only.
func TestCheckNeverSelectsAllMail(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	wf.bridge.appendMsg("All Mail")
	before := wf.bridge.listCount() // the setup sync listed both mailboxes
	if _, err := wf.syn.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := wf.bridge.listCount() - before; got != 0 {
		t.Fatalf("Check issued %d UID listings; it must not SELECT", got)
	}
}

// Without IDLE the watcher polls with STATUS, and the new mail is still synced.
func TestIdleLessFallbackPolls(t *testing.T) {
	idler := newFakeIdler()
	idler.caps = []string{"IMAP4REV1"}
	wf := newWatchFixture(t, idler, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	wf.bridge.appendMsg("INBOX")
	select {
	case names := <-wf.seen:
		if len(names) != 1 || names[0] != "INBOX" {
			t.Fatalf("changed = %v", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("polling did not find the new mail")
	}
	if wf.w.IdleActive() {
		t.Fatal("IDLE reported active without the capability")
	}
	if idler.active.Load() != 0 {
		t.Fatal("an Idle call ran without the capability")
	}
	cancel()
	<-done
}

// An auth failure stops the watcher with the error and does not loop on login.
func TestAuthFailureStopsWatcher(t *testing.T) {
	idler := newFakeIdler()
	idler.connectErr = []error{mail.ErrAuthFailed, mail.ErrAuthFailed, mail.ErrAuthFailed}
	wf := newWatchFixture(t, idler, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	select {
	case err := <-done:
		if !errors.Is(err, mail.ErrAuthFailed) {
			t.Fatalf("Run = %v, want ErrAuthFailed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher kept running after an auth failure")
	}
	if n := idler.connects.Load(); n != 1 {
		t.Fatalf("IDLE connect attempts = %d, want 1 (no retry on auth failure)", n)
	}
}

// Shutdown: Run returns only after the IDLE goroutine has ended and closed its
// connection, so nothing is left running.
func TestShutdownLeavesNoIdleGoroutine(t *testing.T) {
	idler := newFakeIdler()
	wf := newWatchFixture(t, idler, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	done := wf.runWatch(ctx)
	waitFor(t, "IDLE to start", func() bool { return idler.active.Load() == 1 })

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if idler.active.Load() != 0 {
		t.Fatal("Idle still running after Run returned")
	}
	if idler.closes.Load() == 0 {
		t.Fatal("the IDLE connection was not closed")
	}
	if wf.w.IdleActive() {
		t.Fatal("IdleActive still true after shutdown")
	}
}

// A dropped IDLE connection reconnects with backoff and keeps watching.
func TestDroppedIdleReconnects(t *testing.T) {
	idler := newFakeIdler()
	wf := newWatchFixture(t, idler, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	waitFor(t, "first IDLE", func() bool { return idler.active.Load() == 1 })
	idler.dropErr <- errors.New("connection reset")
	waitFor(t, "IDLE to restart", func() bool {
		return idler.connects.Load() >= 2 && idler.active.Load() == 1
	})

	wf.bridge.appendMsg("INBOX")
	idler.kick <- struct{}{}
	select {
	case <-wf.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("no sync after the IDLE reconnect")
	}
	cancel()
	<-done
}

// A failed check (the connection drops) is retried, and the next check syncs.
func TestTransportFailureRetriesCheck(t *testing.T) {
	idler := newFakeIdler()
	idler.caps = []string{"IMAP4REV1"}
	wf := newWatchFixture(t, idler, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wf.bridge.appendMsg("INBOX")
	wf.bridge.mu.Lock()
	wf.bridge.opFails = []error{transportErr(), transportErr(), transportErr(), transportErr(), transportErr(), transportErr()}
	wf.bridge.mu.Unlock()
	done := wf.runWatch(ctx)

	select {
	case names := <-wf.seen:
		if len(names) != 1 || names[0] != "INBOX" {
			t.Fatalf("changed = %v", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not recover from transport failures")
	}
	cancel()
	<-done
}

// A burst of IDLE updates becomes one check.
func TestBurstOfUpdatesIsOneCheck(t *testing.T) {
	idler := newFakeIdler()
	wf := newWatchFixture(t, idler, time.Hour)
	wf.w.opts.Debounce = 60 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)
	waitFor(t, "IDLE to start", func() bool { return idler.active.Load() == 1 })

	before := wf.bridge.statusCount()
	for i := 0; i < 5; i++ {
		idler.kick <- struct{}{}
	}
	waitFor(t, "the burst to be checked", func() bool { return wf.bridge.statusCount() > before })
	time.Sleep(150 * time.Millisecond)
	if got := wf.bridge.statusCount() - before; got > 2 {
		t.Fatalf("status calls for a burst of 5 updates = %d, want one check (one STATUS per mailbox)", got)
	}
	cancel()
	<-done
}
