package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

const testAccount = "acct"

var defaultBoxes = []string{
	"INBOX", "Sent", "Drafts", "Trash", "Spam", "Archive", "All Mail",
	"Folders/Work", "Labels/Test",
}

func boxKind(name string) model.MailboxKind {
	switch name {
	case "INBOX":
		return model.KindInbox
	case "Sent":
		return model.KindSent
	case "Drafts":
		return model.KindDrafts
	case "Trash":
		return model.KindTrash
	case "Spam":
		return model.KindSpam
	case "Archive":
		return model.KindArchive
	case "Labels/Test":
		return model.KindLabel
	case "Folders/Work":
		return model.KindFolder
	default:
		return model.KindFolder
	}
}

type seedMsg struct {
	mid       string
	flags     []string
	mailboxes []string
}

type fixture struct {
	st     store.Store
	audit  *Audit
	fw     *fakeWriter
	ex     *Executor
	ctx    context.Context
	resync []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	audit, err := OpenAudit(filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = audit.Close() })
	fw := newFakeWriter(defaultBoxes)
	boxes := make([]model.Mailbox, 0, len(defaultBoxes))
	for _, name := range defaultBoxes {
		boxes = append(boxes, model.Mailbox{Name: name, Kind: boxKind(name), DisplayName: name, UIDValidity: 1})
	}
	if err := st.UpsertMailboxes(context.Background(), testAccount, boxes); err != nil {
		t.Fatal(err)
	}
	// The seeded mailboxes represent a completed sync: a write to a mailbox
	// that never synced is refused, and only the unsynced test relies on that.
	for _, name := range defaultBoxes {
		if err := st.MarkSynced(context.Background(), testAccount, name, time.Unix(1_700_000_000, 0), 0); err != nil {
			t.Fatal(err)
		}
	}
	fx := &fixture{st: st, audit: audit, fw: fw, ctx: context.Background()}
	ex, err := New(st, fw, audit, Options{
		Account: testAccount,
		Resolve: func(context.Context) (config.BridgeAccount, string, error) {
			return config.BridgeAccount{ID: testAccount, Username: "u@test"}, "pw", nil
		},
		ChunkDelay:    time.Millisecond,
		VerifyDelay:   time.Millisecond,
		VerifyRetries: 2,
		Logf:          func(string, ...any) {},
		Resync: func(_ context.Context, mailboxes []string) {
			fx.resync = append(fx.resync, mailboxes...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ex.Close() })
	fx.ex = ex
	return fx
}

func (f *fixture) seed(t *testing.T, msgs ...seedMsg) {
	t.Helper()
	date := time.Unix(1_700_000_000, 0)
	var headers []model.MessageHeader
	for _, m := range msgs {
		f.fw.addMessage(m.mid, m.flags, m.mailboxes...)
		for _, mb := range m.mailboxes {
			uid, ok := f.fw.placement(m.mid, mb)
			if !ok {
				t.Fatalf("fake has no placement %s/%s", m.mid, mb)
			}
			headers = append(headers, model.MessageHeader{
				Mailbox: mb, UIDValidity: 1, UID: uid, InternalID: m.mid, Canonical: true,
				Subject: "test", Date: date, Flags: m.flags,
			})
		}
	}
	if err := f.st.UpsertHeaders(f.ctx, testAccount, headers); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) resynced(mailbox string) bool {
	for _, mb := range f.resync {
		if mb == mailbox {
			return true
		}
	}
	return false
}

func (f *fixture) cachePlacements(t *testing.T, mid string) map[string]uint32 {
	t.Helper()
	mm, err := f.st.MembershipOfMessages(f.ctx, testAccount, []string{mid})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]uint32{}
	for _, p := range mm[mid] {
		out[p.Mailbox] = p.UID
	}
	return out
}

func (f *fixture) cacheFlags(t *testing.T, mid string) []string {
	t.Helper()
	fl, err := f.st.FlagsOfMessages(f.ctx, testAccount, []string{mid})
	if err != nil {
		t.Fatal(err)
	}
	return fl[mid]
}

