package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/model"
)

// queryPlan returns the EXPLAIN QUERY PLAN lines of q.
func queryPlan(t *testing.T, s Store, q string, args ...any) []string {
	t.Helper()
	rows, err := s.(*sqliteStore).db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return out
}

// A mailbox page must come from the placement index in date order, not from a
// scan and sort of every placement in the mailbox.
func TestListPageUsesPlacementIndex(t *testing.T) {
	s := openTest(t)
	mustUpsertBoxes(t, s, model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1})
	q, args, err := listMessagesQuery(acct, "All Mail", ListOptions{Limit: 50, Before: &Cursor{Date: t0, UID: 10}})
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(queryPlan(t, s, q, args...), "\n")
	if !strings.Contains(plan, "placements_list") {
		t.Fatalf("page query does not use placements_list:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("page query sorts in a temp b-tree:\n%s", plan)
	}
	// The cursor must be a seek on the index, not a filter after a scan.
	if !strings.Contains(plan, "(date,uid)<(?,?)") {
		t.Fatalf("cursor is not a index seek:\n%s", plan)
	}
}

// bigMailbox fills one mailbox with n placements, in chunks, and returns the
// store. Each message has its own id, so there are n entities too.
func bigMailbox(t *testing.T, n int) Store {
	t.Helper()
	s := openTest(t)
	mustUpsertBoxes(t, s, model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1})
	ctx := context.Background()
	const chunk = 1000
	for start := 0; start < n; start += chunk {
		var hs []model.MessageHeader
		for i := start; i < min(start+chunk, n); i++ {
			uid := uint32(i + 1)
			hs = append(hs, member("All Mail", 1, uid, fmt.Sprintf("pm-%07d", uid), t0.Add(time.Duration(uid)*time.Second)))
		}
		if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// 100k placements: the newest page and a deep page both come from the index,
// and the page query takes a bounded time. The timings are logged so the
// numbers can be quoted.
func TestLargeMailboxPageIsFast(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("100k placements: skipped in -short and under -race (about 80 s there)")
	}
	const n = 100_000
	start := time.Now()
	s := bigMailbox(t, n)
	t.Logf("inserted %d placements in %v", n, time.Since(start).Round(time.Millisecond))

	ctx := context.Background()
	first := time.Now()
	page, err := s.ListMessages(ctx, acct, "All Mail", ListOptions{Limit: 50})
	if err != nil || len(page) != 50 {
		t.Fatalf("first page: %d rows, %v", len(page), err)
	}
	t.Logf("first page (50 of %d): %v", n, time.Since(first).Round(time.Microsecond))

	last := page[len(page)-1]
	deep := time.Now()
	deepPage, err := s.ListMessages(ctx, acct, "All Mail", ListOptions{Limit: 50, Before: &Cursor{Date: last.Date, UID: last.UID}})
	if err != nil || len(deepPage) != 50 {
		t.Fatalf("second page: %d rows, %v", len(deepPage), err)
	}
	t.Logf("second page after cursor: %v", time.Since(deep).Round(time.Microsecond))

	q, args, _ := listMessagesQuery(acct, "All Mail", ListOptions{Limit: 50, Before: &Cursor{Date: last.Date, UID: last.UID}})
	if plan := strings.Join(queryPlan(t, s, q, args...), "\n"); !strings.Contains(plan, "(date,uid)<(?,?)") {
		t.Fatalf("deep page not indexed:\n%s", plan)
	}
	// A page must be fast even at 100k. The bound is loose enough for a loaded
	// CI runner and tight enough to catch a scan-and-sort.
	if d := time.Since(first); d > 2*time.Second {
		t.Fatalf("first page took %v", d)
	}
}

