package sync

import (
	"context"
	"errors"
	"reflect"
	gosync "sync"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
)

// E: a newest message that was appended and then expunged leaves UIDNEXT above
// the highest cached UID. The mailbox is resynced once, and then it is quiet.
// Comparing UIDNEXT with the highest cached UID would resync it on every check.
func TestAppendAndExpungeTopUIDResyncsOnce(t *testing.T) {
	idler := newFakeIdler()
	wf := newWatchFixture(t, idler, time.Hour)
	ctx := context.Background()

	uid := wf.bridge.appendMsg("INBOX")
	wf.bridge.expunge("INBOX", uid) // the newest UID is gone; UIDNEXT moved on

	first, err := wf.syn.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Mailboxes, []string{"INBOX"}) {
		t.Fatalf("first check = %+v, want INBOX changed once", first)
	}
	if _, err := wf.syn.RunChanged(ctx, first.Mailboxes); err != nil {
		t.Fatal(err)
	}
	second, err := wf.syn.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Mailboxes) != 0 || second.Full {
		t.Fatalf("second check = %+v, want nothing: the mailbox must not resync forever", second)
	}
}

// E: the stored UIDNEXT is what the sync recorded, not the highest UID.
func TestSyncRecordsUIDNext(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("INBOX")
	fx.mustRun()
	states, err := fx.st.GetSyncState(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range states {
		if st.Name == "INBOX" && st.UIDNext != 3 {
			t.Fatalf("INBOX uidnext = %d, want 3 (the server's UIDNEXT)", st.UIDNext)
		}
	}
}

// F: a read in All Mail changes its unseen count. That must not trigger an All
// Mail resync on the reconcile, because that resync is a full SELECT.
func TestAllMailUnseenDoesNotTriggerCheck(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	uid := wf.bridge.appendMsg("All Mail")
	wf.fixture.mustRun() // All Mail now has one unseen message in the cache

	wf.bridge.setFlags("All Mail", uid, `\Seen`)
	ch, err := wf.syn.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range ch.Mailboxes {
		if name == "All Mail" {
			t.Fatalf("All Mail unseen change triggered a resync: %+v", ch)
		}
	}
}

// F: a message count change in All Mail still triggers its sync. Only the unseen
// comparison is skipped for it.
func TestAllMailCountChangeStillTriggers(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	wf.bridge.appendMsg("All Mail")
	ch, err := wf.syn.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ch.Mailboxes, []string{"All Mail"}) {
		t.Fatalf("changes = %+v, want All Mail (new mail)", ch)
	}
}

