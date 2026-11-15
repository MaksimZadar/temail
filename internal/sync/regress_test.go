package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// A deterministic body failure is attempted once per run and counted, so it
// is dropped after store.MaxBodyAttempts runs.
func TestDeterministicBadBodyIsBoundedAcrossRuns(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 3; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.bridge.bodyErr["INBOX:2"] = fmt.Errorf("%w: UID 2 in INBOX", mail.ErrMessageNotFound)

	for run := 1; run <= store.MaxBodyAttempts; run++ {
		res := fx.mustRun()
		wantFetched := 0 // the two good bodies are cached by the first run
		if run == 1 {
			wantFetched = 2
		}
		if res.BodiesFetched != wantFetched || res.BodiesFailed != 1 {
			t.Errorf("run %d result = %+v, want %d fetched and 1 failed", run, res, wantFetched)
		}
	}
	if n := fx.bridge.bodyFetchAttempts("INBOX:2"); n != store.MaxBodyAttempts {
		t.Errorf("bad body attempted %d times, want %d (once per run)", n, store.MaxBodyAttempts)
	}
	if res := fx.mustRun(); res.BodiesFailed != 0 || fx.bridge.bodyFetchAttempts("INBOX:2") != store.MaxBodyAttempts {
		t.Errorf("exhausted message attempted again: %+v", res)
	}
}

// With small batches, a failing message must not stop later ones, and it is
// not offered again within the run.
func TestBadBodyDoesNotBlockLaterBodiesWithSmallBatches(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.BodyBatchSize = 1 })
	for i := 0; i < 4; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.bridge.bodyErr["INBOX:4"] = fmt.Errorf("%w: UID 4", mail.ErrMessageNotFound)
	fx.bridge.bodyErr["INBOX:3"] = fmt.Errorf("%w: UID 3", mail.ErrMessageNotFound)
	res := fx.mustRun()
	if res.BodiesFetched != 2 || res.BodiesFailed != 2 {
		t.Errorf("result = %+v, want 2 fetched and 2 failed in one run", res)
	}
	if n := fx.bridge.bodyFetchAttempts("INBOX:4"); n != 1 {
		t.Errorf("failing body offered %d times in one run, want 1", n)
	}
}

// A server NO during Bridge startup is not a property of the message: it must
// not count toward the attempt bound.
func TestTransientBodyErrorIsNotCounted(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.bodyErr["INBOX:1"] = &imap.Error{Type: imap.StatusResponseTypeNo, Text: "starting up"}
	for i := 0; i < store.MaxBodyAttempts+2; i++ {
		fx.mustRun()
	}
	pending, err := fx.st.PendingBodies(context.Background(), acct, nil, 10)
	if err != nil || len(pending) != 1 {
		t.Errorf("transient failures removed the body from the queue: %v %v", pending, err)
	}
	delete(fx.bridge.bodyErr, "INBOX:1")
	fx.mustRun()
	if n := fx.bridge.bodyFetch["INBOX:1"]; n != 1 {
		t.Errorf("body not fetched after recovery: %d", n)
	}
}

// A server NO on a mailbox is answered by the server, so it is not retried
// and does not reconnect.
func TestServerReplyIsNotRetriedOrReconnected(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.mailboxErr["INBOX"] = &imap.Error{Type: imap.StatusResponseTypeNo, Text: "mailbox busy"}
	connectsBefore := fx.bridge.connects
	res, err := fx.run()
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("Run error = %v, want ErrPartial", err)
	}
	if len(res.MailboxErrors) != 1 || res.MailboxErrors[0].Mailbox != "INBOX" {
		t.Errorf("MailboxErrors = %+v", res.MailboxErrors)
	}
	// One connect for the run itself; a server NO must not add a reconnect.
	if fx.bridge.connects != connectsBefore+1 {
		t.Errorf("server NO caused %d extra connects", fx.bridge.connects-connectsBefore-1)
	}
	if fx.bridge.listUIDsCalls != 1 {
		t.Errorf("ListUIDs called %d times, want 1 (no retry)", fx.bridge.listUIDsCalls)
	}
}

