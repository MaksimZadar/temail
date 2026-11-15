package sync

import (
	"context"
	"reflect"
	"testing"

	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// setupProton builds the Bridge shape: All Mail, INBOX, a label and a folder.
func setupProton(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	fx := newFixture(t, mutate)
	fx.bridge.addBox("All Mail", model.KindFolder, 1)
	fx.bridge.addBox("Labels/A", model.KindLabel, 1)
	fx.bridge.addBox("Folders/Work", model.KindFolder, 1)
	return fx
}

// The same message is in All Mail, INBOX and Labels/A with a different UID in
// each. One entity, one body fetch, and the membership lists all three.
func TestOneMessageInThreeMailboxesIsOneEntity(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("All Mail", "pm-shared", `\Seen`)
	fx.bridge.copyTo("INBOX", "pm-shared")
	fx.bridge.copyTo("Labels/A", "pm-shared")
	fx.bridge.appendShared("INBOX", "pm-inbox-only")
	fx.mustRun()

	ctx := context.Background()
	st, err := fx.st.CacheStats(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 2 {
		t.Fatalf("messages = %d, want 2 entities (one shared, one INBOX-only)", st.Messages)
	}
	if got := fx.bridge.bodyFetchesOf("pm-shared"); got != 1 {
		t.Fatalf("body fetches of the shared message = %d, want 1", got)
	}
	if st.Bodies != 2 {
		t.Fatalf("bodies = %d, want 2 (one per entity)", st.Bodies)
	}

	// The INBOX copy knows where it lives.
	page, err := fx.st.ListMessages(ctx, acct, "INBOX", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var shared model.MessageHeader
	for _, h := range page {
		if h.InternalID == "pm-shared" {
			shared = h
		}
	}
	if shared.InternalID == "" {
		t.Fatalf("shared message not listed in INBOX: %+v", page)
	}
	members, err := fx.st.MembershipOf(ctx, store.MessageKey{Account: acct, Mailbox: "INBOX", UIDValidity: shared.UIDValidity, UID: shared.UID})
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

// \Seen is global. Reading the message in All Mail (flag changed on the server)
// is seen by the INBOX copy after the next reconcile.
func TestFlagsAreGlobalAcrossCopiesAfterReconcile(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("All Mail", "pm-f")
	fx.bridge.copyTo("INBOX", "pm-f")
	fx.mustRun()

	fx.bridge.setSharedFlags("pm-f", `\Seen`)
	fx.mustRun()

	ctx := context.Background()
	counts, err := fx.st.GetSyncState(ctx, acct)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range counts {
		if m.Name == "INBOX" && m.Unseen != 0 {
			t.Fatalf("INBOX unseen = %d after the message was read in All Mail", m.Unseen)
		}
	}
	page, _ := fx.st.ListMessages(ctx, acct, "INBOX", store.ListOptions{Limit: 10})
	if len(page) != 1 || !reflect.DeepEqual(page[0].Flags, []string{`\Seen`}) {
		t.Fatalf("INBOX flags = %v, want the global \\Seen", page)
	}
}

// Unseen is counted per mailbox from its own listing.
func TestUnseenCountPerMailbox(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("INBOX", "pm-a")
	fx.bridge.appendShared("INBOX", "pm-b", `\Seen`)
	fx.bridge.copyTo("Labels/A", "pm-a")
	fx.mustRun()

	got := map[string]uint32{}
	states, _ := fx.st.GetSyncState(context.Background(), acct)
	for _, m := range states {
		got[m.Name] = m.Unseen
	}
	if got["INBOX"] != 1 || got["Labels/A"] != 1 || got["All Mail"] != 0 {
		t.Fatalf("unseen = %v", got)
	}
}

// Deleting one copy (for example removing a label) keeps the message and its
// body, which the other copies still reference.
func TestDeletedCopyKeepsMessage(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("All Mail", "pm-keep")
	uidLabel := fx.bridge.copyTo("Labels/A", "pm-keep")
	fx.mustRun()

	fx.bridge.expunge("Labels/A", uidLabel)
	res := fx.mustRun()
	if res.Deleted != 1 {
		t.Fatalf("deleted = %d, want the one label placement", res.Deleted)
	}
	ctx := context.Background()
	st, _ := fx.st.CacheStats(ctx, acct)
	if st.Messages != 1 || st.Bodies != 1 {
		t.Fatalf("after removing a label: %+v, want the message and body kept", st)
	}
	if got := fx.bridge.bodyFetchesOf("pm-keep"); got != 1 {
		t.Fatalf("body refetched after a label removal: %d fetches", got)
	}
}

// All Mail first, then the system folders, then folders, labels and the rest.
func TestSyncOrderAllMailFirst(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.addBox("Recovered Messages", model.KindFolder, 1)
	fx.bridge.addBox("Starred", model.KindFolder, 1)
	fx.bridge.addBox("Sent", model.KindSent, 1)
	fx.bridge.appendShared("All Mail", "pm-o")
	fx.mustRun()

	boxes := []model.Mailbox{
		{Name: "Recovered Messages", Kind: model.KindFolder},
		{Name: "Labels/A", Kind: model.KindLabel},
		{Name: "Folders/Work", Kind: model.KindFolder},
		{Name: "INBOX", Kind: model.KindInbox},
		{Name: "Sent", Kind: model.KindSent},
		{Name: "All Mail", Kind: model.KindFolder},
		{Name: "Starred", Kind: model.KindFolder},
	}
	got := []string{}
	for _, b := range orderMailboxes(boxes) {
		got = append(got, b.Name)
	}
	want := []string{"All Mail", "INBOX", "Sent", "Starred", "Folders/Work", "Labels/A", "Recovered Messages"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v\nwant    %v", got, want)
	}
}

// Without All Mail (hidden in Bridge), the rest of the sync still works.
func TestHiddenAllMailStillSyncs(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.addBox("Labels/A", model.KindLabel, 1)
	fx.bridge.appendShared("INBOX", "pm-1")
	fx.bridge.copyTo("Labels/A", "pm-1")
	res := fx.mustRun()
	if res.HeadersFetched != 2 {
		t.Fatalf("headers = %d, want 2 placements", res.HeadersFetched)
	}
	st, _ := fx.st.CacheStats(context.Background(), acct)
	if st.Messages != 1 || st.Bodies != 1 {
		t.Fatalf("hidden All Mail: %+v", st)
	}
}

// Recovered Messages is listed but its bodies are not fetched.
func TestRecoveredMessagesBodiesSkipped(t *testing.T) {
	fx := newFixture(t, nil)
	fx.bridge.addBox("Recovered Messages", model.KindFolder, 1)
	fx.bridge.appendShared("Recovered Messages", "pm-r")
	fx.mustRun()
	if got := fx.bridge.bodyFetchesOf("pm-r"); got != 0 {
		t.Fatalf("recovered message body fetched %d times", got)
	}
	st, _ := fx.st.CacheStats(context.Background(), acct)
	if st.Messages != 1 || st.Bodies != 0 {
		t.Fatalf("stats = %+v, want the header but no body", st)
	}
}

// A UIDVALIDITY change in one mailbox drops only that mailbox's placements. A
// message that is still in All Mail stays, and it is fetched only once.
func TestUIDValidityChangeKeepsSharedMessages(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("All Mail", "pm-v")
	fx.bridge.copyTo("Labels/A", "pm-v")
	fx.mustRun()

	fx.bridge.recreate("Labels/A")
	fx.bridge.copyTo("Labels/A", "pm-v")
	fx.mustRun()

	if got := fx.bridge.bodyFetchesOf("pm-v"); got != 1 {
		t.Fatalf("body fetches = %d after a label resync; the shared body must not be refetched", got)
	}
	st, _ := fx.st.CacheStats(context.Background(), acct)
	if st.Messages != 1 {
		t.Fatalf("messages = %d, want 1", st.Messages)
	}
}

// A run that is interrupted after the headers resumes without refetching the
// headers it already stored, and the body count stays at one per entity.
func TestResumeKeepsOneBodyPerEntity(t *testing.T) {
	fx := setupProton(t, nil)
	fx.bridge.appendShared("All Mail", "pm-x")
	fx.bridge.copyTo("INBOX", "pm-x")
	fx.bridge.appendShared("INBOX", "pm-y")
	fx.bridge.bodyErr = map[string]error{}
	fx.mustRun()

	fx.mustRun()
	if got := fx.bridge.bodyFetchesOf("pm-x") + fx.bridge.bodyFetchesOf("pm-y"); got != 2 {
		t.Fatalf("body fetches across two runs = %d, want 2 (one per entity)", got)
	}
}
