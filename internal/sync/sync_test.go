package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

const acct = "acc1"

// fixture wires a fake Bridge, a real SQLite cache in t.TempDir(), and a Syncer.
type fixture struct {
	t      *testing.T
	bridge *fakeBridge
	st     store.Store
	syn    *Syncer
	sleeps []time.Duration
	events []Progress
}

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fx := &fixture{t: t, bridge: newFakeBridge(), st: st}
	opts := Options{
		Account:       acct,
		BodyBatchSize: 3,
		BodyDelay:     250 * time.Millisecond,
		Retries:       RetryCount(2),
		Backoff:       time.Millisecond,
		OnProgress:    func(p Progress) { fx.events = append(fx.events, p) },
	}
	if mutate != nil {
		mutate(&opts)
	}
	connect := func(ctx context.Context) error {
		return fx.bridge.Connect(ctx, config.BridgeAccount{}, "")
	}
	syn, err := New(st, fx.bridge, connect, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Record delays instead of waiting, so tests are fast and exact.
	syn.sleep = func(ctx context.Context, d time.Duration) error {
		fx.sleeps = append(fx.sleeps, d)
		return ctx.Err()
	}
	fx.syn = syn
	return fx
}

func (fx *fixture) run() (Result, error) {
	fx.t.Helper()
	return fx.syn.Run(context.Background())
}

func (fx *fixture) mustRun() Result {
	fx.t.Helper()
	res, err := fx.run()
	if err != nil {
		fx.t.Fatalf("Run: %v", err)
	}
	return res
}

func (fx *fixture) cachedUIDs(mailbox string, validity uint32) []uint32 {
	fx.t.Helper()
	uids, err := fx.st.CachedUIDs(context.Background(), acct, mailbox, validity)
	if err != nil {
		fx.t.Fatal(err)
	}
	return uids
}

func rangeUIDs(first, last uint32) []uint32 {
	var out []uint32
	for u := first; u <= last; u++ {
		out = append(out, u)
	}
	return out
}

func TestPlanChunksNewestFirstAndNoCachedInsideWindow(t *testing.T) {
	server := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cached := map[uint32]bool{4: true, 5: true, 9: true}
	chunks, pending := planChunks(server, cached, 3)
	if pending != 7 {
		t.Fatalf("pending = %d, want 7", pending)
	}
	want := []chunk{
		{First: 10, Last: 10, Count: 1},
		{First: 6, Last: 8, Count: 3},
		{First: 1, Last: 3, Count: 3},
	}
	if !reflect.DeepEqual(chunks, want) {
		t.Errorf("chunks = %+v, want %+v", chunks, want)
	}
	// Nothing pending: no windows.
	if c, p := planChunks(server, map[uint32]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true, 9: true, 10: true}, 3); len(c) != 0 || p != 0 {
		t.Errorf("fully cached: chunks %v pending %d", c, p)
	}
}