func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func TestMarkReadStarAndUndo(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail", "Labels/Test"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if !fx.fw.flagsOf("m1", `\Seen`) {
		t.Fatal("fake message is not seen")
	}
	if !hasFlag(fx.cacheFlags(t, "m1"), `\Seen`) {
		t.Fatal("cache flags do not show \\Seen")
	}
	if fx.fw.callCount("STORE All Mail") != 1 {
		t.Fatalf("flag write did not use All Mail: %v", fx.fw.callList())
	}
	states, err := fx.st.GetSyncState(fx.ctx, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		if s.Name == "INBOX" && s.Unseen != 0 {
			t.Errorf("INBOX unseen = %d, want 0 after mark_read", s.Unseen)
		}
	}

	entries, err := fx.ex.Recent(fx.ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Verified || entries[0].Type != string(MarkRead) {
		t.Fatalf("audit = %+v", entries)
	}
	if res, err := fx.ex.Undo(fx.ctx, entries[0].ID); err != nil || len(res.Applied) != 1 {
		t.Fatalf("undo: res=%+v err=%v", res, err)
	}
	if fx.fw.flagsOf("m1", `\Seen`) {
		t.Fatal("undo did not clear \\Seen")
	}
	if hasFlag(fx.cacheFlags(t, "m1"), `\Seen`) {
		t.Fatal("cache flags still show \\Seen after undo")
	}
	if fx.fw.callCount("STORE All Mail") != 2 {
		t.Fatalf("undo did not use one STORE: %v", fx.fw.callList())
	}

	res, err = fx.ex.Apply(fx.ctx, Action{Type: Star, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("star: %+v %v", res, err)
	}
	if !fx.fw.flagsOf("m1", `\Flagged`) {
		t.Fatal("\\Flagged not set")
	}
}

func TestArchiveMovesAndUndoResolvesNewUIDs(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.callCount("MOVE INBOX->Archive") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	placements := fx.cachePlacements(t, "m1")
	if _, ok := placements["Archive"]; !ok {
		t.Fatalf("cache has no Archive placement: %v", placements)
	}
	if _, ok := placements["INBOX"]; ok {
		t.Fatalf("cache still has INBOX placement: %v", placements)
	}
	archiveUID := placements["Archive"]
	firstFakeUID, _ := fx.fw.placement("m1", "Archive")
	if archiveUID != firstFakeUID {
		t.Fatalf("cache Archive UID = %d, fake = %d", archiveUID, firstFakeUID)
	}

	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 1 || !strings.Contains(entries[0].InverseJSON, `"folder":"INBOX"`) {
		t.Fatalf("inverse = %s", entries[0].InverseJSON)
	}
	if res, err := fx.ex.Undo(fx.ctx, entries[0].ID); err != nil || len(res.Applied) != 1 {
		t.Fatalf("undo: %+v %v", res, err)
	}
	if fx.fw.callCount("MOVE Archive->INBOX") != 1 {
		t.Fatalf("undo calls = %v", fx.fw.callList())
	}
	placements = fx.cachePlacements(t, "m1")
	if _, ok := placements["INBOX"]; !ok {
		t.Fatalf("undo did not restore INBOX: %v", placements)
	}
	if got := placements["INBOX"]; got == archiveUID {
		t.Fatalf("INBOX UID was not refreshed: %d", got)
	}
	entries, _ = fx.ex.Recent(fx.ctx, 10)
	if !entries[len(entries)-1].Undone {
		t.Fatal("original audit entry was not marked undone")
	}
}

func TestArchiveFromAllMailUsesCopyAndIsNotUndoable(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"All Mail"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	// The copy is confirmed, but the source stays: this is a partial outcome,
	// never a full success.
	if len(res.Applied) != 0 || len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Failed[0].Message, "archived copy created") {
		t.Fatalf("failure message = %q", res.Failed[0].Message)
	}
	if fx.fw.callCount("COPY All Mail->Archive") != 1 || fx.fw.callCount("MOVE") != 0 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	placements := fx.cachePlacements(t, "m1")
	if _, ok := placements["Archive"]; !ok {
		t.Fatalf("no Archive placement: %v", placements)
	}
	if _, ok := placements["All Mail"]; !ok {
		t.Fatalf("All Mail placement was removed: %v", placements)
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if entries[0].Error == "" || !strings.Contains(entries[0].AfterJSON, "Archive") {
		t.Fatalf("audit = %+v", entries[0])
	}
	if _, err := fx.ex.Undo(fx.ctx, entries[0].ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo error = %v, want ErrNotUndoable", err)
	}
}

func TestArchiveFallsBackToCopyWhenMoveIsRefused(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	fx.fw.refuseMoveToArchive = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "archived copy created") {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.callCount("COPY INBOX->Archive") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	placements := fx.cachePlacements(t, "m1")
	if _, ok := placements["Archive"]; !ok {
		t.Fatalf("no Archive placement: %v", placements)
	}
	if _, ok := placements["INBOX"]; !ok {
		t.Fatalf("COPY fallback should leave INBOX: %v", placements)
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if _, err := fx.ex.Undo(fx.ctx, entries[0].ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo error = %v, want ErrNotUndoable", err)
	}
}

func TestMoveToFolderFromAllMailUsesCopy(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"All Mail"}})
	res, err := fx.ex.Apply(fx.ctx, Action{Type: Move, Params: Params{Folder: "Folders/Work"}, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Message, "original still in All Mail") {
		t.Fatalf("result = %+v", res)
	}
	if fx.fw.callCount("COPY All Mail->Folders/Work") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	if _, ok := fx.cachePlacements(t, "m1")["Folders/Work"]; !ok {
		t.Fatalf("no folder placement: %v", fx.cachePlacements(t, "m1"))
	}
}

