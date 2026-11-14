package mail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/model"
)

var bothModes = []string{config.TLSModeSTARTTLS, config.TLSModeSSL}

func TestConnectTOFUPinFlow(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newBridgeFixture(t, mode)
			ctx := context.Background()

			// Not pinned: the handshake is refused with the fingerprint and
			// subject so the UI can ask the user to confirm them.
			_, err := fx.connect("")
			var untrusted *UntrustedCertError
			if !errors.Is(err, ErrUntrustedCert) || !errors.As(err, &untrusted) {
				t.Fatalf("unpinned connect: got %v, want UntrustedCertError", err)
			}
			if untrusted.Fingerprint != fx.fingerprint {
				t.Errorf("fingerprint = %s, want %s", untrusted.Fingerprint, fx.fingerprint)
			}
			if !strings.Contains(untrusted.Subject, "CN=Proton Bridge test") {
				t.Errorf("subject = %q", untrusted.Subject)
			}
			if errors.Is(err, ErrCertMismatch) {
				t.Error("unpinned error must not be a mismatch")
			}

			// Pinned to the same value (upper case accepted): connects.
			c, err := fx.connect(strings.ToUpper(fx.fingerprint))
			if err != nil {
				t.Fatalf("pinned connect: %v", err)
			}
			if _, err := c.ListMailboxes(ctx); err != nil {
				t.Fatalf("ListMailboxes after pinned connect: %v", err)
			}

			// Pinned to a different value: hard failure with a re-pin message.
			other := fingerprintOf([]byte("some other certificate"))
			_, err = fx.connect(other)
			var mismatch *CertMismatchError
			if !errors.Is(err, ErrCertMismatch) || !errors.As(err, &mismatch) {
				t.Fatalf("mismatch connect: got %v, want CertMismatchError", err)
			}
			if errors.Is(err, ErrUntrustedCert) {
				t.Error("mismatch must not be reported as untrusted")
			}
			if !strings.Contains(err.Error(), "re-pin") {
				t.Errorf("mismatch message lacks re-pin guidance: %v", err)
			}
			if mismatch.Pinned != other || mismatch.Fingerprint != fx.fingerprint {
				t.Errorf("mismatch fields = pinned %s presented %s", mismatch.Pinned, mismatch.Fingerprint)
			}
		})
	}
}

func TestConnectWrongPasswordFails(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := NewClient()
	t.Cleanup(func() { _ = c.Close() })
	err := c.Connect(context.Background(), fx.account(fx.fingerprint), "wrong-password-xyz")
	if err == nil {
		t.Fatal("expected login failure")
	}
	if errors.Is(err, ErrUntrustedCert) || errors.Is(err, ErrCertMismatch) {
		t.Errorf("login failure reported as certificate error: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-password-xyz") {
		t.Error("error message must not contain the password")
	}
}

func TestConnectRefusesNonLoopbackHost(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	for _, host := range []string{"192.0.2.10", "mail.example.com", ""} {
		acct := fx.account(fx.fingerprint)
		acct.Host = host
		c := NewClient()
		err := c.Connect(context.Background(), acct, testPassword)
		if !errors.Is(err, ErrNotLoopback) {
			t.Errorf("host %q: got %v, want ErrNotLoopback", host, err)
		}
		_ = c.Close()
	}
}

func TestOperationsRequireConnection(t *testing.T) {
	ctx := context.Background()
	c := NewClient()
	if _, err := c.ListMailboxes(ctx); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ListMailboxes: %v", err)
	}
	if _, err := c.MailboxInfo(ctx, "INBOX"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("MailboxInfo: %v", err)
	}
	if _, err := c.FetchHeaders(ctx, "INBOX", AllUIDs()); !errors.Is(err, ErrNotConnected) {
		t.Errorf("FetchHeaders: %v", err)
	}
	if _, err := c.FetchMessage(ctx, "INBOX", 1); !errors.Is(err, ErrNotConnected) {
		t.Errorf("FetchMessage: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close on unconnected client: %v", err)
	}
}

