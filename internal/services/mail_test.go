package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const testEpoch uint32 = 7

func header(uid uint32) model.MessageHeader {
	return model.MessageHeader{Mailbox: "INBOX", UIDValidity: testEpoch, UID: uid, Subject: "s", Date: t0.Add(time.Duration(uid) * time.Minute)}
}

type mailFixture struct {
	st     *fakeStore
	client *fakeClient
	f      *clientFactory
	svc    *MailService
}

func newMailFixture(t *testing.T) *mailFixture {
	t.Helper()
	st := newFakeStore()
	client := &fakeClient{info: mail.MailboxInfo{UIDValidity: testEpoch}}
	f := &clientFactory{c: client}
	sec := newFakeSecrets()
	sec.pw["main"] = testPassword
	cfg := newFakeConfig(readyConfig())
	svc := NewMailService(st, cfg, sec, f.new)
	t.Cleanup(func() { _ = svc.ServiceShutdown() })
	return &mailFixture{st: st, client: client, f: f, svc: svc}
}

func TestListMailboxesFromCacheINBOXFirst(t *testing.T) {
	fx := newMailFixture(t)
	synced := t0
	fx.st.states = []store.MailboxState{
		{Name: "Labels/Work", DisplayName: "Work", Kind: model.KindLabel, UIDValidity: 3},
		{Name: "INBOX", DisplayName: "INBOX", Kind: model.KindInbox, UIDValidity: 9, LastSync: synced},
		{Name: "Archive", DisplayName: "Archive", Kind: model.KindArchive},
	}
	fx.st.counts = []store.MailboxCounts{{Mailbox: "INBOX", Messages: 12, Bodies: 5}}

	got, err := fx.svc.ListMailboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, m := range got {
		names = append(names, m.Name)
	}
	if strings.Join(names, ",") != "INBOX,Archive,Labels/Work" {
		t.Fatalf("order = %v", names)
	}
	if got[0].Messages != 12 || got[0].Bodies != 5 || got[0].LastSync == nil {
		t.Fatalf("INBOX counts/sync wrong: %+v", got[0])
	}
	if got[1].LastSync != nil {
		t.Fatal("never-synced mailbox must have nil lastSync")
	}
	if fx.f.count() != 0 {
		t.Fatal("listing mailboxes must not connect to Bridge")
	}
}

func TestListMessagesPagingAndLimits(t *testing.T) {
	tests := []struct {
		name      string
		limit     int
		listed    int
		wantLimit int
		wantNext  bool
	}{
		{"default limit when zero", 0, 2, DefaultPageSize, false},
		{"full page yields a cursor", 2, 2, 2, true},
		{"short page ends paging", 5, 1, 5, false},
		{"limit capped at store max", store.MaxListLimit + 500, 0, store.MaxListLimit, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newMailFixture(t)
			for i := 0; i < tt.listed; i++ {
				fx.st.listed = append(fx.st.listed, header(uint32(10-i)))
			}
			page, err := fx.svc.ListMessages(context.Background(), "INBOX", nil, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if fx.st.lastOpts.Limit != tt.wantLimit {
				t.Fatalf("limit = %d, want %d", fx.st.lastOpts.Limit, tt.wantLimit)
			}
			if (page.Next != nil) != tt.wantNext {
				t.Fatalf("next = %+v, want present=%v", page.Next, tt.wantNext)
			}
			if page.Messages == nil {
				t.Fatal("messages must be an empty slice, not nil")
			}
		})
	}
}

func TestListMessagesPassesCursorThrough(t *testing.T) {
	fx := newMailFixture(t)
	c := &PageCursor{Date: t0, UID: 42}
	if _, err := fx.svc.ListMessages(context.Background(), "INBOX", c, 10); err != nil {
		t.Fatal(err)
	}
	if fx.st.lastOpts.Before == nil || !fx.st.lastOpts.Before.Date.Equal(t0) || fx.st.lastOpts.Before.UID != 42 {
		t.Fatalf("cursor not passed to the store: %+v", fx.st.lastOpts.Before)
	}
}

func TestListMessagesRejectsBadMailbox(t *testing.T) {
	fx := newMailFixture(t)
	for _, name := range []string{"", strings.Repeat("a", maxMailboxName+1)} {
		if _, err := fx.svc.ListMessages(context.Background(), name, nil, 10); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("mailbox %q: err = %v", name, err)
		}
	}
}

func TestGetMessageCacheHitDoesNotConnect(t *testing.T) {
	fx := newMailFixture(t)
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 4}
	fx.st.headers[key] = header(4)
	fx.st.bodies[key] = store.Body{Text: "hello", HTML: "<p>hello</p>", Attachments: []model.Attachment{{Filename: "a.pdf", PartID: "2"}}}

	msg, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 4)
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextBody != "hello" || msg.HTMLBody != "<p>hello</p>" || len(msg.Attachments) != 1 || msg.UID != 4 {
		t.Fatalf("unexpected message %+v", msg)
	}
	if fx.f.count() != 0 || fx.st.bodyWrites() != 0 {
		t.Fatal("cache hit must not connect or write")
	}
}

