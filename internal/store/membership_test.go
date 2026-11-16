package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/model"
)

// member returns a header of one copy of a message: same Proton id, a
// mailbox-specific UID.
func member(mb string, validity, uid uint32, internalID string, date time.Time) model.MessageHeader {
	h := header(mb, validity, uid, date)
	h.InternalID = internalID
	h.Canonical = true
	h.MessageID = "<" + internalID + "@protonmail.internalid>"
	return h
}

func TestOneEntityManyPlacements(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	date := t0.Add(time.Hour)
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 90, "pm-1", date),
		member("INBOX", 2, 7, "pm-1", date),
		member("Labels/A", 3, 12, "pm-1", date),
	}); err != nil {
		t.Fatal(err)
	}

	st, err := s.CacheStats(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 1 {
		t.Fatalf("messages = %d, want one entity for three placements", st.Messages)
	}

	// The same entity is listed in each mailbox it is in.
	for _, mb := range []string{"All Mail", "INBOX", "Labels/A"} {
		page, err := s.ListMessages(ctx, acct, mb, ListOptions{Limit: 10})
		if err != nil || len(page) != 1 || page[0].InternalID != "pm-1" {
			t.Fatalf("%s: page=%v err=%v", mb, page, err)
		}
	}

	members, err := s.MembershipOf(ctx, MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 2, UID: 7})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range members {
		names = append(names, m.Mailbox)
	}
	if !reflect.DeepEqual(names, []string{"All Mail", "INBOX", "Labels/A"}) {
		t.Fatalf("membership = %v", names)
	}
}

func TestBodyIsStoredOncePerMessage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	date := t0
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("INBOX", 2, 7, "pm-2", date),
		member("Labels/A", 3, 9, "pm-2", date),
	})
	if err := s.SetBody(ctx, MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 2, UID: 7}, Body{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	// Reading the body through the other placement gives the same body.
	b, err := s.GetBody(ctx, MessageKey{Account: acct, Mailbox: "Labels/A", UIDValidity: 3, UID: 9})
	if err != nil || b.Text != "hello" {
		t.Fatalf("body via second placement: %q, %v", b.Text, err)
	}
	counts, _ := s.Counts(ctx, acct)
	for _, c := range counts {
		if c.Bodies != 1 {
			t.Fatalf("%s: bodies = %d, want 1 (the one shared body)", c.Mailbox, c.Bodies)
		}
	}
	st, _ := s.CacheStats(ctx, acct)
	if st.Bodies != 1 {
		t.Fatalf("CacheStats bodies = %d, want 1", st.Bodies)
	}
	// A body is queued once for the message, not once per placement.
	pending, err := s.PendingBodies(ctx, acct, nil, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after body set = %v, %v", pending, err)
	}
}

func TestPendingBodiesOffersEachMessageOnce(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 5, "pm-x", t0),
		member("INBOX", 2, 3, "pm-x", t0),
		member("INBOX", 2, 4, "pm-y", t0.Add(time.Minute)),
	})
	pending, err := s.PendingBodies(ctx, acct, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d entities, want 2", len(pending))
	}
	// pm-x is fetched through All Mail, the placement that holds every message.
	for _, p := range pending {
		if p.MID == "pm-x" && (p.Key.Mailbox != "All Mail" || p.Key.UID != 5) {
			t.Fatalf("pm-x fetched via %+v, want All Mail", p.Key)
		}
	}
}

func TestDeletingOnePlacementKeepsTheMessage(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 5, "pm-z", t0),
		member("INBOX", 2, 3, "pm-z", t0),
	})
	_ = s.SetBody(ctx, MessageKey{Account: acct, Mailbox: "All Mail", UIDValidity: 1, UID: 5}, Body{Text: "keep me"})

	// INBOX no longer has UID 3; All Mail still does.
	if n, err := s.DeleteMissing(ctx, acct, "INBOX", 2, nil); err != nil || n != 1 {
		t.Fatalf("DeleteMissing = %d, %v", n, err)
	}
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 1 || st.Bodies != 1 {
		t.Fatalf("after deleting one placement: %+v, want the message and its body kept", st)
	}
	if _, err := s.GetBody(ctx, MessageKey{Account: acct, Mailbox: "All Mail", UIDValidity: 1, UID: 5}); err != nil {
		t.Fatalf("body lost with a placement: %v", err)
	}
}

func TestLastPlacementGarbageCollectsMessageAndBody(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3})
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{member("Labels/A", 3, 9, "pm-gc", t0)})
	_ = s.SetBody(ctx, MessageKey{Account: acct, Mailbox: "Labels/A", UIDValidity: 3, UID: 9}, Body{Text: "x"})

	if n, err := s.DeleteMissing(ctx, acct, "Labels/A", 3, nil); err != nil || n != 1 {
		t.Fatalf("DeleteMissing = %d, %v", n, err)
	}
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 0 || st.Bodies != 0 {
		t.Fatalf("orphan entity or body survived: %+v", st)
	}
}

func TestPurgeMailboxKeepsSharedMessages(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("INBOX", 2, 1, "shared", t0),
		member("Labels/A", 3, 1, "shared", t0),
		member("INBOX", 2, 2, "only-inbox", t0),
	})
	if err := s.PurgeMailbox(ctx, acct, "INBOX"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 1 {
		t.Fatalf("messages after purging INBOX = %d, want only the shared one", st.Messages)
	}
	counts, _ := s.Counts(ctx, acct)
	for _, c := range counts {
		if c.Mailbox == "Labels/A" && c.Messages != 1 {
			t.Fatalf("Labels/A lost its placement: %+v", c)
		}
	}
}

