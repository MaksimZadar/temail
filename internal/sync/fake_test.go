package sync

import (
	"context"
	"errors"
	"fmt"
	"net"
	gosync "sync"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
)

// fakeMsg is one message on the fake server.
type fakeMsg struct {
	subject string
	flags   []string
	date    time.Time
	text    string
	html    string
	atts    []model.Attachment
}

// fakeBox is one mailbox on the fake server.
type fakeBox struct {
	kind     model.MailboxKind
	validity uint32
	next     uint32
	msgs     map[uint32]*fakeMsg
}

// fakeBridge implements mail.Client in memory. It records every fetch so tests
// can assert what was (re)fetched, in what order, and how often.
type fakeBridge struct {
	mu    gosync.Mutex
	boxes map[string]*fakeBox

	connected    bool
	connects     int
	connectFails []error // consumed by Connect, one per call
	opFails      []error // consumed by operations; each one drops the connection

	// mailboxErr makes FetchHeaders and ListUIDs fail for a mailbox, without
	// dropping the connection (like a server NO reply).
	mailboxErr map[string]error
	// bodyErr makes FetchMessage fail for "mailbox:uid", without dropping the
	// connection.
	bodyErr map[string]error
	// emptyListing makes ListMailboxes return nothing.
	emptyListing bool
	// zeroValidity makes ListUIDs report UIDVALIDITY 0 for a mailbox.
	zeroValidity  map[string]bool
	listUIDsCalls int

	headerRanges []mail.UIDRange
	headerFetch  map[string]int // "mailbox:uid" -> times fetched
	bodyOrder    []string       // "mailbox:uid" in fetch order
	bodyFetch    map[string]int
	bodyAttempts map[string]int

	// Hooks run after the connection check, outside the lock.
	onHeaders func(rng mail.UIDRange)
	onBody    func(mailbox string, uid uint32)
}

func newFakeBridge() *fakeBridge {
	b := &fakeBridge{
		boxes:        map[string]*fakeBox{},
		headerFetch:  map[string]int{},
		bodyFetch:    map[string]int{},
		bodyAttempts: map[string]int{},
		bodyErr:      map[string]error{},
		mailboxErr:   map[string]error{},
		zeroValidity: map[string]bool{},
	}
	b.addBox("INBOX", model.KindInbox, 1)
	return b
}

func (b *fakeBridge) addBox(name string, kind model.MailboxKind, validity uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.boxes[name] = &fakeBox{kind: kind, validity: validity, next: 1, msgs: map[uint32]*fakeMsg{}}
}

// appendMsg adds a message dated base+uid hours and returns its UID.
func (b *fakeBridge) appendMsg(mailbox string, flags ...string) uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	box := b.boxes[mailbox]
	uid := box.next
	box.next++
	box.msgs[uid] = &fakeMsg{
		subject: fmt.Sprintf("%s msg %d", mailbox, uid),
		flags:   flags,
		date:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(uid) * time.Hour),
		text:    fmt.Sprintf("body of %d", uid),
		html:    fmt.Sprintf("<p>body of %d</p>", uid),
		atts:    []model.Attachment{{Filename: "f.pdf", ContentType: "application/pdf", Size: 10, PartID: "2"}},
	}
	return uid
}

func (b *fakeBridge) expunge(mailbox string, uid uint32) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.boxes[mailbox].msgs, uid)
}

func (b *fakeBridge) setFlags(mailbox string, uid uint32, flags ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.boxes[mailbox].msgs[uid].flags = flags
}

// recreate empties a mailbox and gives it a new UIDVALIDITY, as Bridge does
// when a mailbox is rebuilt.
func (b *fakeBridge) recreate(mailbox string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	box := b.boxes[mailbox]
	box.validity++
	box.next = 1
	box.msgs = map[uint32]*fakeMsg{}
}

func (b *fakeBridge) gate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.connected {
		return mail.ErrNotConnected
	}
	if len(b.opFails) > 0 {
		err := b.opFails[0]
		b.opFails = b.opFails[1:]
		b.connected = false
		return err
	}
	return nil
}

func (b *fakeBridge) Connect(ctx context.Context, _ config.BridgeAccount, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connects++
	b.connected = false
	if len(b.connectFails) > 0 {
		err := b.connectFails[0]
		b.connectFails = b.connectFails[1:]
		return err
	}
	b.connected = true
	return nil
}

