package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MaksimZadar/temail/internal/model"
)

// Paths with URI-special characters must open the same file, not a different
// one (the DSN is built with net/url).
func TestOpenPathWithSpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "we?ird#dir %20 space")
	path := filepath.Join(dir, "cache.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, _ := s.GetSyncState(context.Background(), acct); len(st) != 1 {
		t.Errorf("data not found after reopening special-character path: %v", st)
	}
	var mode string
	if err := s.(*sqliteStore).db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("pragmas not applied on special-character path: mode=%q err=%v", mode, err)
	}
}

// Two processes opening a new cache at once must both succeed: the version is
// re-read under the write lock before each migration.
func TestConcurrentOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	stores := make([]Store, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stores[i], errs[i] = Open(path)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Open %d: %v", i, err)
		}
		if stores[i] != nil {
			_ = stores[i].Close()
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.(*sqliteStore).db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Errorf("user_version = %d (%v), want %d", v, err, SchemaVersion)
	}
}

// A cache created at schema v1 upgrades to the current version, and the body
// queue index is rebuilt for the cursor order.
func TestUpgradeFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	raw, err := sql.Open("sqlite", mustDSN(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO mailboxes (account, name, kind, display_name, uidvalidity) VALUES ('acc1','INBOX','inbox','INBOX',1)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade from v1: %v", err)
	}
	defer s.Close()
	st, _ := s.GetSyncState(context.Background(), acct)
	if len(st) != 1 {
		t.Errorf("v1 data lost on upgrade: %v", st)
	}
	var idx string
	if err := s.(*sqliteStore).db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'messages_body_pending'`).Scan(&idx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(idx, "mid DESC") {
		t.Errorf("body index not upgraded: %s", idx)
	}
}

func mustDSN(t *testing.T, path string) string {
	t.Helper()
	dsn, err := sqliteDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

// UIDVALIDITY 0 is unknown: it neither purges the cache nor overwrites the
// stored value.
func TestZeroUIDValidityIsUnknown(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 9})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{header("INBOX", 9, 1, t0)}); err != nil {
		t.Fatal(err)
	}
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 0})
	st, _ := s.GetSyncState(ctx, acct)
	if st[0].UIDValidity != 9 {
		t.Errorf("zero UIDVALIDITY overwrote stored value: %d", st[0].UIDValidity)
	}
	if cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 9); len(cached) != 1 {
		t.Errorf("zero UIDVALIDITY purged the cache: %v", cached)
	}
}

// Large purges and deletes run in several transactions and still remove
// everything they should.
func TestChunkedPurgeAndDelete(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	const n = 2500
	hs := make([]model.MessageHeader, 0, n)
	for uid := uint32(1); uid <= n; uid++ {
		hs = append(hs, header("INBOX", 1, uid, t0))
	}
	if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
		t.Fatal(err)
	}
	keep := []uint32{}
	for uid := uint32(1); uid <= n; uid += 2 {
		keep = append(keep, uid)
	}
	deleted, err := s.DeleteMissing(ctx, acct, "INBOX", 1, keep)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != n/2 {
		t.Errorf("DeleteMissing removed %d, want %d", deleted, n/2)
	}
	if cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 1); len(cached) != n/2 {
		t.Errorf("cached after chunked delete = %d", len(cached))
	}
	if err := s.PurgeMailbox(ctx, acct, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if cached, _ := s.CachedUIDs(ctx, acct, "INBOX", 1); len(cached) != 0 {
		t.Errorf("cached after chunked purge = %d", len(cached))
	}
}

func TestCachedFlagsAndNormalize(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	h := header("INBOX", 1, 1, t0)
	h.Flags = []string{`\Seen`, `\Flagged`, `\Seen`, ""}
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{h}); err != nil {
		t.Fatal(err)
	}
	got, err := s.CachedFlags(ctx, acct, "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32][]string{1: {`\Flagged`, `\Seen`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CachedFlags = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(NormalizeFlags([]string{`\Seen`, `\Seen`, ""}), []string{`\Seen`}) {
		t.Error("NormalizeFlags did not dedupe or drop empty flags")
	}
}

// Paging with the cursor visits each pending message once and in order, even
// when some messages stay pending.
func TestPendingBodiesCursorPagesOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	var hs []model.MessageHeader
	for uid := uint32(1); uid <= 5; uid++ {
		hs = append(hs, header("INBOX", 1, uid, t0)) // same date: UID breaks ties
	}
	if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
		t.Fatal(err)
	}
	var seen []uint32
	var cur *PendingCursor
	for {
		page, err := s.PendingBodies(ctx, acct, cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, p := range page {
			seen = append(seen, p.Key.UID)
		}
		last := page[len(page)-1]
		cur = &PendingCursor{Date: last.Date, MID: last.MID}
	}
	if !reflect.DeepEqual(seen, []uint32{5, 4, 3, 2, 1}) {
		t.Errorf("cursor paging visited %v", seen)
	}
}

// Only SQLITE_BUSY and SQLITE_LOCKED are retried. Any other error fails at once.
func TestRetryBusyDoesNotRetryOtherErrors(t *testing.T) {
	calls := 0
	boom := errors.New("disk I/O error")
	err := retryBusy(context.Background(), func() error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err = %v after %d calls; want the error after 1 call", err, calls)
	}
}
