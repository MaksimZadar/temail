package actions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
)

// fakeMsg is one message entity with Proton's placement model: the same mid has
// a different UID in every mailbox it is in, and \Seen/\Flagged are global.
type fakeMsg struct {
	mid        string
	flags      map[string]bool
	placements map[string]uint32
}

// fakeWriter is a stateful in-memory writer that models the researched Bridge
// semantics, including the traps that matter to actions.
type fakeWriter struct {
	mu      sync.Mutex
	boxes   map[string]bool
	nextUID map[string]uint32
	msgs    map[string]*fakeMsg

	// traps
	refuseMoveFromAllMail bool
	refuseMoveToArchive   bool
	noCopyUID             bool
	dropCopyPlacement     bool
	// moveLag postpones the removal from the source for this many FetchFlags
	// calls, modelling Bridge's eventual source removal.
	moveLag int
	pending []pendingMove

	calls []string
}

type pendingMove struct {
	mid       string
	mailbox   string
	callsLeft int
}

func newFakeWriter(boxes []string) *fakeWriter {
	f := &fakeWriter{
		boxes:                 map[string]bool{},
		nextUID:               map[string]uint32{},
		msgs:                  map[string]*fakeMsg{},
		refuseMoveFromAllMail: true,
	}
	for _, b := range boxes {
		f.boxes[b] = true
	}
	return f
}

func (f *fakeWriter) alloc(mailbox string) uint32 {
	f.nextUID[mailbox]++
	return f.nextUID[mailbox]
}

func (f *fakeWriter) addMessage(mid string, flags []string, mailboxes ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &fakeMsg{mid: mid, flags: map[string]bool{}, placements: map[string]uint32{}}
	for _, flag := range flags {
		m.flags[flag] = true
	}
	for _, mb := range mailboxes {
		f.boxes[mb] = true
		m.placements[mb] = f.alloc(mb)
	}
	f.msgs[mid] = m
}

func (f *fakeWriter) msgAt(mailbox string, uid uint32) *fakeMsg {
	for _, m := range f.msgs {
		if m.placements[mailbox] == uid {
			return m
		}
	}
	return nil
}

func (f *fakeWriter) Connect(context.Context, config.BridgeAccount, string) error { return nil }

func (f *fakeWriter) StoreFlags(_ context.Context, mailbox string, uids []uint32, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.boxes[mailbox] {
		return fmt.Errorf("no such mailbox %s", mailbox)
	}
	f.calls = append(f.calls, "STORE "+mailbox)
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			return fmt.Errorf("no UID %d in %s", uid, mailbox)
		}
		for _, flag := range add {
			m.flags[flag] = true
		}
		for _, flag := range remove {
			delete(m.flags, flag)
		}
	}
	return nil
}

func (f *fakeWriter) Move(_ context.Context, mailbox string, uids []uint32, target string) (mail.CopyUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.boxes[mailbox] {
		return mail.CopyUID{}, fmt.Errorf("no such mailbox %s", mailbox)
	}
	if f.refuseMoveFromAllMail && mailbox == "All Mail" {
		return mail.CopyUID{}, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "MOVE from All Mail is not allowed"}
	}
	if f.refuseMoveToArchive && target == "Archive" {
		return mail.CopyUID{}, &imap.Error{Type: imap.StatusResponseTypeNo, Text: "no"}
	}
	seen := map[string]bool{}
	var out mail.CopyUID
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			return mail.CopyUID{}, fmt.Errorf("no UID %d in %s", uid, mailbox)
		}
		out.SourceUIDs = append(out.SourceUIDs, uid)
		dest := f.alloc(target)
		m.placements[target] = dest
		out.DestUIDs = append(out.DestUIDs, dest)
		if !seen[m.mid] {
			seen[m.mid] = true
			if f.moveLag > 0 {
				f.pending = append(f.pending, pendingMove{mid: m.mid, mailbox: mailbox, callsLeft: f.moveLag})
			} else {
				delete(m.placements, mailbox)
			}
		}
	}
	f.calls = append(f.calls, "MOVE "+mailbox+"->"+target)
	if f.noCopyUID {
		out = mail.CopyUID{}
	}
	return out, nil
}

func (f *fakeWriter) Copy(_ context.Context, mailbox string, uids []uint32, target string) (mail.CopyUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.boxes[mailbox] {
		return mail.CopyUID{}, fmt.Errorf("no such mailbox %s", mailbox)
	}
	var out mail.CopyUID
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			return mail.CopyUID{}, fmt.Errorf("no UID %d in %s", uid, mailbox)
		}
		out.SourceUIDs = append(out.SourceUIDs, uid)
		dest := f.alloc(target)
		if !f.dropCopyPlacement {
			m.placements[target] = dest
		}
		out.DestUIDs = append(out.DestUIDs, dest)
	}
	f.calls = append(f.calls, "COPY "+mailbox+"->"+target)
	if f.noCopyUID {
		out = mail.CopyUID{}
	}
	return out, nil
}

// ExpungeDeleted models the label semantics: it removes the placement in the one
// mailbox and never the message from the others.
func (f *fakeWriter) ExpungeDeleted(_ context.Context, mailbox string, uids []uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "EXPUNGE "+mailbox)
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			continue
		}
		delete(m.placements, mailbox)
	}
	return nil
}

func (f *fakeWriter) SearchInternalID(_ context.Context, mailbox, internalID string) ([]uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.msgs[internalID]
	if m == nil {
		return nil, nil
	}
	if uid, ok := m.placements[mailbox]; ok {
		return []uint32{uid}, nil
	}
	return nil, nil
}

func (f *fakeWriter) FetchFlags(_ context.Context, mailbox string, uids []uint32) (map[uint32][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.boxes[mailbox] {
		return nil, fmt.Errorf("no such mailbox %s", mailbox)
	}
	// Advance eventual source removal.
	if len(f.pending) > 0 {
		var still []pendingMove
		for _, p := range f.pending {
			p.callsLeft--
			if p.callsLeft <= 0 {
				if m := f.msgs[p.mid]; m != nil {
					delete(m.placements, p.mailbox)
				}
				continue
			}
			still = append(still, p)
		}
		f.pending = still
	}
	out := map[uint32][]string{}
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			continue
		}
		var flags []string
		for flag := range m.flags {
			flags = append(flags, flag)
		}
		sort.Strings(flags)
		out[uid] = flags
	}
	return out, nil
}

func (f *fakeWriter) Close() error { return nil }

func (f *fakeWriter) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeWriter) callCount(prefix string) int {
	n := 0
	for _, c := range f.callList() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeWriter) flagsOf(mid string, flag string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.msgs[mid]
	return m != nil && m.flags[flag]
}

func (f *fakeWriter) placement(mid, mailbox string) (uint32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.msgs[mid]
	if m == nil {
		return 0, false
	}
	uid, ok := m.placements[mailbox]
	return uid, ok
}

func (f *fakeWriter) mailboxesOf(mid string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.msgs[mid]
	if m == nil {
		return nil
	}
	var out []string
	for mb := range m.placements {
		out = append(out, mb)
	}
	sort.Strings(out)
	return out
}

var _ = errors.Is