func (b *fakeBridge) ListMailboxes(ctx context.Context) ([]model.Mailbox, error) {
	if err := b.gate(ctx); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []model.Mailbox
	if b.emptyListing {
		return out, nil
	}
	for name, box := range b.boxes {
		out = append(out, model.Mailbox{
			Name: name, DisplayName: name, Kind: box.kind, UIDValidity: box.validity,
			Messages: uint32(len(box.msgs)),
		})
	}
	return out, nil
}

func (b *fakeBridge) MailboxInfo(ctx context.Context, mailbox string) (mail.MailboxInfo, error) {
	return mail.MailboxInfo{}, errors.New("not used by sync")
}

func (b *fakeBridge) FetchHeaders(ctx context.Context, mailbox string, rng mail.UIDRange) ([]model.MessageHeader, error) {
	if err := b.gate(ctx); err != nil {
		return nil, err
	}
	if b.onHeaders != nil {
		b.onHeaders(rng)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.mailboxErr[mailbox]; err != nil {
		return nil, err
	}
	b.headerRanges = append(b.headerRanges, rng)
	box := b.boxes[mailbox]
	var out []model.MessageHeader
	for uid := uint32(1); uid < box.next; uid++ {
		m, ok := box.msgs[uid]
		if !ok || uid < rng.First || (rng.Last != 0 && uid > rng.Last) {
			continue
		}
		b.headerFetch[fmt.Sprintf("%s:%d", mailbox, uid)]++
		out = append(out, model.MessageHeader{
			Mailbox: mailbox, UIDValidity: box.validity, UID: uid,
			MessageID: fmt.Sprintf("<%s-%d@example.org>", mailbox, uid),
			Subject:   m.subject, Date: m.date,
			From:  []model.Address{{Email: "a@example.org"}},
			Flags: append([]string(nil), m.flags...),
		})
	}
	return out, nil
}

func (b *fakeBridge) ListUIDs(ctx context.Context, mailbox string) (mail.UIDListing, error) {
	if err := b.gate(ctx); err != nil {
		return mail.UIDListing{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listUIDsCalls++
	if err := b.mailboxErr[mailbox]; err != nil {
		return mail.UIDListing{}, err
	}
	box := b.boxes[mailbox]
	out := mail.UIDListing{UIDValidity: box.validity}
	if b.zeroValidity[mailbox] {
		out.UIDValidity = 0
	}
	for uid := uint32(1); uid < box.next; uid++ {
		if m, ok := box.msgs[uid]; ok {
			out.Messages = append(out.Messages, mail.UIDState{UID: uid, Flags: append([]string(nil), m.flags...)})
		}
	}
	return out, nil
}

func (b *fakeBridge) FetchMessage(ctx context.Context, mailbox string, uid uint32) (model.Message, error) {
	if err := b.gate(ctx); err != nil {
		return model.Message{}, err
	}
	if b.onBody != nil {
		b.onBody(mailbox, uid)
	}
	if err := ctx.Err(); err != nil {
		return model.Message{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	key := fmt.Sprintf("%s:%d", mailbox, uid)
	box := b.boxes[mailbox]
	m, ok := box.msgs[uid]
	b.bodyAttempts[key]++
	if err := b.bodyErr[key]; err != nil {
		return model.Message{}, err
	}
	if !ok {
		return model.Message{}, fmt.Errorf("%w: UID %d in %s", mail.ErrMessageNotFound, uid, mailbox)
	}
	b.bodyFetch[key]++
	b.bodyOrder = append(b.bodyOrder, key)
	return model.Message{
		MessageHeader: model.MessageHeader{
			Mailbox: mailbox, UIDValidity: box.validity, UID: uid, Subject: m.subject, Date: m.date,
		},
		TextBody:    m.text,
		HTMLBody:    m.html,
		Attachments: append([]model.Attachment(nil), m.atts...),
	}, nil
}

func (b *fakeBridge) FetchAttachment(ctx context.Context, mailbox string, uid uint32, partID string) ([]byte, error) {
	return nil, errors.New("sync must never fetch attachment content")
}

func (b *fakeBridge) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connected = false
	return nil
}

// transportErr is a connection-level failure (a refused dial).
func transportErr() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
}

// bodyFetchAttempts counts FetchMessage calls for "mailbox:uid", successful or not.
func (b *fakeBridge) bodyFetchAttempts(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bodyAttempts[key]
}

func (b *fakeBridge) headerFetches(mailbox string, uid uint32) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.headerFetch[fmt.Sprintf("%s:%d", mailbox, uid)]
}

var _ mail.Client = (*fakeBridge)(nil)