// F: the flag sweep syncs All Mail at its interval, and the names are reported.
func TestFlagSweepSyncsAllMail(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	wf.w.opts.FlagSweepInterval = time.Nanosecond // every check is a sweep
	wf.w.opts.Debounce = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	wf.w.Kick() // a check with no real change: only the sweep has work
	select {
	case names := <-wf.seen:
		if !reflect.DeepEqual(names, []string{"All Mail"}) {
			t.Fatalf("sweep synced %v, want [All Mail]", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the flag sweep did not run")
	}
	cancel()
	<-done
}

// Kick runs a check without any IDLE event, so the service can ask for a sync
// while the watcher is running.
func TestKickRunsCheck(t *testing.T) {
	wf := newWatchFixture(t, newFakeIdler(), time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := wf.runWatch(ctx)

	wf.bridge.appendMsg("INBOX")
	wf.w.Kick()
	select {
	case names := <-wf.seen:
		if !reflect.DeepEqual(names, []string{"INBOX"}) {
			t.Fatalf("kick synced %v, want [INBOX]", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Kick did not trigger a check")
	}
	cancel()
	<-done
}

// H: one strike for every path. The sync's login is refused. The watcher's IDLE
// connection does not log in at all, because the same latch refuses it.
func TestTwoPathsOneStrike(t *testing.T) {
	latch := NewAuthLatch()
	fx := newFixture(t, func(o *Options) {
		o.Auth = latch
		o.CredsKey = "creds-A"
	})
	fx.bridge.connectFails = []error{mail.ErrAuthFailed}
	if _, err := fx.run(); !errors.Is(err, mail.ErrAuthFailed) {
		t.Fatalf("sync run = %v, want ErrAuthFailed", err)
	}
	if !latch.Tripped() {
		t.Fatal("the refused login did not trip the latch")
	}
	connectsAfterSync := fx.bridge.connects

	idler := newFakeIdler()
	w := NewWatcher(fx.syn, idler, func(ctx context.Context) error {
		return idler.Connect(ctx, config.BridgeAccount{}, "")
	}, WatchOptions{Interval: time.Hour, PollInterval: time.Hour, Debounce: time.Millisecond, Backoff: time.Millisecond})
	if err := w.Run(context.Background()); !errors.Is(err, mail.ErrAuthFailed) {
		t.Fatalf("watcher Run = %v, want ErrAuthFailed", err)
	}
	if idler.connects.Load() != 0 {
		t.Fatalf("the IDLE connection tried to log in %d times after a refused login", idler.connects.Load())
	}
	if fx.bridge.connects != connectsAfterSync {
		t.Fatalf("the poll path logged in again (%d -> %d)", connectsAfterSync, fx.bridge.connects)
	}
}

// H: a latched login is not attempted again with the same credentials, and a
// new key (changed account or password) clears the latch.
func TestAuthLatchUntilCredentialsChange(t *testing.T) {
	latch := NewAuthLatch()
	latch.Trip("creds-A", mail.ErrAuthFailed)

	if err := latch.Check("creds-A"); !errors.Is(err, mail.ErrAuthFailed) {
		t.Fatalf("same creds: %v, want the latched error", err)
	}
	if err := latch.Check("creds-B"); err != nil {
		t.Fatalf("new creds: %v, want nil (latch cleared)", err)
	}
	if latch.Tripped() {
		t.Fatal("the latch is still set after a credentials change")
	}
}

// H: the latch ignores errors that are not authentication failures, so transport
// problems are still retried.
func TestLatchIgnoresTransportErrors(t *testing.T) {
	latch := NewAuthLatch()
	latch.Trip("creds-A", errors.New("connection reset"))
	if latch.Tripped() {
		t.Fatal("a transport error was latched")
	}
	var nilLatch *AuthLatch
	nilLatch.Trip("x", mail.ErrAuthFailed)
	if nilLatch.Check("x") != nil || nilLatch.Tripped() {
		t.Fatal("a nil latch must never latch")
	}
}

// H: Run leaves the connection open, so the watcher reuses it. The test counts
// logins: the sync logs in once, and the watcher's checks reuse that connection.
func TestRunLeavesConnectionForWatcher(t *testing.T) {
	fx := newFixture(t, nil)
	fx.mustRun()
	if fx.bridge.connects != 1 {
		t.Fatalf("logins after the run = %d, want 1", fx.bridge.connects)
	}
	if _, err := fx.syn.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fx.bridge.connects != 1 {
		t.Fatalf("a check logged in again (%d logins); the connection should be reused", fx.bridge.connects)
	}
	if err := fx.syn.Close(); err != nil {
		t.Fatal(err)
	}
}

// scriptIdler runs one scripted IDLE session per call. Each script returns when
// the session ends, after the started callback has run.
type scriptIdler struct {
	mu       gosync.Mutex
	scripts  []func(ctx context.Context) error
	sessions int
}

func (s *scriptIdler) Connect(context.Context, config.BridgeAccount, string) error { return nil }
func (s *scriptIdler) Capabilities() []string                                      { return []string{"IDLE"} }
func (s *scriptIdler) Close() error                                                { return nil }

func (s *scriptIdler) Idle(ctx context.Context, _ string, started func(), _ func()) error {
	s.mu.Lock()
	i := s.sessions
	s.sessions++
	s.mu.Unlock()
	if started != nil {
		started()
	}
	if i < len(s.scripts) {
		return s.scripts[i](ctx)
	}
	<-ctx.Done()
	return ctx.Err()
}

// I: a long IDLE session resets the backoff, so the next retry is short. A
// session that drops at once keeps the backoff growing.
func TestLongIdleSessionResetsBackoff(t *testing.T) {
	fx := newFixture(t, nil)
	fx.mustRun()

	var mu gosync.Mutex
	var sleeps []time.Duration
	dropped := errors.New("connection reset")
	idler := &scriptIdler{scripts: []func(context.Context) error{
		func(context.Context) error { return dropped }, // short: backoff grows
		func(ctx context.Context) error { // long: resets the backoff
			time.Sleep(30 * time.Millisecond)
			return dropped
		},
		func(ctx context.Context) error { return dropped },
	}}
	w := NewWatcher(fx.syn, idler, func(context.Context) error { return nil }, WatchOptions{
		Interval:        time.Hour,
		PollInterval:    time.Hour,
		Debounce:        time.Millisecond,
		Backoff:         10 * time.Millisecond,
		IdleStableAfter: 20 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		if d != w.opts.Debounce {
			sleeps = append(sleeps, d)
		}
		n := len(sleeps)
		mu.Unlock()
		if n >= 3 {
			cancel() // ends the watcher: both loops stop
			return context.Canceled
		}
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) < 2 {
		t.Fatalf("sleeps = %v", sleeps)
	}
	if sleeps[0] != 10*time.Millisecond {
		t.Fatalf("first retry = %v, want the base backoff", sleeps[0])
	}
	if sleeps[1] != 10*time.Millisecond {
		t.Fatalf("retry after a long session = %v, want the base backoff again (not %v)", sleeps[1], 2*sleeps[0])
	}
}

// I: IdleActive is false until IDLE has started, and true right after it.
func TestIdleReportedOnlyAfterStart(t *testing.T) {
	fx := newFixture(t, nil)
	fx.mustRun()
	probe := &startProbe{done: make(chan struct{})}
	w := NewWatcher(fx.syn, probe, func(context.Context) error { return nil }, WatchOptions{Interval: time.Hour})
	probe.w = w
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	select {
	case <-probe.done:
	case <-time.After(5 * time.Second):
		t.Fatal("IDLE never started")
	}
	if probe.beforeStart {
		t.Fatal("IdleActive was true before IDLE started")
	}
	if !probe.afterStart {
		t.Fatal("IdleActive was false after IDLE started")
	}
	cancel()
}

// startProbe is an IDLE connection that records IdleActive around the start.
type startProbe struct {
	w           *Watcher
	done        chan struct{}
	beforeStart bool
	afterStart  bool
}

func (p *startProbe) Connect(context.Context, config.BridgeAccount, string) error { return nil }
func (p *startProbe) Capabilities() []string                                      { return []string{"IDLE"} }
func (p *startProbe) Close() error                                                { return nil }

func (p *startProbe) Idle(ctx context.Context, _ string, started func(), _ func()) error {
	p.beforeStart = p.w.IdleActive()
	started()
	p.afterStart = p.w.IdleActive()
	close(p.done)
	<-ctx.Done()
	return ctx.Err()
}