// One failing mailbox is recorded; the other mailboxes and the body phase run.
func TestOneFailingMailboxDoesNotAbortRun(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.addBox("Folders/Work", model.KindFolder, 5)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.appendMsg("Folders/Work")
	fx.bridge.mailboxErr["Folders/Work"] = &imap.Error{Type: imap.StatusResponseTypeBad, Text: "broken folder"}

	res, err := fx.run()
	if !errors.Is(err, ErrPartial) {
		t.Fatalf("Run error = %v, want ErrPartial", err)
	}
	if len(res.MailboxErrors) != 1 || res.MailboxErrors[0].Mailbox != "Folders/Work" {
		t.Errorf("MailboxErrors = %+v", res.MailboxErrors)
	}
	if got := fx.cachedUIDs("INBOX", 1); !reflect.DeepEqual(got, []uint32{1}) {
		t.Errorf("INBOX not synced after the other mailbox failed: %v", got)
	}
	if res.BodiesFetched != 1 {
		t.Errorf("body phase did not run for other mailboxes: %+v", res)
	}
}

// A rejected password fails the same way every run, so it is not retried.
func TestAuthFailureIsNotRetried(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.connectFails = []error{fmt.Errorf("%w: log in: %w", mail.ErrAuthFailed, &imap.Error{Type: imap.StatusResponseTypeNo})}
	_, err := fx.run()
	if !errors.Is(err, mail.ErrAuthFailed) {
		t.Fatalf("Run error = %v, want ErrAuthFailed", err)
	}
	if fx.bridge.connects != 1 {
		t.Errorf("connects = %d, want 1 (bad password is not retried)", fx.bridge.connects)
	}
}

// Retries: RetryCount(0) means no retries, not the default.
func TestRetryCountZeroDisablesRetries(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Retries = RetryCount(0) })
	fx.bridge.appendMsg("INBOX")
	fx.bridge.opFails = []error{io.ErrUnexpectedEOF}
	if _, err := fx.run(); err == nil {
		t.Fatal("Run succeeded despite a failed operation and no retries")
	}
	if fx.bridge.connects != 1 {
		t.Errorf("connects = %d, want 1 (the initial connect only)", fx.bridge.connects)
	}
}

// An empty listing must not remove cached mailboxes.
func TestEmptyListingDoesNotWipeCache(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.mustRun()

	fx.bridge.emptyListing = true
	if _, err := fx.run(); err == nil {
		t.Fatal("empty mailbox listing was accepted")
	}
	st, _ := fx.st.GetSyncState(context.Background(), acct)
	if len(st) != 1 || len(fx.cachedUIDs("INBOX", 1)) != 1 {
		t.Errorf("empty listing removed cached mailboxes: %+v", st)
	}
}

// UIDVALIDITY 0 from the server is not an epoch and must not be cached.
func TestZeroUIDValidityFromServerIsNotCached(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.appendMsg("INBOX")
	fx.bridge.zeroValidity["INBOX"] = true
	res, err := fx.run()
	if !errors.Is(err, ErrPartial) || len(res.MailboxErrors) != 1 {
		t.Fatalf("Run = %+v, %v; want INBOX recorded as a mailbox error", res, err)
	}
	if got := fx.cachedUIDs("INBOX", 0); len(got) != 0 {
		t.Errorf("messages cached under UIDVALIDITY 0: %v", got)
	}
}

// flagRecorder records the flag updates that reach the store.
type flagRecorder struct {
	store.Store
	updates []store.FlagUpdate
}

func (r *flagRecorder) UpdateFlags(ctx context.Context, account, mailbox string, v uint32, updates []store.FlagUpdate) error {
	r.updates = append(r.updates, updates...)
	return r.Store.UpdateFlags(ctx, account, mailbox, v, updates)
}

// Reconcile writes only the rows whose flags changed, and lists each mailbox
// once per run (one UID FETCH 1:* per mailbox).
func TestReconcileWritesOnlyChangedFlags(t *testing.T) {
	fx := newFixture(t, nil)
	for i := 0; i < 5; i++ {
		fx.bridge.appendMsg("INBOX")
	}
	fx.mustRun()

	rec := &flagRecorder{Store: fx.st}
	syn, err := New(rec, fx.bridge, func(ctx context.Context) error {
		return fx.bridge.Connect(ctx, config.BridgeAccount{}, "")
	}, Options{Account: acct, Retries: RetryCount(0)})
	if err != nil {
		t.Fatal(err)
	}
	syn.sleep = func(context.Context, time.Duration) error { return nil }
	fx.bridge.setFlags("INBOX", 4, `\Seen`)
	fx.bridge.listUIDsCalls = 0
	if _, err := syn.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.updates) != 1 || rec.updates[0].UID != 4 {
		t.Errorf("flag updates = %+v, want only UID 4", rec.updates)
	}
	if fx.bridge.listUIDsCalls != 1 {
		t.Errorf("ListUIDs calls = %d, want 1 (one mailbox)", fx.bridge.listUIDsCalls)
	}
}
