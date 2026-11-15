package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/model"
)

const acct = "acc1"

var t0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func openTest(t *testing.T) Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustUpsertBoxes(t *testing.T, s Store, boxes ...model.Mailbox) {
	t.Helper()
	if err := s.UpsertMailboxes(context.Background(), acct, boxes); err != nil {
		t.Fatalf("UpsertMailboxes: %v", err)
	}
}

func header(mb string, validity, uid uint32, date time.Time) model.MessageHeader {
	return model.MessageHeader{
		Mailbox:     mb,
		UIDValidity: validity,
		UID:         uid,
		MessageID:   "<m" + string(rune('a'+uid%26)) + "@x>",
		Subject:     "subject",
		From:        []model.Address{{Name: "A", Email: "a@example.org"}},
		To:          []model.Address{{Email: "b@example.org"}},
		Date:        date,
		Flags:       []string{`\Seen`},
		InReplyTo:   "parent@x",
		References:  "<parent@x>",
	}
}

func TestOpenCreatesPrivateFileAndSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "temail")
	path := filepath.Join(dir, "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("cache file perm = %o, want 600", perm)
		}
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := di.Mode().Perm(); perm != 0o700 {
			t.Errorf("cache dir perm = %o, want 700", perm)
		}
	}
	impl := s.(*sqliteStore)
	var v int
	if err := impl.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Errorf("user_version = %d, want %d", v, SchemaVersion)
	}
	var mode string
	if err := impl.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %s, want wal", mode)
	}
	var fk int
	if err := impl.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
}

func TestOpenIsIdempotentAndRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path) // reopen: migrations must not run again
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st, err := s.GetSyncState(context.Background(), acct)
	if err != nil || len(st) != 1 {
		t.Fatalf("data lost on reopen: %v %v", st, err)
	}
	impl := s.(*sqliteStore)
	if _, err := impl.db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a newer schema version")
	}
}

func TestForeignKeysRejectOrphanMessages(t *testing.T) {
	s := openTest(t)
	err := s.UpsertHeaders(context.Background(), acct, []model.MessageHeader{header("Nope", 1, 1, t0)})
	if err == nil {
		t.Fatal("header for an uncached mailbox was stored")
	}
	impl := s.(*sqliteStore)
	_, err = impl.db.Exec(`INSERT INTO messages (account, mailbox, uidvalidity, uid) VALUES ('a','b',1,1)`)
	if err == nil {
		t.Fatal("foreign key not enforced on messages")
	}
}

func TestHeadersRoundTripAndListNewestFirst(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 7})
	var hs []model.MessageHeader
	for uid := uint32(1); uid <= 5; uid++ {
		hs = append(hs, header("INBOX", 7, uid, t0.Add(time.Duration(uid)*time.Hour)))
	}
	// Flags are stored normalised: sorted and deduplicated.
	hs[2].Flags = []string{`\Flagged`, `\Seen`, `\Seen`}
	if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetMessage(ctx, MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 7, UID: 3})
	if err != nil {
		t.Fatal(err)
	}
	want := hs[2]
	want.Flags = []string{`\Flagged`, `\Seen`}
	want.Date = want.Date.UTC()
	// A header without an InternalID gets a synthetic, non-canonical id.
	want.InternalID = syntheticMID("INBOX", 7, 3)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetMessage =\n%+v\nwant\n%+v", got, want)
	}

	page, err := s.ListMessages(ctx, acct, "INBOX", ListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].UID != 5 || page[1].UID != 4 {
		t.Fatalf("first page UIDs = %v", uids(page))
	}
	next, err := s.ListMessages(ctx, acct, "INBOX", ListOptions{Limit: 2, Before: &Cursor{Date: page[1].Date, UID: page[1].UID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[0].UID != 3 || next[1].UID != 2 {
		t.Fatalf("second page UIDs = %v", uids(next))
	}
	if _, err := s.ListMessages(ctx, acct, "INBOX", ListOptions{Limit: 0}); err == nil {
		t.Error("limit 0 accepted")
	}
}

func TestListCursorBreaksDateTiesByUID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	var hs []model.MessageHeader
	for uid := uint32(1); uid <= 4; uid++ {
		hs = append(hs, header("INBOX", 1, uid, t0)) // same date
	}
	if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
		t.Fatal(err)
	}
	p1, _ := s.ListMessages(ctx, acct, "INBOX", ListOptions{Limit: 2})
	p2, _ := s.ListMessages(ctx, acct, "INBOX", ListOptions{Limit: 2, Before: &Cursor{Date: p1[1].Date, UID: p1[1].UID}})
	seen := append(uids(p1), uids(p2)...)
	if !reflect.DeepEqual(seen, []uint32{4, 3, 2, 1}) {
		t.Errorf("paged UIDs = %v, want [4 3 2 1] with no gaps or repeats", seen)
	}
}

