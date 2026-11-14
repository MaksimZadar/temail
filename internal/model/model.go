// Package model holds the plain domain types shared by the mail layer and
// (later) the cache, UI and action layers. It has no behaviour and no
// dependencies beyond the standard library.
package model

import "time"

// MailboxKind classifies an IMAP mailbox for display and for later action
// rules. Proton Bridge exposes user folders as "Folders/X" and labels as
// "Labels/X"; the system folders map to their own kinds.
type MailboxKind string

const (
	KindInbox   MailboxKind = "inbox"
	KindSent    MailboxKind = "sent"
	KindDrafts  MailboxKind = "drafts"
	KindTrash   MailboxKind = "trash"
	KindSpam    MailboxKind = "spam"
	KindArchive MailboxKind = "archive"
	KindFolder  MailboxKind = "folder"
	KindLabel   MailboxKind = "label"
)

// Mailbox is one IMAP mailbox as listed by the server.
type Mailbox struct {
	// Name is the full IMAP mailbox name, e.g. "INBOX" or "Labels/Work".
	Name string
	// DisplayName is the name shown to the user: the part after "Folders/" or
	// "Labels/" for user folders and labels, otherwise Name.
	DisplayName string
	Kind        MailboxKind
	// UIDValidity identifies the UID epoch of this mailbox. A change means
	// cached UIDs for the mailbox are invalid.
	UIDValidity uint32
	Messages    uint32
	Unseen      uint32
}

// Address is an RFC 5322 mailbox address.
type Address struct {
	Name  string
	Email string
}

// MessageHeader is the envelope-level view of one message. The pair
// (Mailbox, UIDValidity, UID) identifies it; Message-ID is not unique enough
// to key on.
type MessageHeader struct {
	Mailbox     string
	UIDValidity uint32
	UID         uint32
	MessageID   string
	Subject     string
	From        []Address
	To          []Address
	Date        time.Time
	// Flags holds IMAP flags such as "\Seen" and "\Flagged".
	Flags      []string
	InReplyTo  string
	References string
}

// Attachment describes one non-body MIME part. PartID is the IMAP body
// section number (for example "2" or "2.1") used to fetch its content.
type Attachment struct {
	Filename    string
	ContentType string
	Size        int64
	PartID      string
}

// Message is a fully fetched message. HTMLBody is already sanitised for
// display; TextBody is the plain-text alternative and is always populated when
// the message has one.
type Message struct {
	MessageHeader
	TextBody    string
	HTMLBody    string
	Attachments []Attachment
}
