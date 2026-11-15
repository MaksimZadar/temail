package mail

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/config"
)

// withIdentity returns a message with the Proton identity header.
func withIdentity(id string, n int) []byte {
	return []byte("From: a@example.org\r\nTo: me@example.org\r\nSubject: m" +
		string(rune('0'+n)) + "\r\nMessage-ID: <rfc" + id + "@example.org>\r\nX-Pm-Internal-Id: " + id +
		"\r\nDate: Mon, 02 Nov 2026 09:00:00 +0000\r\n\r\nbody\r\n")
}

func TestStatusReadsCountsWithoutSelect(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", plainMessage(1))
	fx.appendMessage("INBOX", plainMessage(2), imap.FlagSeen)
	fx.appendMessage("INBOX", plainMessage(3))
	c := fx.mustConnect()

	st, err := c.Status(context.Background(), "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if st.Messages != 3 || st.Unseen != 2 || st.UIDNext != 4 || st.UIDValidity == 0 {
		t.Fatalf("status = %+v", st)
	}
	if st.Name != "INBOX" {
		t.Fatalf("name = %q", st.Name)
	}
}

func TestStatusOfMissingMailboxFails(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := fx.mustConnect()
	if _, err := c.Status(context.Background(), "No/Such"); err == nil {
		t.Fatal("status of a missing mailbox must fail")
	}
}

func TestCapabilitiesAfterLogin(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := fx.mustConnect()
	caps := c.Capabilities()
	if len(caps) == 0 {
		t.Fatal("no capabilities after login")
	}
	for i := 1; i < len(caps); i++ {
		if caps[i-1] > caps[i] {
			t.Fatalf("capabilities not sorted: %v", caps)
		}
	}
	found := false
	for _, c := range caps {
		if c == "IMAP4REV1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("IMAP4REV1 missing from %v", caps)
	}
	if got := NewClient().Capabilities(); got != nil {
		t.Fatalf("unconnected client reports %v", got)
	}
}

func TestFetchHeadersReadsProtonIdentity(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", withIdentity("pm-77", 1))
	fx.appendMessage("INBOX", plainMessage(2))
	c := fx.mustConnect()

	hs, err := c.FetchHeaders(context.Background(), "INBOX", AllUIDs())
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 2 {
		t.Fatalf("headers = %d", len(hs))
	}
	if hs[0].InternalID != "pm-77" || !hs[0].Canonical {
		t.Fatalf("identity header not read: %+v", hs[0])
	}
	if hs[1].Canonical || hs[1].InternalID != "" {
		t.Fatalf("message without identity must not be canonical: %+v", hs[1])
	}
}
