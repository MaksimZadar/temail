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
	Name  string `json:"name"`
	Email string `json:"email"`
}

// MessageHeader is the envelope-level view of one message. The pair
// (Mailbox, UIDValidity, UID) identifies it; Message-ID is not unique enough
// to key on.
type MessageHeader struct {
	Mailbox     string    `json:"mailbox"`
	UIDValidity uint32    `json:"uidValidity"`
	UID         uint32    `json:"uid"`
	MessageID   string    `json:"messageId"`
	Subject     string    `json:"subject"`
	From        []Address `json:"from"`
	To          []Address `json:"to"`
	Date        time.Time `json:"date"`
	// Flags holds IMAP flags such as "\Seen" and "\Flagged".
	Flags      []string `json:"flags"`
	InReplyTo  string   `json:"inReplyTo"`
	References string   `json:"references"`
}

// Attachment describes one non-body MIME part. PartID is the IMAP body
// section number (for example "2" or "2.1") used to fetch its content.
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	PartID      string `json:"partId"`
}

// Message is a fully fetched message. HTMLBody is already sanitised for
// display; TextBody is the plain-text alternative and is always populated when
// the message has one.
type Message struct {
	MessageHeader
	TextBody    string       `json:"textBody"`
	HTMLBody    string       `json:"htmlBody"`
	Attachments []Attachment `json:"attachments"`
}
