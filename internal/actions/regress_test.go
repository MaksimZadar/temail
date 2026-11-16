package actions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestEpochChangeRefusedAndResyncs covers a UIDVALIDITY change for a flag write
// and for a move: each is refused per message and schedules a resync.
func TestEpochChangeRefusedAndResyncs(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		fx := newFixture(t)
		fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
		fx.fw.bumpEpoch("All Mail")

		res, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Applied) != 0 || len(res.Failed) != 1 {
			t.Fatalf("result = %+v", res)
		}
		if !strings.Contains(res.Failed[0].Message, "UIDVALIDITY") {
			t.Fatalf("failure = %q", res.Failed[0].Message)
		}
		if fx.fw.callCount("STORE") != 0 {
			t.Fatalf("wrote with a stale epoch: %v", fx.fw.callList())
		}
		if !fx.resynced("All Mail") {
			t.Fatalf("no resync for All Mail: %v", fx.resync)
		}
	})
	t.Run("move", func(t *testing.T) {
		fx := newFixture(t)
		fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
		fx.fw.bumpEpoch("INBOX")

		res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Applied) != 0 || len(res.Failed) != 1 {
			t.Fatalf("result = %+v", res)
		}
		if fx.fw.callCount("MOVE") != 0 || fx.fw.callCount("COPY") != 0 {
			t.Fatalf("wrote with a stale epoch: %v", fx.fw.callList())
		}
		if !fx.resynced("INBOX") {
			t.Fatalf("no resync for INBOX: %v", fx.resync)
		}
	})
}

// TestNoopFlagWriteHasNoInverse: mark_read on an already-read message writes
// nothing and records no inverse.
func TestNoopFlagWriteHasNoInverse(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", flags: []string{`\Seen`}, mailboxes: []string{"INBOX", "All Mail"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.callCount("STORE") != 0 {
		t.Fatalf("no-op mark_read wrote: %v", fx.fw.callList())
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 1 || entries[0].InverseJSON != "{}" || entries[0].Undoable {
		t.Fatalf("audit = %+v", entries)
	}
	if _, err := fx.ex.Undo(fx.ctx, entries[0].ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo = %v, want ErrNotUndoable", err)
	}
}

// TestUndoRefusesWhenCacheDiverged: the after-state recorded by the action no
// longer matches the cache, so undo refuses instead of restoring the wrong
// thing.
func TestUndoRefusesWhenCacheDiverged(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	readID := entries[0].ID

	// Another action changes the message after the audited one.
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: Star, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	_, err := fx.ex.Undo(fx.ctx, readID)
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("undo error = %v, want ErrDiverged", err)
	}
	if !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("ErrDiverged must satisfy ErrNotUndoable: %v", err)
	}
	if !fx.fw.flagsOf("m1", `\Seen`) {
		t.Fatal("a diverged undo changed the message")
	}
}

// TestBatchIDGroupsRecentAndUndoLast: one action is one batch; Recent returns
// one row for it; UndoLast undoes the whole batch; the undo rows are not
// undoable again.
func TestBatchIDGroupsRecentAndUndoLast(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t,
		seedMsg{mid: "m1", mailboxes: []string{"INBOX"}},
		seedMsg{mid: "m2", mailboxes: []string{"INBOX"}},
	)
	res, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1", "m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.BatchID == "" || len(res.Entries) != 2 {
		t.Fatalf("result = %+v", res)
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 1 || entries[0].Count != 2 || len(entries[0].AuditIDs) != 2 {
		t.Fatalf("recent = %+v", entries)
	}
	if !entries[0].Undoable {
		t.Fatalf("batch is not undoable: %+v", entries[0])
	}

	if res, err := fx.ex.UndoLast(fx.ctx); err != nil || len(res.Applied) != 2 {
		t.Fatalf("UndoLast: %+v %v", res, err)
	}
	for _, mid := range []string{"m1", "m2"} {
		if fx.fw.flagsOf(mid, `\Seen`) {
			t.Fatalf("%s still seen after UndoLast", mid)
		}
	}
	entries, _ = fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 2 {
		t.Fatalf("recent after undo = %+v", entries)
	}
	if entries[0].Type != string(MarkUnread) || entries[0].Undoable || entries[0].Count != 2 {
		t.Fatalf("undo batch = %+v", entries[0])
	}
	if !entries[1].Undone {
		t.Fatalf("original batch not undone: %+v", entries[1])
	}
	// There is no redo loop: the undo batch has no inverse.
	if _, err := fx.ex.UndoLast(fx.ctx); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("second UndoLast = %v, want ErrNotUndoable", err)
	}
}

// TestUnsyncedFolderIsRefused: a placement in a mailbox that never completed a
// sync is not used to guess a move.
func TestUnsyncedFolderIsRefused(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	if err := fx.st.PurgeMailbox(fx.ctx, testAccount, "INBOX"); err != nil {
		t.Fatal(err)
	}
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "has not been synced") {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.callCount("MOVE") != 0 || fx.fw.callCount("COPY") != 0 {
		t.Fatalf("wrote from an unsynced mailbox: %v", fx.fw.callList())
	}

	if err := fx.st.MarkSynced(fx.ctx, testAccount, "INBOX", time.Unix(1_700_000_000, 0), 0); err != nil {
		t.Fatal(err)
	}
	res, err = fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("after sync: %+v %v", res, err)
	}
}