// BenchmarkListPage measures one 50-row page at the newest end and at depth.
// Run with: go test -run=^$ -bench=ListPage ./internal/store
func BenchmarkListPage(b *testing.B) {
	s := benchStore(b, 100_000)
	ctx := context.Background()
	b.Run("newest", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.ListMessages(ctx, acct, "All Mail", ListOptions{Limit: 50}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("deep", func(b *testing.B) {
		c := &Cursor{Date: t0.Add(50_000 * time.Second), UID: 50_000}
		for i := 0; i < b.N; i++ {
			if _, err := s.ListMessages(ctx, acct, "All Mail", ListOptions{Limit: 50, Before: c}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// benchStore builds the large store once per benchmark run.
func benchStore(b *testing.B, n int) Store {
	b.Helper()
	st, err := Open(b.TempDir() + "/cache.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.UpsertMailboxes(ctx, acct, []model.Mailbox{{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1}}); err != nil {
		b.Fatal(err)
	}
	for start := 0; start < n; start += 1000 {
		var hs []model.MessageHeader
		for i := start; i < min(start+1000, n); i++ {
			uid := uint32(i + 1)
			hs = append(hs, member("All Mail", 1, uid, fmt.Sprintf("pm-%07d", uid), t0.Add(time.Duration(uid)*time.Second)))
		}
		if err := st.UpsertHeaders(ctx, acct, hs); err != nil {
			b.Fatal(err)
		}
	}
	return st
}

// B: a placement re-pointed to another message garbage-collects the old one in
// the same transaction. A synthetic id that becomes canonical is the common case.
func TestRepointGarbageCollectsSyntheticMessage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2})

	synthetic := header("INBOX", 2, 7, t0) // no identity: synthetic id
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{synthetic}); err != nil {
		t.Fatal(err)
	}
	key := MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 2, UID: 7}
	if err := s.SetBody(ctx, key, Body{Text: "old"}); err != nil {
		t.Fatal(err)
	}

	canonical := member("INBOX", 2, 7, "pm-real", t0)
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{canonical}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 1 {
		t.Fatalf("messages = %d, want 1 (the synthetic entity removed)", st.Messages)
	}
	var orphanBodies int
	_ = s.(*sqliteStore).db.QueryRow(`SELECT COUNT(*) FROM bodies WHERE mid LIKE 'mb:%'`).Scan(&orphanBodies)
	if orphanBodies != 0 {
		t.Fatalf("the synthetic message's body survived: %d", orphanBodies)
	}
	h, err := s.GetMessage(ctx, key)
	if err != nil || h.InternalID != "pm-real" || !h.Canonical {
		t.Fatalf("placement now = %+v, %v", h, err)
	}
	assertCountersMatch(t, s, "INBOX")
}

// C: a body or a re-point of a message that still has another placement keeps
// the message and only moves the placement.
func TestRepointKeepsMessageWithOtherPlacements(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{member("All Mail", 1, 1, "pm-a", t0), member("INBOX", 2, 1, "pm-a", t0)})
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{member("INBOX", 2, 1, "pm-b", t0)})
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 2 {
		t.Fatalf("messages = %d, want pm-a kept (All Mail) and pm-b added", st.Messages)
	}
	assertCountersMatch(t, s, "INBOX")
	assertCountersMatch(t, s, "All Mail")
}