func TestUnseenIsStoredPerMailbox(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Labels/A", Kind: model.KindLabel, UIDValidity: 3},
	)
	if err := s.SetUnseen(ctx, acct, "INBOX", 4); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUnseen(ctx, acct, "Labels/A", 1); err != nil {
		t.Fatal(err)
	}
	st, err := s.GetSyncState(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint32{}
	for _, m := range st {
		got[m.Name] = m.Unseen
	}
	if got["INBOX"] != 4 || got["Labels/A"] != 1 {
		t.Fatalf("unseen = %v", got)
	}
	if err := s.SetUnseen(ctx, acct, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown mailbox: %v", err)
	}
}

func TestFlagsAreGlobalAcrossPlacements(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 5, "pm-f", t0),
		member("INBOX", 2, 3, "pm-f", t0),
	})
	// The change comes from the INBOX placement; the All Mail copy sees it.
	if err := s.UpdateFlags(ctx, acct, "INBOX", 2, []FlagUpdate{{UID: 3, Flags: []string{`\Seen`}}}); err != nil {
		t.Fatal(err)
	}
	h, err := s.GetMessage(ctx, MessageKey{Account: acct, Mailbox: "All Mail", UIDValidity: 1, UID: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.Flags, []string{`\Seen`}) {
		t.Fatalf("flags in All Mail = %v, want the global \\Seen", h.Flags)
	}
}

func TestMembershipOfMissingPlacementIsNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.MembershipOf(context.Background(), MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 1, UID: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

// A header without an InternalID gets its own non-canonical entity, so two
// unrelated messages without ids never merge.
func TestSyntheticIDsDoNotMerge(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2})
	a := header("INBOX", 2, 1, t0)
	b := header("INBOX", 2, 2, t0)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{a, b})
	st, _ := s.CacheStats(ctx, acct)
	if st.Messages != 2 {
		t.Fatalf("messages = %d, want 2 separate synthetic entities", st.Messages)
	}
}

// Schema v2 caches are rebuilt on upgrade: messages and bodies are gone, the
// mailboxes stay, and the sync state is reset.
func TestUpgradeFromV2ResetsMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	raw, err := sql.Open("sqlite", mustDSN(t, path))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := raw.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := raw.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO mailboxes (account, name, kind, display_name, uidvalidity, highest_uid, last_sync)
		VALUES ('acc1','INBOX','inbox','INBOX',1, 9, 123)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO messages (account, mailbox, uidvalidity, uid, date)
		VALUES ('acc1','INBOX',1,9, 0)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade from v2: %v", err)
	}
	defer s.Close()
	st, _ := s.CacheStats(context.Background(), acct)
	if st.Messages != 0 {
		t.Fatalf("v2 messages survived the rebuild: %+v", st)
	}
	states, _ := s.GetSyncState(context.Background(), acct)
	if len(states) != 1 || states[0].HighestUID != 0 || !states[0].LastSync.IsZero() {
		t.Fatalf("sync state not reset: %+v", states)
	}
}

func TestMessageCountsPerMailbox(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s,
		model.Mailbox{Name: "All Mail", Kind: model.KindFolder, UIDValidity: 1},
		model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 2},
		model.Mailbox{Name: "Empty", Kind: model.KindFolder, UIDValidity: 4},
	)
	_ = s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("All Mail", 1, 5, "pm-c", t0),
		member("INBOX", 2, 3, "pm-c", t0),
		member("INBOX", 2, 4, "pm-d", t0),
	})
	got, err := s.MessageCounts(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if got["All Mail"] != 1 || got["INBOX"] != 2 {
		t.Fatalf("counts = %v", got)
	}
	// A mailbox with no placements is reported with 0, from its counter.
	if n, ok := got["Empty"]; !ok || n != 0 {
		t.Fatalf("empty mailbox = %v, want 0 present", got)
	}
}

// TestStaleEpochPlacementIsFiltered guards the epoch join: a placement row from
// an older UIDVALIDITY is never returned, because its UID names a different
// message in the current epoch. An interrupted epoch change can leave one.
func TestStaleEpochPlacementIsFiltered(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	mustUpsertBoxes(t, s, model.Mailbox{Name: "INBOX", Kind: model.KindInbox, UIDValidity: 1})
	if err := s.UpsertHeaders(ctx, acct, []model.MessageHeader{
		member("INBOX", 1, 7, "pm-1", t0),
	}); err != nil {
		t.Fatal(err)
	}
	sq := s.(*sqliteStore)
	if _, err := sq.db.ExecContext(ctx, `
		INSERT INTO placements (account, mailbox, uidvalidity, uid, mid, date)
		VALUES (?, ?, ?, ?, ?, ?)`, acct, "INBOX", 9, 777, "pm-1", t0.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	mm, err := s.MembershipOfMessages(ctx, acct, []string{"pm-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(mm["pm-1"]) != 1 || mm["pm-1"][0].UID != 7 || mm["pm-1"][0].UIDValidity != 1 {
		t.Fatalf("membership = %+v, want only the current epoch", mm["pm-1"])
	}
	members, err := s.MembershipOf(ctx, MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: 9, UID: 777})
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].UIDValidity != 1 {
		t.Fatalf("MembershipOf returned stale rows: %+v", members)
	}
}