func TestTrashSpamNotSpam(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t,
		seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}},
		seedMsg{mid: "m2", mailboxes: []string{"INBOX", "All Mail"}},
		seedMsg{mid: "m3", mailboxes: []string{"Spam", "All Mail"}},
	)
	steps := []struct {
		mid  string
		typ  Type
		call string
		dest string
	}{
		{"m1", Trash, "MOVE INBOX->Trash", "Trash"},
		{"m2", Spam, "MOVE INBOX->Spam", "Spam"},
		{"m3", NotSpam, "MOVE Spam->INBOX", "INBOX"},
	}
	for _, step := range steps {
		res, err := fx.ex.Apply(fx.ctx, Action{Type: step.typ, MessageIDs: []string{step.mid}})
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("%s: %+v %v", step.typ, res, err)
		}
		if fx.fw.callCount(step.call) != 1 {
			t.Fatalf("%s calls = %v", step.typ, fx.fw.callList())
		}
		if _, ok := fx.cachePlacements(t, step.mid)[step.dest]; !ok {
			t.Fatalf("%s: cache has no %s placement", step.typ, step.dest)
		}
	}
}

func TestAddAndRemoveLabel(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})

	res, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if fx.fw.callCount("COPY All Mail->Labels/Test") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	placements := fx.cachePlacements(t, "m1")
	if _, ok := placements["Labels/Test"]; !ok {
		t.Fatalf("no label placement: %v", placements)
	}
	if _, ok := placements["INBOX"]; !ok {
		t.Fatalf("add_label removed INBOX: %v", placements)
	}

	res, err = fx.ex.Apply(fx.ctx, Action{Type: RemoveLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("remove: %+v %v", res, err)
	}
	if fx.fw.callCount("EXPUNGE Labels/Test") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
	placements = fx.cachePlacements(t, "m1")
	if _, ok := placements["Labels/Test"]; ok {
		t.Fatalf("label placement still cached: %v", placements)
	}
	if _, ok := placements["INBOX"]; !ok {
		t.Fatalf("remove_label removed INBOX: %v", placements)
	}
	if fx.fw.callCount("EXPUNGE") != 1 {
		t.Fatalf("more than one expunge: %v", fx.fw.callList())
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if entries[0].Type != string(RemoveLabel) {
		t.Fatalf("recent type = %s", entries[0].Type)
	}
	if _, err := fx.ex.Undo(fx.ctx, entries[0].ID); err != nil {
		t.Fatalf("undo remove_label: %v", err)
	}
	if _, ok := fx.cachePlacements(t, "m1")["Labels/Test"]; !ok {
		t.Fatal("undo did not restore the label")
	}
}

func TestVerificationFallsBackToHeaderSearch(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	fx.fw.noCopyUID = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if fx.fw.callCount("COPY") != 1 {
		t.Fatalf("calls = %v", fx.fw.callList())
	}
}

func TestUnverifiedWriteIsFailedAndAudited(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	fx.fw.noCopyUID = true
	fx.fw.dropCopyPlacement = true

	res, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if res.Failed[0].Message == "" {
		t.Fatal("failure has no message")
	}
	if _, ok := fx.cachePlacements(t, "m1")["Labels/Test"]; ok {
		t.Fatal("unverified write was cached")
	}
	entries, _ := fx.ex.Recent(fx.ctx, 10)
	if len(entries) != 1 || entries[0].Verified || entries[0].Error == "" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestEventualSourceRemovalIsRetried(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	fx.fw.moveLag = 2

	res, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, ok := fx.fw.placement("m1", "INBOX"); ok {
		t.Fatal("source placement remains after retries")
	}
}

func TestBatchCapAndMissingMessages(t *testing.T) {
	fx := newFixture(t)
	many := make([]string, MaxBatch+1)
	for i := range many {
		many[i] = fmt.Sprintf("m%d", i)
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: many}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("101 unique ids: err = %v", err)
	}
	// Duplicates do not count twice.
	dup := make([]string, 0, 2*MaxBatch)
	for i := 0; i < 2*MaxBatch; i++ {
		dup = append(dup, "m1")
	}
	if err := Validate(Action{Type: MarkRead, MessageIDs: dup}); err != nil {
		t.Fatalf("duplicates should normalise: %v", err)
	}

	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	res, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1", "missing"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Failed) != 1 || res.Failed[0].MID != "missing" {
		t.Fatalf("result = %+v", res)
	}
}