func TestCancelledContextFailsBeforeAnyCommand(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := fx.mustConnect()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := fx.rec.stats()
	if _, err := c.ListMailboxes(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if after := fx.rec.stats(); len(after.selectsRO) != len(before.selectsRO) {
		t.Error("a cancelled operation must not reach the server")
	}
}

func TestListMailboxesKindsAndCounts(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSSL)
	for _, name := range []string{"Folders/Work", "Labels/Important", "Sent", "Drafts", "Trash", "Spam", "Archive", "All Mail"} {
		fx.ensureMailbox(name)
	}
	fx.appendMessage("INBOX", plainMessage(1))
	fx.appendMessage("INBOX", plainMessage(2), `\Seen`)
	fx.appendMessage("INBOX", plainMessage(3))
	c := fx.mustConnect()

	boxes, err := c.ListMailboxes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]model.Mailbox{}
	for _, b := range boxes {
		byName[b.Name] = b
	}
	want := map[string]struct {
		kind    model.MailboxKind
		display string
	}{
		"INBOX":            {model.KindInbox, "INBOX"},
		"Folders/Work":     {model.KindFolder, "Work"},
		"Labels/Important": {model.KindLabel, "Important"},
		"Sent":             {model.KindSent, "Sent"},
		"Drafts":           {model.KindDrafts, "Drafts"},
		"Trash":            {model.KindTrash, "Trash"},
		"Spam":             {model.KindSpam, "Spam"},
		"Archive":          {model.KindArchive, "Archive"},
		"All Mail":         {model.KindFolder, "All Mail"},
	}
	for name, w := range want {
		b, ok := byName[name]
		if !ok {
			t.Errorf("mailbox %q missing from listing", name)
			continue
		}
		if b.Kind != w.kind || b.DisplayName != w.display {
			t.Errorf("%s: kind=%s display=%q, want kind=%s display=%q", name, b.Kind, b.DisplayName, w.kind, w.display)
		}
		if b.UIDValidity == 0 {
			t.Errorf("%s: UIDValidity not reported", name)
		}
	}
	if inbox := byName["INBOX"]; inbox.Messages != 3 || inbox.Unseen != 2 {
		t.Errorf("INBOX counts = %d messages, %d unseen; want 3, 2", inbox.Messages, inbox.Unseen)
	}
}