func TestFullSyncPopulatesCacheAndCounts(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.addBox("Folders/Work", model.KindFolder, 5)
	for i := 0; i < 4; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.bridge.appendMsg("Folders/Work")

	res := fx.mustRun()
	if res.Mailboxes != 2 || res.HeadersFetched != 5 || res.BodiesFetched != 5 || res.BodiesFailed != 0 {
		t.Errorf("result = %+v", res)
	}
	counts, err := fx.st.Counts(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.MailboxCounts{{Mailbox: "Folders/Work", Messages: 1, Bodies: 1}, {Mailbox: "INBOX", Messages: 4, Bodies: 4}}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("counts = %v, want %v", counts, want)
	}
	states, _ := fx.st.GetSyncState(context.Background(), acct)
	for _, s := range states {
		if s.LastSync.IsZero() {
			t.Errorf("%s: LastSync not recorded", s.Name)
		}
	}
	body, err := fx.st.GetBody(context.Background(), store.MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 1, UID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "body of 2" || len(body.Attachments) != 1 || body.Attachments[0].Filename != "f.pdf" {
		t.Errorf("cached body = %+v", body)
	}
	// Mailboxes are visited INBOX first, and progress reports every state.
	var states2 []State
	for _, e := range fx.events {
		if e.Mailbox == "" || len(states2) == 0 || states2[len(states2)-1] != e.State {
			states2 = append(states2, e.State)
		}
	}
	if states2[0] != StateConnecting || states2[len(states2)-1] != StateDone {
		t.Errorf("state sequence = %v", states2)
	}
}

func TestHeadersAreFetchedNewestFirstAndVisibleEarly(t *testing.T) {
	fx := newFixture(t, nil)
	const total = 450 // three windows of mail.BatchSize (200, 200, 50)
	for i := 0; i < total; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	var topAfterFirstBatch []uint32
	fx.bridge.onHeaders = func(rng mail.UIDRange) {
		if len(fx.bridge.headerRanges) == 1 && topAfterFirstBatch == nil {
			// Second window: the first has been committed, so the cache already
			// holds the newest messages.
			msgs, err := fx.st.ListMessages(context.Background(), acct, "INBOX", store.ListOptions{Limit: 3})
			if err != nil {
				t.Errorf("ListMessages during sync: %v", err)
			}
			for _, m := range msgs {
				topAfterFirstBatch = append(topAfterFirstBatch, m.UID)
			}
		}
	}
	fx.mustRun()

	if len(fx.bridge.headerRanges) != 3 {
		t.Fatalf("header windows = %d, want 3", len(fx.bridge.headerRanges))
	}
	wantRanges := []mail.UIDRange{{First: 251, Last: 450}, {First: 51, Last: 250}, {First: 1, Last: 50}}
	if !reflect.DeepEqual(fx.bridge.headerRanges, wantRanges) {
		t.Errorf("header windows = %+v, want %+v", fx.bridge.headerRanges, wantRanges)
	}
	if !reflect.DeepEqual(topAfterFirstBatch, []uint32{450, 449, 448}) {
		t.Errorf("cache top after first window = %v, want newest UIDs", topAfterFirstBatch)
	}
}

func TestUIDValidityChangeResyncsMailbox(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("INBOX")
	fx.mustRun()

	fx.bridge.recreate("INBOX")
	fx.bridge.appendMsg("INBOX")
	fx.mustRun()

	if got := fx.cachedUIDs("INBOX", 1); len(got) != 0 {
		t.Errorf("old epoch still cached: %v", got)
	}
	if got := fx.cachedUIDs("INBOX", 2); !reflect.DeepEqual(got, []uint32{1}) {
		t.Errorf("new epoch cached = %v, want [1]", got)
	}
	if n := fx.bridge.headerFetches("INBOX", 1); n != 2 {
		t.Errorf("new UID 1 fetched %d times across epochs, want 2 (once per epoch)", n)
	}
}

func TestServerDeletionsAndFlagChangesReconciled(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 4; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.mustRun()

	fx.bridge.expunge("INBOX", 2)
	fx.bridge.setFlags("INBOX", 3, `\Seen`, `\Flagged`)
	before := fx.bridge.headerFetches("INBOX", 4)
	res := fx.mustRun()

	if res.Deleted != 1 {
		t.Errorf("Deleted = %d, want 1", res.Deleted)
	}
	if got := fx.cachedUIDs("INBOX", 1); !reflect.DeepEqual(got, []uint32{1, 3, 4}) {
		t.Errorf("cached after deletion = %v", got)
	}
	m, err := fx.st.GetMessage(context.Background(), store.MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 1, UID: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(m.Flags, []string{`\Flagged`, `\Seen`}) {
		t.Errorf("flags after change = %v", m.Flags)
	}
	if n := fx.bridge.headerFetches("INBOX", 4); n != before {
		t.Errorf("unchanged message re-fetched on reconcile (%d -> %d)", before, n)
	}
}

func TestNewMailIsFetchedWithoutRefetchingCache(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 3; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.mustRun()
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("INBOX")
	res := fx.mustRun()

	if res.HeadersFetched != 2 || res.BodiesFetched != 2 {
		t.Errorf("incremental result = %+v, want 2 headers and 2 bodies", res)
	}
	if !reflect.DeepEqual(fx.bridge.headerRanges[len(fx.bridge.headerRanges)-1], mail.UIDRange{First: 4, Last: 5}) {
		t.Errorf("incremental window = %+v", fx.bridge.headerRanges[len(fx.bridge.headerRanges)-1])
	}
	for uid := uint32(1); uid <= 3; uid++ {
		if n := fx.bridge.headerFetches("INBOX", uid); n != 1 {
			t.Errorf("UID %d header fetched %d times", uid, n)
		}
	}
}

func TestInterruptedSyncResumesWithoutRefetch(t *testing.T) {
	fx := newFixture(t, nil)
	const total = 450
	for i := 0; i < total; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	fx.bridge.onHeaders = func(rng mail.UIDRange) {
		calls++
		if calls == 2 { // cancel while the second window is in flight
			cancel()
		}
	}
	if _, err := fx.syn.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted Run error = %v, want context.Canceled", err)
	}
	committed := fx.cachedUIDs("INBOX", 1)
	if !reflect.DeepEqual(committed, rangeUIDs(251, 450)) {
		t.Fatalf("after interrupt cache holds %d UIDs, want the first window (251-450)", len(committed))
	}

	fx.bridge.onHeaders = nil
	fx.bridge.headerRanges = nil
	fx.mustRun()

	for _, rng := range fx.bridge.headerRanges {
		if rng.First > 250 {
			t.Errorf("resumed run refetched committed window %+v", rng)
		}
	}
	for uid := uint32(1); uid <= total; uid++ {
		if n := fx.bridge.headerFetches("INBOX", uid); n != 1 {
			t.Fatalf("UID %d header fetched %d times across interrupted run and resume, want 1", uid, n)
		}
	}
	if got := fx.cachedUIDs("INBOX", 1); len(got) != total {
		t.Errorf("cache holds %d UIDs after resume, want %d", len(got), total)
	}
}

func TestInterruptedBodiesResumeWithoutRefetch(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 8; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetched := 0
	fx.bridge.onBody = func(string, uint32) {
		fetched++
		if fetched == 3 {
			cancel()
		}
	}
	if _, err := fx.syn.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted Run error = %v", err)
	}
	fx.bridge.onBody = nil
	fx.mustRun()
	for uid := uint32(1); uid <= 8; uid++ {
		key := fmt.Sprintf("INBOX:%d", uid)
		if n := fx.bridge.bodyFetch[key]; n != 1 {
			t.Errorf("body of UID %d fetched %d times, want 1", uid, n)
		}
	}
	// Newest first across the whole run.
	want := []string{"INBOX:8", "INBOX:7", "INBOX:6", "INBOX:5", "INBOX:4", "INBOX:3", "INBOX:2", "INBOX:1"}
	if !reflect.DeepEqual(fx.bridge.bodyOrder, want) {
		t.Errorf("body fetch order = %v", fx.bridge.bodyOrder)
	}
}