// C: the counters match the base tables after every kind of change. The check
// recomputes the counts from placements and messages and compares them.
func TestCountersStayConsistent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	check := func(step string) {
		t.Helper()
		for _, mb := range []string{"All Mail", "INBOX", "Labels/A"} {
			if err := countersErr(s, mb); err != nil {
				t.Fatalf("after %s: %v", step, err)
			}
		}
		var orphans int
		if err := s.(*sqliteStore).db.QueryRow(`
			SELECT COUNT(*) FROM messages m WHERE NOT EXISTS
				(SELECT 1 FROM placements p WHERE p.account = m.account AND p.mid = m.mid)`).Scan(&orphans); err != nil {
			t.Fatal(err)
		}
		if orphans != 0 {
			t.Fatalf("after %s: %d messages with no placement", step, orphans)
		}
	}

	h := func(mb string, v, uid uint32, id string, flags ...string) model.MessageHeader {
		m := member(mb, v, uid, id, t0.Add(time.Duration(uid)*time.Minute))
		m.Flags = flags
		return m
	}
	mustUpsert := func(hs ...model.MessageHeader) {
		t.Helper()
		if err := s.UpsertHeaders(ctx, acct, hs); err != nil {
			t.Fatal(err)
		}
	}
	mustUpsert(h("All Mail", 1, 1, "pm-1", `\Seen`), h("INBOX", 2, 1, "pm-1", `\Seen`), h("Labels/A", 3, 1, "pm-1", `\Seen`),
		h("INBOX", 2, 2, "pm-2"), h("INBOX", 2, 3, "pm-3", `\Flagged`))
	check("insert")

	if err := s.SetBody(ctx, MessageKey{Account: acct, Mailbox: "Labels/A", UIDValidity: 3, UID: 1}, Body{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	check("body set through a label")

	if err := s.UpdateFlags(ctx, acct, "INBOX", 2, []FlagUpdate{{UID: 2, Flags: []string{`\Seen`}}}); err != nil {
		t.Fatal(err)
	}
	check("pm-2 marked seen")
	if err := s.UpdateFlags(ctx, acct, "All Mail", 1, []FlagUpdate{{UID: 1, Flags: nil}}); err != nil {
		t.Fatal(err)
	}
	check("pm-1 marked unseen from All Mail")

	mustUpsert(h("INBOX", 2, 2, "pm-3", `\Flagged`)) // pm-2 re-pointed to pm-3 at UID 2
	check("re-point INBOX UID 2 from pm-2 to pm-3")

	if _, err := s.DeleteMissing(ctx, acct, "INBOX", 2, []uint32{1}); err != nil {
		t.Fatal(err)
	}
	check("delete INBOX UIDs not kept")

	mustUpsert(h("INBOX", 2, 5, "pm-5"), h("INBOX", 2, 6, "pm-6", `\Seen`))
	check("re-add INBOX messages")

	if err := s.PurgeMailbox(ctx, acct, "Labels/A"); err != nil {
		t.Fatal(err)
	}
	check("purge a label")

	if err := s.UpsertMailboxes(ctx, acct, []model.Mailbox{{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 9}}); err != nil {
		t.Fatal(err)
	}
	check("UIDVALIDITY change of INBOX")
}

// countersErr compares the mailbox counters with a recount of the base tables.
// The unseen count is recounted from the message flags.
func countersErr(s Store, mailbox string) error {
	db := s.(*sqliteStore).db
	var pc, bc, uc int
	if err := db.QueryRow(`SELECT COUNT(*) FROM placements WHERE account = ? AND mailbox = ?`, acct, mailbox).Scan(&pc); err != nil {
		return err
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM placements p JOIN messages m ON m.account = p.account AND m.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ? AND m.body_state = 'cached'`, acct, mailbox).Scan(&bc); err != nil {
		return err
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM placements p JOIN messages m ON m.account = p.account AND m.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ? AND instr(m.flags, '\\Seen') = 0`, acct, mailbox).Scan(&uc); err != nil {
		return err
	}
	var gp, gb, gu int
	if err := db.QueryRow(`SELECT placements_count, bodies_count, unseen FROM mailboxes WHERE account = ? AND name = ?`,
		acct, mailbox).Scan(&gp, &gb, &gu); err != nil {
		return err
	}
	if pc != gp || bc != gb || uc != gu {
		return fmt.Errorf("%s: counters placements=%d bodies=%d unseen=%d, recount placements=%d bodies=%d unseen=%d",
			mailbox, gp, gb, gu, pc, bc, uc)
	}
	return nil
}

func assertCountersMatch(t *testing.T, s Store, mailbox string) {
	t.Helper()
	if err := countersErr(s, mailbox); err != nil {
		t.Fatal(err)
	}
}

// C: the batched membership returns the same placements as the per-key lookup,
// and messages without placements are absent.
func TestMembershipOfMessagesBatch(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 1, "pm-1", t0), member("INBOX", 2, 1, "pm-1", t0), member("Labels/A", 3, 1, "pm-1", t0),
		member("INBOX", 2, 2, "pm-2", t0),
	})
	got, err := s.MembershipOfMessages(ctx, acct, []string{"pm-1", "pm-2", "pm-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got["pm-1"]) != 3 || len(got["pm-2"]) != 1 {
		t.Fatalf("batch = %v", got)
	}
	if _, ok := got["pm-missing"]; ok {
		t.Fatal("unknown message must be absent")
	}
	single, err := s.MembershipOf(ctx, MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 2, UID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(single, got["pm-1"]) {
		t.Fatalf("batch and single lookup disagree:\n%v\n%v", got["pm-1"], single)
	}
	if empty, err := s.MembershipOfMessages(ctx, acct, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty batch = %v, %v", empty, err)
	}
}

// D: the server's UIDNEXT is stored with the sync and read back.
func TestMarkSyncedStoresUIDNext(t *testing.T) {
	s := openTest(t)
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2})
	if err := s.MarkSynced(context.Background(), acct, "INBOX", t0, 41); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetSyncState(context.Background(), acct)
	if len(st) != 1 || st[0].UIDNext != 41 {
		t.Fatalf("uidnext = %+v, want 41", st)
	}
}