func TestGetMessageFallbackFetchesAndCaches(t *testing.T) {
	fx := newMailFixture(t)
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 9}
	fx.st.headers[key] = header(9)
	fx.client.message = model.Message{
		MessageHeader: header(9),
		TextBody:      "fetched text",
		HTMLBody:      "<p>fetched</p>",
		Attachments:   []model.Attachment{{Filename: "x.txt", PartID: "2", Size: 3}},
	}

	msg, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 9)
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextBody != "fetched text" || msg.Subject != "s" {
		t.Fatalf("unexpected message %+v", msg)
	}
	if m, _ := fx.client.fetches(); m != 1 {
		t.Fatalf("fetches = %d, want 1", m)
	}
	if fx.st.bodyWrites() != 1 || fx.st.bodies[key].Text != "fetched text" {
		t.Fatal("fetched body was not cached")
	}

	// A second read is served from the cache.
	if _, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, 9); err != nil {
		t.Fatal(err)
	}
	if m, _ := fx.client.fetches(); m != 1 {
		t.Fatalf("second read fetched again (fetches = %d)", m)
	}
}

func TestGetMessageErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mailFixture)
		uid     uint32
		wantErr error
	}{
		{"header not cached", func(*mailFixture) {}, 5, ErrNotCached},
		{"zero uid is invalid", func(*mailFixture) {}, 0, ErrInvalidInput},
		{"epoch changed on server, no fetch", func(fx *mailFixture) {
			key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 6}
			fx.st.headers[key] = header(6)
			fx.client.info = mail.MailboxInfo{UIDValidity: testEpoch + 1}
		}, 6, ErrStaleEpoch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newMailFixture(t)
			tt.setup(fx)
			_, err := fx.svc.GetMessage(context.Background(), "INBOX", testEpoch, tt.uid)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if m, _ := fx.client.fetches(); m != 0 {
				t.Fatal("no message may be fetched in an error case")
			}
		})
	}
}

func TestGetAttachmentChecksPartAndSize(t *testing.T) {
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 3}
	listed := []model.Attachment{
		{Filename: "small.txt", ContentType: "text/plain", Size: 5, PartID: "2"},
		{Filename: "huge.bin", ContentType: "application/octet-stream", Size: MaxAttachmentBytes + 1, PartID: "3"},
	}
	tests := []struct {
		name    string
		partID  string
		wantErr error
	}{
		{"listed part is returned", "2", nil},
		{"unlisted part is refused", "9", ErrNoSuchAttachment},
		{"path-like part id is invalid", "../2", ErrInvalidInput},
		{"shell-like part id is invalid", "2;rm", ErrInvalidInput},
		{"declared size over the limit is refused", "3", ErrAttachmentTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newMailFixture(t)
			fx.st.headers[key] = header(3)
			fx.st.bodies[key] = store.Body{Attachments: listed}
			fx.client.attachment = []byte("hello")

			got, err := fx.svc.GetAttachment(context.Background(), "INBOX", testEpoch, 3, tt.partID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil {
				if string(got.Data) != "hello" || got.Filename != "small.txt" || got.Size != 5 {
					t.Fatalf("unexpected attachment %+v", got)
				}
			}
		})
	}
}

func TestGetAttachmentRefusesStaleEpochAndOversizeContent(t *testing.T) {
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 3}

	fx := newMailFixture(t)
	fx.st.headers[key] = header(3)
	fx.st.bodies[key] = store.Body{Attachments: []model.Attachment{{PartID: "2", Size: 5}}}
	fx.client.info = mail.MailboxInfo{UIDValidity: testEpoch + 1}
	if _, err := fx.svc.GetAttachment(context.Background(), "INBOX", testEpoch, 3, "2"); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale epoch: err = %v", err)
	}

	fx = newMailFixture(t)
	fx.st.headers[key] = header(3)
	fx.st.bodies[key] = store.Body{Attachments: []model.Attachment{{PartID: "2", Size: 5}}}
	fx.client.info = mail.MailboxInfo{UIDValidity: testEpoch}
	fx.client.attachment = make([]byte, MaxAttachmentBytes+1)
	if _, err := fx.svc.GetAttachment(context.Background(), "INBOX", testEpoch, 3, "2"); !errors.Is(err, ErrAttachmentTooLarge) {
		t.Fatalf("oversize content: err = %v", err)
	}
}

func TestGetAttachmentFetchErrorIsWrapped(t *testing.T) {
	key := store.MessageKey{Account: "main", Mailbox: "INBOX", UIDValidity: testEpoch, UID: 3}
	fx := newMailFixture(t)
	fx.st.headers[key] = header(3)
	fx.st.bodies[key] = store.Body{Attachments: []model.Attachment{{PartID: "2", Size: 5}}}
	boom := errors.New("bridge went away")
	fx.client.attachErr = boom
	if _, err := fx.svc.GetAttachment(context.Background(), "INBOX", testEpoch, 3, "2"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