func TestBodyFetchIsThrottledAndBatched(t *testing.T) {
	fx := newFixture(t, func(o *Options) {
		o.BodyBatchSize = 2
		o.BodyDelay = 750 * time.Millisecond
	})
	for i := 0; i < 5; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.mustRun()

	// Five body fetches need four delays: none before the first fetch.
	var bodySleeps []time.Duration
	for _, d := range fx.sleeps {
		if d == 750*time.Millisecond {
			bodySleeps = append(bodySleeps, d)
		}
	}
	if len(bodySleeps) != 4 {
		t.Errorf("body delays = %v (all sleeps %v), want 4 x 750ms", bodySleeps, fx.sleeps)
	}
	if len(fx.bridge.bodyOrder) != 5 {
		t.Errorf("bodies fetched = %d, want 5", len(fx.bridge.bodyOrder))
	}
}

func TestTransientConnectionFailureReconnects(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 3; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.bridge.connectFails = []error{transportErr()} // first connect fails
	fx.bridge.opFails = []error{io.ErrUnexpectedEOF} // a later op drops the connection
	res := fx.mustRun()
	if res.HeadersFetched != 3 || res.BodiesFetched != 3 {
		t.Errorf("result after reconnects = %+v", res)
	}
	if fx.bridge.connects < 3 {
		t.Errorf("connects = %d, want at least 3 (failed, initial, reconnect)", fx.bridge.connects)
	}
}

func TestPersistentConnectionFailureAbortsWithoutCorruptingCache(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Retries = RetryCount(2) })
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("INBOX")
	fx.bridge.connectFails = []error{transportErr(), transportErr(), transportErr()}
	if _, err := fx.run(); err == nil {
		t.Fatal("Run succeeded with Bridge down")
	}
	if got := fx.cachedUIDs("INBOX", 1); len(got) != 0 {
		t.Errorf("cache changed while Bridge was down: %v", got)
	}
}

func TestPermanentCertErrorIsNotRetried(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.connectFails = []error{&mail.UntrustedCertError{Fingerprint: "aa", Subject: "bridge"}}
	_, err := fx.run()
	if !errors.Is(err, mail.ErrUntrustedCert) {
		t.Fatalf("Run error = %v, want ErrUntrustedCert", err)
	}
	if fx.bridge.connects != 1 {
		t.Errorf("connects = %d, want 1 (no retry on an untrusted certificate)", fx.bridge.connects)
	}
}

func TestCancelDuringBodiesStopsCleanly(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.bridge.onBody = func(string, uint32) { cancel() }
	if _, err := fx.syn.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if got := fx.cachedUIDs("INBOX", 1); len(got) != 1 {
		t.Errorf("headers lost on cancel: %v", got)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/c.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	noop := func(context.Context) error { return nil }
	if _, err := New(st, newFakeBridge(), noop, Options{}); err == nil {
		t.Error("missing account accepted")
	}
	if _, err := New(st, newFakeBridge(), noop, Options{Account: "a", BodyDelay: -1}); err == nil {
		t.Error("negative body delay accepted")
	}
}
