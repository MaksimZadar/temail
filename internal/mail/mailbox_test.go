package mail

import (
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/model"
)

func TestClassifyMailbox(t *testing.T) {
	tests := []struct {
		name        string
		mailbox     string
		delim       rune
		attrs       []imap.MailboxAttr
		wantKind    model.MailboxKind
		wantDisplay string
	}{
		{"inbox by name", "INBOX", '/', nil, model.KindInbox, "INBOX"},
		{"inbox case-insensitive", "inbox", '/', nil, model.KindInbox, "inbox"},
		{"sent by name", "Sent", '/', nil, model.KindSent, "Sent"},
		{"drafts by name", "Drafts", '/', nil, model.KindDrafts, "Drafts"},
		{"trash by name", "Trash", '/', nil, model.KindTrash, "Trash"},
		{"spam by name", "Spam", '/', nil, model.KindSpam, "Spam"},
		{"archive by name", "Archive", '/', nil, model.KindArchive, "Archive"},
		{"folder", "Folders/Work", '/', nil, model.KindFolder, "Work"},
		{"nested folder keeps path", "Folders/Work/2026", '/', nil, model.KindFolder, "Work/2026"},
		{"label", "Labels/Important", '/', nil, model.KindLabel, "Important"},
		{"label with spaces", "Labels/To do", '/', nil, model.KindLabel, "To do"},
		{"all mail is a plain folder", "All Mail", '/', nil, model.KindFolder, "All Mail"},
		{"unknown top level is a folder", "Notes", '/', nil, model.KindFolder, "Notes"},
		{"special-use Sent beats name", "Outbox", '/', []imap.MailboxAttr{imap.MailboxAttrSent}, model.KindSent, "Outbox"},
		{"special-use Junk maps to spam", "Bulk", '/', []imap.MailboxAttr{imap.MailboxAttrJunk}, model.KindSpam, "Bulk"},
		{"special-use Archive", "Old", '/', []imap.MailboxAttr{imap.MailboxAttrArchive}, model.KindArchive, "Old"},
		{"special-use Trash", "Bin", '/', []imap.MailboxAttr{imap.MailboxAttrTrash}, model.KindTrash, "Bin"},
		{"special-use Drafts", "Draft", '/', []imap.MailboxAttr{imap.MailboxAttrDrafts}, model.KindDrafts, "Draft"},
		{"\\All does not become a special kind", "Everything", '/', []imap.MailboxAttr{imap.MailboxAttrAll}, model.KindFolder, "Everything"},
		{"\\Flagged does not become a special kind", "Starred", '/', []imap.MailboxAttr{imap.MailboxAttrFlagged}, model.KindFolder, "Starred"},
		{"Folders prefix wins over special-use", "Folders/Archive", '/', []imap.MailboxAttr{imap.MailboxAttrArchive}, model.KindFolder, "Archive"},
		{"other delimiter", "Folders.Work", '.', nil, model.KindFolder, "Work"},
		{"missing delimiter falls back to slash", "Labels/X", 0, nil, model.KindLabel, "X"},
		{"bare Folders is not a folder entry", "Folders", '/', nil, model.KindFolder, "Folders"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, display := classifyMailbox(tc.mailbox, tc.delim, tc.attrs)
			if kind != tc.wantKind || display != tc.wantDisplay {
				t.Errorf("classifyMailbox(%q) = (%s, %q), want (%s, %q)",
					tc.mailbox, kind, display, tc.wantKind, tc.wantDisplay)
			}
		})
	}
}

func TestParsePartID(t *testing.T) {
	ok := map[string][]int{"1": {1}, "2.1": {2, 1}, "10.3.4": {10, 3, 4}}
	for id, want := range ok {
		got, valid := parsePartID(id)
		if !valid || len(got) != len(want) {
			t.Errorf("parsePartID(%q) = %v, %v", id, got, valid)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("parsePartID(%q) = %v, want %v", id, got, want)
			}
		}
	}
	for _, bad := range []string{"", "0", "01", "1.", ".1", "1..2", "1 BODY[]", "a", "1.2.3.4.5.6.7.8.9", "12345"} {
		if _, valid := parsePartID(bad); valid {
			t.Errorf("parsePartID(%q) accepted", bad)
		}
	}
}