func TestUpsertHeadersKeepsBodyStateAndRejectsStaleEpoch(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 3})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 3, 1, t0)}); err != nil {
		t.Fatal(err)
	}
	key := MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 3, UID: 1}
	if err := s.SetBody(ctx, key, Body{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	h := header("INBOX", 3, 1, t0)
	h.Subject = "changed"
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{h}); err != nil {
		t.Fatal(err)
	}
	pending, _ := pendingKeys(ctx, s, 10)
	if len(pending) != 0 {
		t.Errorf("header refresh reset body state: pending %v", pending)
	}
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 2, 9, t0)}); err == nil {
		t.Error("header with stale UIDVALIDITY was stored")
	}
}

func TestUIDValidityChangePurgesMailbox(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 1, t0), header("INBOX", 1, 2, t0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBody(ctx, MessageKey{acct, "INBOX", 1, 1}, Body{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSynced(ctx, acct, "INBOX", t0); err != nil {
		t.Fatal(err)
	}

	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2})
	st, _ := s.GetSyncState(ctx, acct)
	if st[0].UIDValidity != 2 || st[0].HighestUID != 0 || !st[0].LastSync.IsZero() {
		t.Errorf("state after epoch change = %+v", st[0])
	}
	if cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 1); len(cached) != 0 {
		t.Errorf("old epoch UIDs survived: %v", cached)
	}
	if _, err := s.GetBody(ctx, MessageKey{acct, "INBOX", 1, 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("body of old epoch survived: %v", err)
	}
	// Same validity again: nothing is purged.
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 2, 5, t0)}); err != nil {
		t.Fatal(err)
	}
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2})
	if cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 2); len(cached) != 1 {
		t.Errorf("unchanged epoch was purged: %v", cached)
	}
}

func TestPurgeMailboxCascadesToBodies(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 1, t0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBody(ctx, MessageKey{acct, "INBOX", 1, 1}, Body{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeMailbox(ctx, acct, "INBOX"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.(*sqliteStore).db.QueryRow(`SELECT COUNT(*) FROM bodies`).Scan(&n); err != nil || n != 0 {
		t.Errorf("bodies left after purge: %d (%v)", n, err)
	}
	st, _ := s.GetSyncState(ctx, acct)
	if len(st) != 1 || st[0].UIDValidity != 1 {
		t.Errorf("purge should keep the mailbox row and its UIDVALIDITY: %+v", st)
	}
}

func TestHighestUIDTracksNewestCachedUID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 40, t0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 7, t0)}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetSyncState(ctx, acct)
	if st[0].HighestUID != 40 {
		t.Errorf("HighestUID = %d, want 40", st[0].HighestUID)
	}
}

func TestDeleteMissingAndUpdateFlags(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	var hs []model.MessageHeader
	for uid := uint32(1); uid <= 5; uid++ {
		hs = append(hs, header("INBOX", 1, uid, t0))
	}
	if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateFlags(ctx, acct, "INBOX", 1, []FlagUpdate{{UID: 2, Flags: []string{`\Flagged`}}, {UID: 99, Flags: nil}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetMessage(ctx, MessageKey{acct, "INBOX", 1, 2})
	if !reflect.DeepEqual(got.Flags, []string{`\Flagged`}) {
		t.Errorf("flags after update = %v", got.Flags)
	}
	n, err := s.DeleteMissing(ctx, acct, "INBOX", 1, []uint32{1, 3, 5, 77})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("DeleteMissing removed %d, want 2", n)
	}
	cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 1)
	if !reflect.DeepEqual(cached, []uint32{1, 3, 5}) {
		t.Errorf("cached after DeleteMissing = %v", cached)
	}
	// A keep set larger than the SQLite parameter limit still works.
	big := make([]uint32, 40000)
	for i := range big {
		big[i] = uint32(i + 1)
	}
	if _, err := s.DeleteMissing(ctx, acct, "INBOX", 1, big); err != nil {
		t.Errorf("DeleteMissing with large keep set: %v", err)
	}
}

