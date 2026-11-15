// Package store is the SQLite cache for temail: mailboxes, message headers,
// sanitised bodies and attachment metadata. The cache is disposable. Deleting
// the file never loses data on the server, and a fresh sync rebuilds it.
//
// The schema is versioned with PRAGMA user_version (see schema.go). Foreign
// keys are on, so deleting a mailbox row or purging a mailbox also deletes its
// messages and bodies.
//
// Message identity is the key (account, mailbox, UIDVALIDITY, UID). The
// Message-ID is stored and indexed, but it is not unique and is not used as a
// key. See README.md.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/MaksimZadar/temail/internal/model"
)

// ErrEmptyListing is returned by DeleteMailboxesNotIn when given no names. An
// empty server listing is treated as a failed listing, not as "no mailboxes".
var ErrEmptyListing = errors.New("refusing to remove mailboxes for an empty server listing")

// ErrNotFound is returned when a requested message or body is not cached.
var ErrNotFound = errors.New("not found in cache")

// MaxBodyAttempts bounds how often PendingBodies offers a message whose body
// fetch failed. After this many failures the message is left without a body
// until its mailbox is purged.
const MaxBodyAttempts = 3

// MaxListLimit caps ListMessages pages.
const MaxListLimit = 1000

// MessageKey identifies one cached message.
type MessageKey struct {
	Account     string
	Mailbox     string
	UIDValidity uint32
	UID         uint32
}

// MailboxState is the cached state of one mailbox.
type MailboxState struct {
	Name        string
	Kind        model.MailboxKind
	DisplayName string
	UIDValidity uint32
	// Unseen is the number of messages in the mailbox without \Seen, as of the
	// last reconcile of the mailbox. It is 0 until the first sync.
	Unseen uint32
	// HighestUID is the highest UID cached for the current UIDVALIDITY epoch.
	// It is 0 when nothing is cached.
	HighestUID uint32
	// LastSync is when a sync of this mailbox last ran to completion. Zero
	// means it never completed (or was purged).
	LastSync time.Time
}

// Cursor marks a position in ListMessages, which is ordered by (Date, UID)
// descending. A page starts strictly after the cursor.
type Cursor struct {
	Date time.Time
	UID  uint32
}

// ListOptions controls ListMessages paging. A nil Before starts at the newest
// message.
type ListOptions struct {
	Limit  int
	Before *Cursor
}

// Body is the cached content of one message. Attachments holds metadata only;
// attachment content is never cached.
type Body struct {
	Text        string
	HTML        string
	Attachments []model.Attachment
}

// PendingBody is one message (one entity, across all its mailboxes) that still
// needs a body. Key is the placement the body should be fetched through: All
// Mail when the message is there, otherwise another placement.
type PendingBody struct {
	Key  MessageKey
	MID  string
	Date time.Time
}

// PendingCursor marks a position in PendingBodies. The order is (Date, MID)
// descending, so the cursor is the last message of the previous page.
type PendingCursor struct {
	Date time.Time
	MID  string
}

// Placement is one mailbox membership of a message: where it is, and under
// which UID. A message has one placement per mailbox it is in (folder and
// labels included).
type Placement struct {
	Mailbox     string
	Kind        model.MailboxKind
	DisplayName string
	UIDValidity uint32
	UID         uint32
}

// FlagUpdate sets the flags of one cached message.
type FlagUpdate struct {
	UID   uint32
	Flags []string
}

// MailboxCounts is the cached message and body count of one mailbox.
type MailboxCounts struct {
	Mailbox  string
	Messages int
	Bodies   int
}

// CacheStats is the size of the cache on disk and the number of cached rows of
// one account. Bytes covers the database file and its -wal and -shm files; a
// missing file counts as 0.
type CacheStats struct {
	Bytes    int64
	Messages int64
	Bodies   int64
}

