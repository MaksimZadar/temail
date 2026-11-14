package mail

import (
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/model"
)

// classifyMailbox maps a LIST entry to its kind and display name.
//
// Order of rules:
//  1. "Folders<delim>X" is a user folder and "Labels<delim>X" a label. The
//     display name is X.
//  2. A SPECIAL-USE attribute (\Sent, \Drafts, \Trash, \Junk, \Archive) decides
//     the kind when the server sends one.
//  3. Otherwise the name decides: INBOX, Sent, Drafts, Trash, Spam, Archive
//     (case-insensitive).
//  4. Anything else is a folder named as the server lists it.
//
// System mailboxes keep their IMAP name as the display name. "All Mail" and
// other unlisted special-use mailboxes (\All, \Flagged, \Important) map to
// KindFolder.
func classifyMailbox(name string, delim rune, attrs []imap.MailboxAttr) (model.MailboxKind, string) {
	sep := "/"
	if delim != 0 {
		sep = string(delim)
	}
	if rest, ok := strings.CutPrefix(name, "Folders"+sep); ok && rest != "" {
		return model.KindFolder, rest
	}
	if rest, ok := strings.CutPrefix(name, "Labels"+sep); ok && rest != "" {
		return model.KindLabel, rest
	}

	for _, a := range attrs {
		switch a {
		case imap.MailboxAttrSent:
			return model.KindSent, name
		case imap.MailboxAttrDrafts:
			return model.KindDrafts, name
		case imap.MailboxAttrTrash:
			return model.KindTrash, name
		case imap.MailboxAttrJunk:
			return model.KindSpam, name
		case imap.MailboxAttrArchive:
			return model.KindArchive, name
		}
	}

	switch {
	case strings.EqualFold(name, "INBOX"):
		return model.KindInbox, name
	case strings.EqualFold(name, "Sent"):
		return model.KindSent, name
	case strings.EqualFold(name, "Drafts"):
		return model.KindDrafts, name
	case strings.EqualFold(name, "Trash"):
		return model.KindTrash, name
	case strings.EqualFold(name, "Spam"):
		return model.KindSpam, name
	case strings.EqualFold(name, "Archive"):
		return model.KindArchive, name
	}
	return model.KindFolder, name
}

// hasAttr reports whether attrs contains want (attributes are case-insensitive).
func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if strings.EqualFold(string(a), string(want)) {
			return true
		}
	}
	return false
}