func TestMailboxInfoUIDValidityChangesOnRecreate(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	for i := 1; i <= 3; i++ {
		fx.appendMessage("INBOX", plainMessage(i))
	}
	fx.ensureMailbox("Temp")
	c := fx.mustConnect()
	ctx := context.Background()

	inbox, err := c.MailboxInfo(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Messages != 3 || inbox.HighestUID != 3 || inbox.UIDNext != 4 {
		t.Errorf("INBOX info = %+v", inbox)
	}

	first, err := c.MailboxInfo(ctx, "Temp")
	if err != nil {
		t.Fatal(err)
	}
	if first.HighestUID != 0 {
		t.Errorf("empty mailbox HighestUID = %d, want 0", first.HighestUID)
	}
	// Deleting and recreating a mailbox starts a new UID epoch.
	if err := fx.user.Delete("Temp"); err != nil {
		t.Fatal(err)
	}
	fx.ensureMailbox("Temp")
	second, err := c.MailboxInfo(ctx, "Temp")
	if err != nil {
		t.Fatal(err)
	}
	if second.UIDValidity == first.UIDValidity {
		t.Errorf("UIDVALIDITY did not change after recreate (%d)", second.UIDValidity)
	}
}

func TestFetchHeadersBatchingAndIncrementalSync(t *testing.T) {
	const total = 450
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	for i := 1; i <= total; i++ {
		fx.appendMessage("INBOX", plainMessage(i))
	}
	c := fx.mustConnect()
	ctx := context.Background()

	info, err := c.MailboxInfo(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	fetchesBefore := fx.rec.stats().fetches
	all, err := c.FetchHeaders(ctx, "INBOX", AllUIDs())
	if err != nil {
		t.Fatal(err)
	}
	if got := fx.rec.stats().fetches - fetchesBefore; got != 3 {
		t.Errorf("FETCH commands for %d headers = %d, want 3 (batch size %d)", total, got, BatchSize)
	}
	if len(all) != total {
		t.Fatalf("got %d headers, want %d", len(all), total)
	}
	for i, h := range all {
		if h.UID != uint32(i+1) {
			t.Fatalf("header %d has UID %d, want %d", i, h.UID, i+1)
		}
	}
	if h := all[0]; h.Subject != "msg 1" || h.Mailbox != "INBOX" || h.UIDValidity != info.UIDValidity || h.MessageID != "msg-1@example.org" {
		t.Errorf("first header = %+v", h)
	}

	// Incremental sync: only UIDs above the last one seen.
	inc, err := c.FetchHeaders(ctx, "INBOX", UIDsAbove(447))
	if err != nil {
		t.Fatal(err)
	}
	if len(inc) != 3 || inc[0].UID != 448 || inc[2].UID != 450 {
		t.Errorf("UIDsAbove(447) = %d headers", len(inc))
	}
	// Nothing above the highest UID. The server's "n:*" answer is the highest
	// UID, which must be filtered out.
	none, err := c.FetchHeaders(ctx, "INBOX", UIDsAbove(total))
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("UIDsAbove(highest) returned %d headers, want 0", len(none))
	}
	mid, err := c.FetchHeaders(ctx, "INBOX", UIDRange{First: 10, Last: 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(mid) != 3 || mid[0].UID != 10 || mid[2].UID != 12 {
		t.Errorf("bounded range returned %d headers", len(mid))
	}
	empty, err := c.FetchHeaders(ctx, "INBOX", UIDRange{First: 20, Last: 10})
	if err != nil || len(empty) != 0 {
		t.Errorf("inverted range: %d headers, err %v", len(empty), err)
	}
}

func TestFetchMessageParsesWithoutMarkingSeen(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", altMessage())
	c := fx.mustConnect()
	ctx := context.Background()

	msg, err := c.FetchMessage(ctx, "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.TextBody, "Plain hello.") {
		t.Errorf("TextBody = %q", msg.TextBody)
	}
	if !strings.Contains(msg.HTMLBody, "<b>hello</b>") {
		t.Errorf("HTMLBody = %q", msg.HTMLBody)
	}
	if msg.Subject != "Alt body" || msg.MessageID != "alt-1@example.org" || msg.UID != 1 {
		t.Errorf("header = subject %q id %q uid %d", msg.Subject, msg.MessageID, msg.UID)
	}
	if msg.InReplyTo != "parent@example.org" {
		t.Errorf("InReplyTo = %q", msg.InReplyTo)
	}
	if msg.References != "<root@example.org> <parent@example.org>" {
		t.Errorf("References = %q", msg.References)
	}
	if len(msg.From) != 1 || msg.From[0].Email != "alice@example.org" || msg.From[0].Name != "Alice" {
		t.Errorf("From = %+v", msg.From)
	}
	if len(msg.Attachments) != 0 {
		t.Errorf("unexpected attachments: %+v", msg.Attachments)
	}

	// The message was not read through a BODY[] without PEEK, so it stays
	// unseen.
	boxes, err := c.ListMailboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range boxes {
		if b.Name == "INBOX" && b.Unseen != 1 {
			t.Errorf("INBOX unseen after fetch = %d, want 1", b.Unseen)
		}
	}
	if st := fx.rec.stats(); st.nonPeek != 0 {
		t.Errorf("%d non-PEEK body fetches", st.nonPeek)
	}
}

func TestFetchMessageSanitizesRemoteContent(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", remoteImageMessage())
	c := fx.mustConnect()

	msg, err := c.FetchMessage(context.Background(), "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	h := msg.HTMLBody
	for _, bad := range []string{"<script", "alert(", "onload", "onclick", "javascript:", "tracker.example", "color:red"} {
		if strings.Contains(h, bad) {
			t.Errorf("HTMLBody still contains %q: %s", bad, h)
		}
	}
	for _, good := range []string{"cid:logo@example.org", "https://example.org/ok", "good link"} {
		if !strings.Contains(h, good) {
			t.Errorf("HTMLBody lost %q: %s", good, h)
		}
	}
	// The text body is produced by enmime from the HTML part. Script content must
	// not leak into it.
	if !strings.Contains(msg.TextBody, "Hi") || !strings.Contains(msg.TextBody, "good link") || strings.Contains(msg.TextBody, "alert('x')") {
		t.Errorf("TextBody derived from HTML = %q", msg.TextBody)
	}
}

func TestAttachmentsListedAndDecoded(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSSL)
	fx.appendMessage("INBOX", attachmentMessage())
	c := fx.mustConnect()
	ctx := context.Background()

	msg, err := c.FetchMessage(ctx, "INBOX", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.TextBody, "See attached.") {
		t.Errorf("TextBody = %q", msg.TextBody)
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("attachments = %+v, want 2", msg.Attachments)
	}
	pdf, notes := msg.Attachments[0], msg.Attachments[1]
	if pdf.Filename != "report.pdf" || pdf.ContentType != "application/pdf" || pdf.PartID != "2" || pdf.Size <= 0 {
		t.Errorf("pdf attachment = %+v", pdf)
	}
	if notes.Filename != "notes.txt" || notes.ContentType != "text/plain" || notes.PartID != "3" {
		t.Errorf("notes attachment = %+v", notes)
	}

	got, err := c.FetchAttachment(ctx, "INBOX", 1, pdf.PartID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(pdfPayload) {
		t.Errorf("base64 attachment decoded to %q", got)
	}
	got, err = c.FetchAttachment(ctx, "INBOX", 1, notes.PartID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != notesText {
		t.Errorf("quoted-printable attachment decoded to %q", got)
	}
}

func TestFetchAttachmentRejectsBadRequests(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", attachmentMessage())
	c := fx.mustConnect()
	ctx := context.Background()

	cases := []struct {
		name   string
		uid    uint32
		partID string
	}{
		{"body text part is not an attachment", 1, "1"},
		{"unknown part", 1, "9"},
		{"unknown UID", 99, "2"},
		{"zero UID", 0, "2"},
		{"empty part ID", 1, ""},
		{"zero part ID", 1, "0"},
		{"injection attempt", 1, "2 BODY[]"},
		{"path traversal", 1, "../2"},
		{"leading dot", 1, ".2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.FetchAttachment(ctx, "INBOX", tc.uid, tc.partID); err == nil {
				t.Errorf("FetchAttachment(%d, %q) succeeded", tc.uid, tc.partID)
			}
		})
	}
	if _, err := c.FetchMessage(ctx, "INBOX", 0); err == nil {
		t.Error("FetchMessage UID 0 succeeded")
	}
}
