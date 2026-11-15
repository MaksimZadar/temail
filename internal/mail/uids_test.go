package mail

import (
	"context"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/config"
)

func TestListUIDsReturnsUIDsAndFlags(t *testing.T) {
	const total = 250
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	for i := 1; i <= total; i++ {
		fx.appendMessage("INBOX", plainMessage(i))
	}
	fx.appendMessage("INBOX", plainMessage(total+1), imap.FlagSeen, imap.FlagFlagged)
	c := fx.mustConnect()
	ctx := context.Background()

	info, err := c.MailboxInfo(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	fetchesBefore := fx.rec.stats().fetches
	got, err := c.ListUIDs(ctx, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if n := fx.rec.stats().fetches - fetchesBefore; n != 1 {
		t.Errorf("FETCH commands for %d UIDs = %d, want 1 (UID 1:*)", total+1, n)
	}
	if got.UIDValidity != info.UIDValidity {
		t.Errorf("UIDValidity = %d, want %d", got.UIDValidity, info.UIDValidity)
	}
	if len(got.Messages) != total+1 {
		t.Fatalf("got %d UIDs, want %d", len(got.Messages), total+1)
	}
	for i, m := range got.Messages {
		if m.UID != uint32(i+1) {
			t.Fatalf("entry %d has UID %d, want %d", i, m.UID, i+1)
		}
	}
	last := got.Messages[total]
	if last.UID != total+1 || !slices.Contains(last.Flags, "\\Seen") || !slices.Contains(last.Flags, "\\Flagged") {
		t.Errorf("last entry = %+v, want UID %d with \\Seen and \\Flagged", last, total+1)
	}
	if len(got.Messages[0].Flags) != 0 {
		t.Errorf("first entry flags = %v, want none", got.Messages[0].Flags)
	}
}

func TestListUIDsEmptyMailbox(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := fx.mustConnect()
	got, err := c.ListUIDs(context.Background(), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 0 {
		t.Errorf("empty mailbox returned %d UIDs", len(got.Messages))
	}
}