func TestMoveTargetValidation(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: Move, Params: Params{Folder: "INBOX"}, MessageIDs: []string{"m1"}}); err == nil {
		t.Fatal("move to INBOX was accepted")
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: Move, Params: Params{Folder: "Labels/Test"}, MessageIDs: []string{"m1"}}); err == nil {
		t.Fatal("move to a label was accepted")
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Nope"}, MessageIDs: []string{"m1"}}); err == nil {
		t.Fatal("unknown label was accepted")
	}
}

func TestUndoLast(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX"}})
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: MarkRead, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: Star, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if res, err := fx.ex.UndoLast(fx.ctx); err != nil || len(res.Applied) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if fx.fw.flagsOf("m1", `\Flagged`) {
		t.Fatal("UndoLast did not undo star")
	}
	if !fx.fw.flagsOf("m1", `\Seen`) {
		t.Fatal("UndoLast undid more than the last action")
	}
}

func TestCountersStayConsistent(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "All Mail"}})
	if _, err := fx.ex.Apply(fx.ctx, Action{Type: Archive, MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	counts, err := fx.st.Counts(fx.ctx, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for _, c := range counts {
		byName[c.Mailbox] = c.Messages
	}
	if byName["INBOX"] != 0 || byName["Archive"] != 1 || byName["All Mail"] != 1 {
		t.Fatalf("counts = %v", byName)
	}
	states, _ := fx.st.GetSyncState(fx.ctx, testAccount)
	for _, s := range states {
		if s.Name == "INBOX" && s.Unseen != 0 {
			t.Fatalf("INBOX unseen = %d", s.Unseen)
		}
	}
}

func TestConcurrentAppliesAreSerialised(t *testing.T) {
	fx := newFixture(t)
	for i := 0; i < 10; i++ {
		fx.seed(t, seedMsg{mid: "m" + string(rune('a'+i)), mailboxes: []string{"INBOX"}})
	}
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			mid := "m" + string(rune('a'+i%10))
			_, err := fx.ex.Apply(fx.ctx, Action{Type: Star, MessageIDs: []string{mid}})
			errs <- err
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		mid := "m" + string(rune('a'+i))
		if !fx.fw.flagsOf(mid, `\Flagged`) {
			t.Fatalf("%s not flagged", mid)
		}
	}
}

func TestNoOpLabelHasNoInverse(t *testing.T) {
	fx := newFixture(t)
	fx.seed(t, seedMsg{mid: "m1", mailboxes: []string{"INBOX", "Labels/Test"}})
	res, err := fx.ex.Apply(fx.ctx, Action{Type: AddLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m1"}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if fx.fw.callCount("COPY") != 0 {
		t.Fatalf("no-op add_label wrote: %v", fx.fw.callList())
	}
	entries, _ := fx.ex.Recent(fx.ctx, 1)
	if entries[0].InverseJSON != "{}" {
		t.Fatalf("no-op inverse = %s", entries[0].InverseJSON)
	}
	if _, err := fx.ex.Undo(fx.ctx, entries[0].ID); !errors.Is(err, ErrNotUndoable) {
		t.Fatalf("undo no-op: %v", err)
	}
	if _, ok := fx.cachePlacements(t, "m1")["Labels/Test"]; !ok {
		t.Fatal("error: a no-op undo removed a pre-existing label")
	}

	fx2 := newFixture(t)
	fx2.seed(t, seedMsg{mid: "m2", mailboxes: []string{"INBOX"}})
	if _, err := fx2.ex.Apply(fx2.ctx, Action{Type: RemoveLabel, Params: Params{Label: "Test"}, MessageIDs: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if fx2.fw.callCount("EXPUNGE") != 0 {
		t.Fatalf("no-op remove_label expunged: %v", fx2.fw.callList())
	}
}

// TestNoExpungeOutsideLabelPath is a static guard: actions may call the
// expunging writer method in exactly one place, applyRemoveLabel. Archive, move,
// trash and spam must never expunge.
func TestNoExpungeOutsideLabelPath(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, needle := range []string{".ExpungeDeleted(", ".Expunge(", "UIDExpunge("} {
			n := strings.Count(text, needle)
			if n == 0 {
				continue
			}
			if file != "apply.go" || needle != ".ExpungeDeleted(" || n != 1 {
				t.Errorf("%s calls %s %d times; only applyRemoveLabel may expunge", file, needle, n)
			}
			found += n
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one expunge call, found %d", found)
	}
}