// Store is the cache API. Every method is safe for concurrent use. Write
// methods run in one transaction each, so a cancelled or failed call leaves
// no partial state.
type Store interface {
	// UpsertMailboxes inserts or updates the given mailboxes. If a mailbox's
	// UIDVALIDITY differs from the cached one, its messages are purged in the
	// same transaction, because cached UIDs from the old epoch are invalid.
	UpsertMailboxes(ctx context.Context, account string, boxes []model.Mailbox) error
	// DeleteMailboxesNotIn removes the account's mailboxes whose names are not
	// in names, with their messages and bodies. Pass the full server listing.
	// An empty names list returns ErrEmptyListing and removes nothing.
	DeleteMailboxesNotIn(ctx context.Context, account string, names []string) error
	// GetSyncState returns the cached state of every mailbox of the account.
	GetSyncState(ctx context.Context, account string) ([]MailboxState, error)
	// MarkSynced records that a sync of mailbox completed at at.
	MarkSynced(ctx context.Context, account, mailbox string, at time.Time) error
	// PurgeMailbox deletes the placements of the mailbox, then every message and
	// body that is left with no placement, and resets its HighestUID, LastSync
	// and Unseen. The UIDVALIDITY is kept.
	PurgeMailbox(ctx context.Context, account, mailbox string) error
	// SetUnseen records the unseen count of a mailbox, computed by the caller
	// from its listing.
	SetUnseen(ctx context.Context, account, mailbox string, unseen uint32) error

	// CachedUIDs returns the cached UIDs of one mailbox epoch, ascending.
	CachedUIDs(ctx context.Context, account, mailbox string, uidValidity uint32) ([]uint32, error)
	// CachedFlags returns the cached flags of one mailbox epoch, by UID.
	CachedFlags(ctx context.Context, account, mailbox string, uidValidity uint32) (map[uint32][]string, error)
	// UpsertHeaders stores headers in one transaction: the message (keyed by
	// InternalID, or a synthetic id when it is empty) and its placement in the
	// header's mailbox. An existing message keeps its body state. Headers for an
	// epoch other than the mailbox's current UIDVALIDITY are rejected.
	UpsertHeaders(ctx context.Context, account string, headers []model.MessageHeader) error
	// DeleteMissing removes the placements of one epoch whose UIDs are not in
	// keep, then the messages that are left with no placement. Deletes run in
	// transactions of at most 1000 rows each. It returns the number of removed
	// placements.
	DeleteMissing(ctx context.Context, account, mailbox string, uidValidity uint32, keep []uint32) (int, error)
	// UpdateFlags sets the flags of the listed messages of one epoch in one
	// transaction. Unknown UIDs are ignored.
	UpdateFlags(ctx context.Context, account, mailbox string, uidValidity uint32, updates []FlagUpdate) error

	// ListMessages returns headers of one mailbox, newest first.
	ListMessages(ctx context.Context, account, mailbox string, opts ListOptions) ([]model.MessageHeader, error)
	// GetMessage returns one cached header, or ErrNotFound.
	GetMessage(ctx context.Context, key MessageKey) (model.MessageHeader, error)
	// SetBody stores the body of a message and marks it as cached. It returns
	// ErrNotFound if the message header is not cached.
	SetBody(ctx context.Context, key MessageKey, body Body) error
	// GetBody returns the cached body, or ErrNotFound.
	GetBody(ctx context.Context, key MessageKey) (Body, error)
	// MarkBodyFailed records one failed body fetch for a message.
	MarkBodyFailed(ctx context.Context, key MessageKey) error
	// PendingBodies returns up to limit messages (entities) of the account that
	// have no body and fewer than MaxBodyAttempts failures, newest first. Each is
	// returned with the placement to fetch it through. Pass the last message of
	// the previous page as after (nil for the first page). Paging with a cursor
	// means a message that failed in this run is not offered again in the same
	// run.
	PendingBodies(ctx context.Context, account string, after *PendingCursor, limit int) ([]PendingBody, error)

	// Counts returns the message and body counts of every mailbox of the account.
	Counts(ctx context.Context, account string) ([]MailboxCounts, error)
	// MembershipOf returns every placement of the message that the key points
	// at: its folder and labels. ErrNotFound if the key has no placement.
	MembershipOf(ctx context.Context, key MessageKey) ([]Placement, error)
	// CacheStats returns the on-disk size and the message and body totals of
	// the account. It only reads: it does not scan rows (see sqlite.go).
	CacheStats(ctx context.Context, account string) (CacheStats, error)

	// Close closes the database.
	Close() error
}