// TestOneNoDoesNotBreakLaterGroups: a NO answer keeps the connection; the next
// group writes without an ErrNotConnected.
func TestOneNoDoesNotBreakLaterGroups(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t,
		seedMsg{mid: "m1", mailboxes: []string{"Archive", "All Mail"}},
		seedMsg{mid: "m2", mailboxes: []string{"INBOX", "All Mail"}},
	)
	fx.fw.refuseWrites["Archive"] = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Trash, MessageIDs: []string{"m1", "m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "m2" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Failed) != 1 || res.Failed[0].MID != "m1" {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.connectCount() != 1 {
		t.Fatalf("one NO caused %d connects; the connection was dropped", fx.fw.connectCount())
	}
}

// TestTransportErrorReconnects: a transport error drops the connection, and the
// next group reconnects and succeeds.
func TestTransportErrorReconnects(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t,
		seedMsg{mid: "m1", mailboxes: []string{"Archive", "All Mail"}},
		seedMsg{mid: "m2", mailboxes: []string{"INBOX", "All Mail"}},
	)
	fx.fw.transportWrites["Archive"] = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Trash, MessageIDs: []string{"m1", "m2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.connectCount() < 2 {
		t.Fatalf("connect count = %d, want a reconnect", fx.fw.connectCount())
	}
}

// TestAmbiguousDestSearchIsNotGuessed: more than one search hit is a failure,
// not a zip-by-index.
func TestAmbiguousDestSearchIsNotGuessed(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	fx.fw.multiSearch["m1"] = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "matches 2 copies") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := fx.cachePlacements(t, "m1")["Labels/Test"]; ok {
		t.Fatal("an ambiguous result wrote a placement")
	}
	if !fx.resynced("Labels/Test") {
		t.Fatalf("no resync: %v", fx.resync)
	}
}

// TestCopyUIDEpochMismatchIsUnknown: a COPYUID from another epoch must not be
// turned into a placement.
func TestCopyUIDEpochMismatchIsUnknown(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"All Mail"}})
	fx.fw.copyValidity = 99

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "unknown outcome") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := fx.cachePlacements(t, "m1")["Archive"]; ok {
		t.Fatal("placed the message with a mismatched COPYUID epoch")
	}
	if !fx.resynced("Archive") {
		t.Fatalf("no resync: %v", fx.resync)
	}
}

// TestMissingUIDPLUSRefusesLabelExpunge: no UIDPLUS means no expunge, and the
// label placement stays cached.
func TestMissingUIDPLUSRefusesLabelExpunge(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "Labels/Test"}})
	fx.fw.noUIDPLUS = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: RemoveLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "UID EXPUNGE") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := fx.cachePlacements(t, "m1")["Labels/Test"]; !ok {
		t.Fatal("cache lost the label although the expunge was refused")
	}
	if fx.fw.callCount("EXPUNGE") != 0 {
		t.Fatalf("expunged: %v", fx.fw.callList())
	}
}

// TestPartialMoveKeepsConfirmedTarget: the source still holds the message, but
// the confirmed target placement is cached and both mailboxes are resynced.
func TestPartialMoveKeepsConfirmedTarget(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	fx.fw.moveLag = 10 // more retries than VerifyRetries

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "still in INBOX") {
		t.Fatalf("result = %+v", res)
	}
	placements := fx.cachePlacements(t, "m1")
	if _, ok := placements["Archive"]; !ok {
		t.Fatalf("confirmed target placement was not cached: %v", placements)
	}
	if _, ok := placements["INBOX"]; !ok {
		t.Fatalf("source was removed although the move is partial: %v", placements)
	}
	if !fx.resynced("INBOX") || !fx.resynced("Archive") {
		t.Fatalf("resync = %v", fx.resync)
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 1 || entries[0].Error == "" || !strings.Contains(entries[0].AfterJSON, "Archive") {
		t.Fatalf("audit = %+v", entries)
	}
}

// TestAuditRowExistsBeforeWrite: the audit row is inserted with verified = 0
// before the write runs, so a crash mid-write leaves evidence.
func TestAuditRowExistsBeforeWrite(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	saw := false
	fx.fw.onStore = func() {
		entries, err := fx.audit.Recent(fx.ctx, testAccount, 10)
		if err == nil && len(entries) == 1 && !entries[0].Verified {
			saw = true
		}
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("no verified = 0 audit row existed when the write ran")
	}
}

// TestRecentAndUndoLastAreScopedByAccount: another account's audit rows never
// leak into this account's recent list or undo last.
func TestRecentAndUndoLastAreScopedByAccount(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	if _, err := fx.audit.Insert(fx.ctx, Entry{
		TS: time.Now(), Account: "other", Type: string(Star), MID: "x",
		ParamsJSON: "{}", InverseJSON: `{"type":"unstar"}`, Verified: true,
		BatchID: "other-batch", AfterJSON: `{"mailboxes":[],"flags":[]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	entries, err := fx.ex.Recent(fx.ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].BatchID == "other-batch" {
		t.Fatalf("recent = %+v", entries)
	}
	if _, err := fx.ex.UndoLast(fx.ctx); err != nil {
		t.Fatal(err)
	}
	if fx.fw.flagsOf("m1", `\Seen`) {
		t.Fatal("UndoLast did not undo this account's action")
	}
}

// TestCancelledWriteStillLeavesAnAuditRow: the audit row exists before the
// write, and a cancellation still finalises it, so no attempted write is lost.
func TestCancelledWriteStillLeavesAnAuditRow(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	ctx, cancel := context.WithCancel(fx.ctx)
	fx.fw.onStore = cancel

	res, err := fx.ex.Apply(ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	entries, rerr := fx.ex.Recent(fx.ctx, 10)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 1 || entries[0].Verified || !strings.Contains(entries[0].Error, "context canceled") {
		t.Fatalf("audit = %+v", entries)
	}
}
