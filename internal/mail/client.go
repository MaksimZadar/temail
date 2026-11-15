// Package mail is the read-only IMAP client for Proton Bridge. It returns the
// plain types in internal/model and never changes mailbox state: the Client
// interface has no write methods, and the implementation issues only LOGIN,
// LIST, STATUS, EXAMINE (read-only SELECT), UID SEARCH, UID FETCH with
// BODY.PEEK, and LOGOUT/close. See README.md.
package mail

import (
	"context"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/model"
)

// BatchSize is the number of UIDs requested per FETCH when reading headers.
const BatchSize = 200

// Client is a read-only connection to one Bridge account. A Client is safe for
// concurrent use, but operations are serialised: each one selects its mailbox
// and then fetches, and those steps must not interleave with another
// operation's SELECT.
//
// Cancelling the context passed to an operation aborts it and closes the
// connection; call Connect again to reconnect.
type Client interface {
	// Connect dials Bridge, verifies the pinned certificate (see
	// ErrUntrustedCert and ErrCertMismatch), and logs in. Calling Connect on a
	// connected Client closes the old connection first.
	Connect(ctx context.Context, acct config.BridgeAccount, password string) error

	// ListMailboxes returns every selectable mailbox with its kind and counts.
	ListMailboxes(ctx context.Context) ([]model.Mailbox, error)

	// MailboxInfo returns the UIDVALIDITY and UID state of a mailbox. Use it
	// before incremental sync: if UIDValidity changed, cached UIDs are invalid.
	MailboxInfo(ctx context.Context, mailbox string) (MailboxInfo, error)

	// FetchHeaders returns headers for the messages whose UIDs fall in rng,
	// ordered by UID. It fetches in batches of BatchSize.
	FetchHeaders(ctx context.Context, mailbox string, rng UIDRange) ([]model.MessageHeader, error)

	// Status returns the UIDVALIDITY, UIDNEXT, message count and unseen count of
	// a mailbox with STATUS. It does not SELECT, so it is cheap even for All Mail
	// and it does not change the selected mailbox. Use it for polling.
	Status(ctx context.Context, mailbox string) (MailboxStatus, error)

	// Capabilities returns the server's capability names from the last login,
	// upper-cased and sorted (for example "IDLE" or "MOVE"). It returns nil when
	// not connected. It never sends a command.
	Capabilities() []string

	// ListUIDs returns the UID and flags of every message in a mailbox, with
	// the mailbox's UIDVALIDITY. It transfers no headers or bodies, so it is
	// the cheap way to find server-side deletions and flag changes. It issues
	// UID SEARCH and UID FETCH FLAGS only.
	ListUIDs(ctx context.Context, mailbox string) (UIDListing, error)

	// FetchMessage returns one message with sanitised HTML, the plain-text body
	// and its attachment list. It does not set \Seen.
	FetchMessage(ctx context.Context, mailbox string, uid uint32) (model.Message, error)

	// FetchAttachment returns the decoded content of an attachment part listed
	// by FetchMessage. partID is validated before it reaches the server.
	FetchAttachment(ctx context.Context, mailbox string, uid uint32, partID string) ([]byte, error)

	// Close logs out and closes the connection. Closing an unconnected Client
	// is not an error.
	Close() error
}

// UIDRange selects UIDs First through Last inclusive. Last == 0 means no upper
// bound ("*"). First == 0 is treated as 1.
//
// Use UIDsAbove for incremental sync.
type UIDRange struct {
	First uint32
	Last  uint32
}

// UIDsAbove returns the range of UIDs strictly greater than n, for fetching
// only messages that arrived after the last synced UID.
func UIDsAbove(n uint32) UIDRange {
	return UIDRange{First: n + 1}
}

// AllUIDs returns the range of every UID.
func AllUIDs() UIDRange {
	return UIDRange{First: 1}
}

// UIDListing is the UID and flag state of every message in one mailbox. UIDs
// are ascending.
type UIDListing struct {
	UIDValidity uint32
	Messages    []UIDState
}

// UIDState is one message's UID and current IMAP flags.
type UIDState struct {
	UID   uint32
	Flags []string
}

// MailboxStatus is the STATUS answer for one mailbox. Unseen is 0 when the
// server did not report it.
type MailboxStatus struct {
	Name        string
	UIDValidity uint32
	UIDNext     uint32
	Messages    uint32
	Unseen      uint32
}

// MailboxInfo is the UID state of a selected mailbox.
type MailboxInfo struct {
	Name        string
	UIDValidity uint32
	// UIDNext is the UID the next appended message will get.
	UIDNext uint32
	// HighestUID is UIDNext-1: the highest UID assigned in this UIDVALIDITY
	// epoch. Some UIDs at or below it may have been expunged.
	HighestUID uint32
	Messages   uint32
}

// ensure the implementation satisfies the interface.
var _ Client = (*imapClient)(nil)