func TestBodiesGzipRoundTripAndPendingOrder(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1},
		model.Mailbox{Name: "Folders/Work", Kind: model.KindFolder, DisplayName: "Work", UIDValidity: 4})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		header("INBOX", 1, 1, t0.Add(1*time.Hour)),
		header("INBOX", 1, 2, t0.Add(3*time.Hour)),
		header("Folders/Work", 4, 1, t0.Add(2*time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}

	pending, err := pendingKeys(ctx, s, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []MessageKey{
		{acct, "INBOX", 1, 2}, {acct, "Folders/Work", 4, 1}, {acct, "INBOX", 1, 1},
	}
	if !reflect.DeepEqual(pending, want) {
		t.Errorf("PendingBodies = %v, want newest first %v", pending, want)
	}

	body := Body{
		Text: "plain text ü",
		HTML: "<p>html</p>",
		Attachments: []model.Attachment{
			{Filename: "a.pdf", ContentType: "application/pdf", Size: 1234, PartID: "2"},
		},
	}
	if err := s.SetBody(ctx, want[0], body); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBody(ctx, want[0])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, body) {
		t.Errorf("GetBody = %+v, want %+v", got, body)
	}
	// Attachment content is not in the cache: only metadata columns exist.
	var raw string
	if err := s.(*sqliteStore).db.QueryRow(`SELECT attachments_meta_json FROM bodies`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw == "" || raw[0] != '[' {
		t.Errorf("attachments_meta_json = %q", raw)
	}

	pending, _ = pendingKeys(ctx, s, 10)
	if len(pending) != 2 || pending[0] != want[1] {
		t.Errorf("pending after one body = %v", pending)
	}
	if _, err := s.GetBody(ctx, want[2]); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBody without body = %v, want ErrNotFound", err)
	}
	if err := s.SetBody(ctx, MessageKey{acct, "INBOX", 1, 99}, Body{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetBody for unknown message = %v", err)
	}
}

func TestMarkBodyFailedIsBounded(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 1, t0)}); err != nil {
		t.Fatal(err)
	}
	key := MessageKey{acct, "INBOX", 1, 1}
	for i := 0; i < MaxBodyAttempts; i++ {
		if p, _ := pendingKeys(ctx, s, 5); len(p) != 1 {
			t.Fatalf("attempt %d: message not pending", i)
		}
		if err := s.MarkBodyFailed(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := pendingKeys(ctx, s, 5); len(p) != 0 {
		t.Errorf("message still pending after %d failures", MaxBodyAttempts)
	}
}

func TestCountsAndSyncStateAndMailboxesNotIn(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, DisplayName: "INBOX", UIDValidity: 1},
		model.Mailbox{Name: "Archive", Kind: model.KindArchive, DisplayName: "Archive", UIDValidity: 2},
		model.Mailbox{Name: "Gone", Kind: model.KindFolder, UIDValidity: 3})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 1, 1, t0), header("INBOX", 1, 2, t0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBody(ctx, MessageKey{acct, "INBOX", 1, 1}, Body{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Counts(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	want := []MailboxCounts{{"Archive", 0, 0}, {"Gone", 0, 0}, {"INBOX", 2, 1}}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("Counts = %v, want %v", counts, want)
	}

	if err := s.DeleteMailboxesNotIn(ctx, acct, []string{"INBOX", "Archive"}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetSyncState(ctx, acct)
	if len(st) != 2 {
		t.Errorf("mailboxes after DeleteMailboxesNotIn = %+v", st)
	}
	// An empty listing must not wipe the account's mailboxes.
	if err := s.DeleteMailboxesNotIn(ctx, acct, nil); !errors.Is(err, ErrEmptyListing) {
		t.Fatalf("empty listing: err = %v, want ErrEmptyListing", err)
	}
	st, _ = s.GetSyncState(ctx, acct)
	if len(st) != 2 {
		t.Errorf("empty listing removed mailboxes: %+v", st)
	}
	// Dropping INBOX from the listing removes its messages too.
	if err := s.DeleteMailboxesNotIn(ctx, acct, []string{"Archive"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.(*sqliteStore).db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n); err != nil || n != 0 {
		t.Errorf("messages left after mailbox delete: %d (%v)", n, err)
	}
}

func TestMarkSyncedUnknownMailbox(t *testing.T) {
	s := openTest(t)
	if err := s.MarkSynced(context.Background(), acct, "nope", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkSynced unknown = %v", err)
	}
}

func TestDefaultPathLinuxXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux path rules")
	}
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(xdg, "temail", "cache.db"); got != want {
		t.Errorf("DefaultPath = %s, want %s", got, want)
	}
	t.Setenv("XDG_DATA_HOME", "relative/dir")
	got, err = DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".local", "share", "temail", "cache.db"); got != want {
		t.Errorf("relative XDG_DATA_HOME: DefaultPath = %s, want %s", got, want)
	}
}

// pendingKeys returns the keys of the first page of PendingBodies.
func pendingKeys(ctx context.Context, s Store, limit int) ([]MessageKey, error) {
	page, err := s.PendingBodies(ctx, acct, nil, limit)
	if err != nil {
		return nil, err
	}
	keys := make([]MessageKey, 0, len(page))
	for _, p := range page {
		keys = append(keys, p.Key)
	}
	return keys, nil
}

func uids(hs []model.MessageHeader) []uint32 {
	out := make([]uint32, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.UID)
	}
	return out
}
