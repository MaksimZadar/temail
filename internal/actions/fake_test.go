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
// semantics, including the traps that matter to actions. It also models the
// connection and the mailbox epoch, so the executor's reconnect and UIDVALIDITY
// handling are covered.
type fakeWriter struct {
	mu        sync.Mutex
	boxes     map[string]bool
	epoch     map[string]uint32
	nextUID   map[string]uint32
	msgs      map[string]*fakeMsg
	connected bool
	connects  int

	// traps
	refuseMoveFromAllMail bool
	refuseMoveToArchive   bool
	noCopyUID             bool
	dropCopyPlacement     bool
	noUIDPLUS             bool
	failIdentity          bool
	failSearch            bool
	failFetch             bool
	multiSearch           map[string]bool
	identityMismatch      map[string]string
	refuseWrites          map[string]bool
	transportWrites       map[string]bool
	copyValidity          uint32
	onStore               func()
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
		epoch:                 map[string]uint32{},
		nextUID:               map[string]uint32{},
		msgs:                  map[string]*fakeMsg{},
		multiSearch:           map[string]bool{},
		identityMismatch:      map[string]string{},
		refuseWrites:          map[string]bool{},
		transportWrites:       map[string]bool{},
		refuseMoveFromAllMail: true,
	}
	for _, b := range boxes {
		f.boxes[b] = true
		f.epoch[b] = 1
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
		if f.epoch[mb] == 0 {
			f.epoch[mb] = 1
		}
		m.placements[mb] = f.alloc(mb)
	}
	f.msgs[mid] = m
}

// bumpEpoch models a UIDVALIDITY reset: every UID in the mailbox is reassigned.
func (f *fakeWriter) bumpEpoch(mailbox string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.epoch[mailbox]++
	f.nextUID[mailbox] = 0
	for _, m := range f.msgs {
		if _, ok := m.placements[mailbox]; ok {
			m.placements[mailbox] = f.alloc(mailbox)
		}
	}
}

func (f *fakeWriter) msgAt(mailbox string, uid uint32) *fakeMsg {
	for _, m := range f.msgs {
		if m.placements[mailbox] == uid {
			return m
		}
	}
	return nil
}

// ready models the connection and epoch checks the real writer does before a
// write. A NO on writes is injected per mailbox to test group robustness.
func (f *fakeWriter) ready(mailbox string, uidValidity uint32) error {
	if !f.connected {
		return mail.ErrNotConnected
	}
	if !f.boxes[mailbox] {
		return fmt.Errorf("no such mailbox %s", mailbox)
	}
	if uidValidity != 0 && f.epoch[mailbox] != uidValidity {
		return fmt.Errorf("%w: %s is now UIDVALIDITY %d, the cache has %d",
			mail.ErrEpochChanged, mailbox, f.epoch[mailbox], uidValidity)
	}
	return nil
}

// writeReady is ready plus the injected write failures.
func (f *fakeWriter) writeReady(mailbox string, uidValidity uint32) error {
	if err := f.ready(mailbox, uidValidity); err != nil {
		return err
	}
	if f.transportWrites[mailbox] {
		return errors.New("connection reset by peer")
	}
	if f.refuseWrites[mailbox] {
		return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "NO"}
	}
	return nil
}

func (f *fakeWriter) Connect(context.Context, config.BridgeAccount, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = true
	f.connects++
	return nil
}

func (f *fakeWriter) StoreFlags(ctx context.Context, mailbox string, uidValidity uint32, uids []uint32, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onStore != nil {
		f.onStore()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.writeReady(mailbox, uidValidity); err != nil {
		return err
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

func (f *fakeWriter) Move(_ context.Context, mailbox string, uidValidity uint32, uids []uint32, target string) (mail.CopyUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.writeReady(mailbox, uidValidity); err != nil {
		return mail.CopyUID{}, err
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
	out.UIDValidity = f.epoch[target]
	if f.copyValidity != 0 {
		out.UIDValidity = f.copyValidity
	}
	if f.noCopyUID {
		out = mail.CopyUID{}
	}
	return out, nil
}

func (f *fakeWriter) Copy(_ context.Context, mailbox string, uidValidity uint32, uids []uint32, target string) (mail.CopyUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.writeReady(mailbox, uidValidity); err != nil {
		return mail.CopyUID{}, err
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
	out.UIDValidity = f.epoch[target]
	if f.copyValidity != 0 {
		out.UIDValidity = f.copyValidity
	}
	if f.noCopyUID {
		out = mail.CopyUID{}
	}
	return out, nil
}

// ExpungeDeleted models the label semantics: it removes the placement in the one
// mailbox and never the message from the others.
func (f *fakeWriter) ExpungeDeleted(_ context.Context, mailbox string, uidValidity uint32, uids []uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.writeReady(mailbox, uidValidity); err != nil {
		return err
	}
	if f.noUIDPLUS {
		return mail.ErrExpungeUnsupported
	}
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

func (f *fakeWriter) SearchInternalID(_ context.Context, mailbox string, uidValidity uint32, internalID string) ([]uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(mailbox, uidValidity); err != nil {
		return nil, err
	}
	if f.failSearch {
		return nil, errors.New("search failed")
	}
	m := f.msgs[internalID]
	if m == nil {
		return nil, nil
	}
	uid, ok := m.placements[mailbox]
	if !ok {
		return nil, nil
	}
	if f.multiSearch[internalID] {
		return []uint32{uid, uid + 1000}, nil
	}
	return []uint32{uid}, nil
}

func (f *fakeWriter) FetchInternalIDs(_ context.Context, mailbox string, uidValidity uint32, uids []uint32) (map[uint32]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(mailbox, uidValidity); err != nil {
		return nil, err
	}
	if f.failIdentity {
		return nil, errors.New("identity fetch failed")
	}
	out := map[uint32]string{}
	for _, uid := range uids {
		m := f.msgAt(mailbox, uid)
		if m == nil {
			continue
		}
		id := m.mid
		if wrong, ok := f.identityMismatch[m.mid]; ok {
			id = wrong
		}
		out[uid] = id
	}
	return out, nil
}

func (f *fakeWriter) FetchFlags(_ context.Context, mailbox string, uidValidity uint32, uids []uint32) (map[uint32][]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ready(mailbox, uidValidity); err != nil {
		return nil, err
	}
	if f.failFetch {
		return nil, errors.New("fetch failed")
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
		flags := []string{}
		for flag := range m.flags {
			flags = append(flags, flag)
		}
		sort.Strings(flags)
		out[uid] = flags
	}
	return out, nil
}

func (f *fakeWriter) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
	return nil
}

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

func (f *fakeWriter) connectCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connects
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
