package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/store"
	mailsync "github.com/MaksimZadar/temail/internal/sync"
)

// MaxBatch caps the messages one Action may carry. Batch actions are one write
// per source mailbox, and the cap keeps a single call polite to Bridge.
const MaxBatch = 100

// DefaultChunkDelay is the pause after each mailbox write, so a batch does not
// flood Bridge.
const DefaultChunkDelay = 100 * time.Millisecond

// DefaultVerifyDelay and DefaultVerifyRetries bound the recheck of a source
// mailbox after a MOVE, whose removal is eventual.
const (
	DefaultVerifyDelay   = time.Second
	DefaultVerifyRetries = 2
)

// Type is an allowlisted action type. The set is closed; anything else is
// rejected.
type Type string

const (
	MarkRead    Type = "mark_read"
	MarkUnread  Type = "mark_unread"
	Star        Type = "star"
	Unstar      Type = "unstar"
	Archive     Type = "archive"
	Move        Type = "move"
	Trash       Type = "trash"
	Spam        Type = "spam"
	NotSpam     Type = "not_spam"
	AddLabel    Type = "add_label"
	RemoveLabel Type = "remove_label"
)

// Params are the typed arguments of an action. Exactly one field is used, by
// action type. UndoOf is set only on an inverse action created by Undo; it lets
// the inverse move back to a system folder, which the public Move action does
// not allow.
type Params struct {
	Folder string `json:"folder,omitempty"`
	Label  string `json:"label,omitempty"`
	UndoOf int64  `json:"undoOf,omitempty"`
}

// Action is one batch: one type, its parameters, and up to MaxBatch stable
// message ids. A message id is the Proton internal id (the store's mid), never
// a UID: UIDs are resolved from the cache at apply time.
type Action struct {
	Type       Type
	Params     Params
	MessageIDs []string
}

// Failure is one message the action did not apply to. Message is safe to show:
// it describes the write or the verification, not message content.
type Failure struct {
	MID     string `json:"mid"`
	Mailbox string `json:"mailbox"`
	Message string `json:"message"`
}

// Result is the outcome of one action.
type Result struct {
	Applied []string  `json:"applied"`
	Failed  []Failure `json:"failed"`
}

// Inverse is the action that undoes one applied action. An empty Type means the
// action cannot be undone.
type Inverse struct {
	Type   string `json:"type,omitempty"`
	Params Params `json:"params,omitempty"`
}

// CacheStore is the slice of store.Store that actions needs.
type CacheStore interface {
	GetSyncState(ctx context.Context, account string) ([]store.MailboxState, error)
	MembershipOfMessages(ctx context.Context, account string, mids []string) (map[string][]store.Placement, error)
	FlagsOfMessages(ctx context.Context, account string, mids []string) (map[string][]string, error)
	ApplyActionUpdates(ctx context.Context, account string, updates []store.ActionUpdate) error
}

// Writer is the write-capable connection. mail.Writer satisfies it.
type Writer interface {
	Connect(ctx context.Context, acct config.BridgeAccount, password string) error
	StoreFlags(ctx context.Context, mailbox string, uids []uint32, add, remove []string) error
	Move(ctx context.Context, mailbox string, uids []uint32, target string) (mail.CopyUID, error)
	Copy(ctx context.Context, mailbox string, uids []uint32, target string) (mail.CopyUID, error)
	ExpungeDeleted(ctx context.Context, mailbox string, uids []uint32) error
	SearchInternalID(ctx context.Context, mailbox, internalID string) ([]uint32, error)
	FetchFlags(ctx context.Context, mailbox string, uids []uint32) (map[uint32][]string, error)
	Close() error
}

// AuthLatch is the one-strike login latch shared with internal/sync. A refused
// login is not retried with the same credentials. *sync.AuthLatch satisfies it.
type AuthLatch interface {
	Check(key string) error
	Trip(key string, err error)
}

// Options configures an Executor.
type Options struct {
	// Account keys the cache rows and audit entries.
	Account string
	// Resolve returns the Bridge account and password for a (re)connect. It is
	// called lazily, so a read-only UI session never logs in for actions.
	Resolve func(context.Context) (config.BridgeAccount, string, error)
	// Auth is the latch shared by every login path of this account (the sync
	// connection, the watcher and actions). nil means no latch.
	Auth AuthLatch
	// ChunkDelay is the pause between mailbox writes; zero means 100 ms.
	ChunkDelay time.Duration
	// VerifyDelay and VerifyRetries bound the source-removal recheck after a
	// MOVE. Zero means 1 s and 2 retries.
	VerifyDelay   time.Duration
	VerifyRetries int
	// Logf receives one line per failed write or verification. nil means
	// log.Printf. Failures also appear in the Result.
	Logf func(format string, args ...any)
	// Now returns the clock; nil means time.Now.
	Now func() time.Time
}

// Executor applies actions for one account. All writes are serialised on one
// connection.
type Executor struct {
	st    CacheStore
	w     Writer
	audit *Audit
	opts  Options

	mu        sync.Mutex
	connected bool
	sleep     func(context.Context, time.Duration) error
}

// New returns an Executor. The store, writer and audit log are required.
func New(st CacheStore, w Writer, audit *Audit, opts Options) (*Executor, error) {
	if st == nil || w == nil || audit == nil {
		return nil, errors.New("actions: store, writer and audit log are required")
	}
	if opts.Account == "" {
		return nil, errors.New("actions: account is required")
	}
	if opts.Resolve == nil {
		return nil, errors.New("actions: a credential resolver is required")
	}
	if opts.ChunkDelay < 0 || opts.VerifyDelay < 0 || opts.VerifyRetries < 0 {
		return nil, errors.New("actions: delays and retries must not be negative")
	}
	if opts.ChunkDelay == 0 {
		opts.ChunkDelay = DefaultChunkDelay
	}
	if opts.VerifyDelay == 0 {
		opts.VerifyDelay = DefaultVerifyDelay
	}
	if opts.VerifyRetries == 0 {
		opts.VerifyRetries = DefaultVerifyRetries
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	e := &Executor{st: st, w: w, audit: audit, opts: opts, sleep: sleepCtx}
	return e, nil
}

// Close closes the write connection. It does not close the audit log.
func (e *Executor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.connected = false
	return e.w.Close()
}

// Validate checks an action's type, parameters and batch size without touching
// the cache or the network. The services call it at the trust boundary.
func Validate(a Action) error {
	switch a.Type {
	case MarkRead, MarkUnread, Star, Unstar, Archive, Trash, Spam, NotSpam:
		if a.Params.Folder != "" || a.Params.Label != "" {
			return fmt.Errorf("%w: %s takes no parameters", ErrInvalidAction, a.Type)
		}
	case Move:
		if err := validMailbox(a.Params.Folder); err != nil {
			return fmt.Errorf("%w: move: %v", ErrInvalidAction, err)
		}
		if a.Params.UndoOf == 0 && !strings.HasPrefix(a.Params.Folder, "Folders/") {
			return fmt.Errorf("%w: move target must be a folder (Folders/<name>)", ErrInvalidAction)
		}
	case AddLabel, RemoveLabel:
		if err := validLabel(a.Params.Label); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidAction, a.Type, err)
		}
		if a.Params.Folder != "" {
			return fmt.Errorf("%w: %s takes a label, not a folder", ErrInvalidAction, a.Type)
		}
	default:
		return fmt.Errorf("%w: unknown action %q", ErrInvalidAction, a.Type)
	}
	if len(a.MessageIDs) == 0 {
		return fmt.Errorf("%w: at least one message id is required", ErrInvalidAction)
	}
	if len(normalizeIDs(a.MessageIDs)) > MaxBatch {
		return fmt.Errorf("%w: at most %d message ids per action", ErrInvalidAction, MaxBatch)
	}
	for _, id := range a.MessageIDs {
		if id != "" && strings.ContainsFunc(id, unicode.IsControl) {
			return fmt.Errorf("%w: message id contains a control character", ErrInvalidAction)
		}
	}
	return nil
}

// ErrInvalidAction marks an input that the action layer refuses.
var ErrInvalidAction = errors.New("invalid action")

func validMailbox(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mailbox is required")
	}
	if len(name) > 1024 {
		return errors.New("mailbox name is too long")
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("mailbox name contains a control character")
	}
	return nil
}

func validLabel(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("label is required")
	}
	if len(name) > 128 {
		return errors.New("label name is too long")
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return errors.New("label name contains a control character")
	}
	return nil
}

// Apply runs one action batch. Per-message failures are in Result.Failed; the
// error is reserved for a refused action, a cancelled context, or a failure to
// reach Bridge before any write. A write-level failure marks its messages as
// failed and is also logged.
func (e *Executor) Apply(ctx context.Context, a Action) (Result, error) {
	if err := Validate(a); err != nil {
		return Result{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.execute(ctx, a, normalizeIDs(a.MessageIDs), 0)
}

// Undo applies the inverse of audit entry id. It resolves the message's current
// UID from the cache again, so it works after UIDs changed. The original entry
// is marked undone only when the inverse is verified.
func (e *Executor) Undo(ctx context.Context, id int64) (Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry, err := e.audit.Get(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if entry.Account != e.opts.Account {
		return Result{}, fmt.Errorf("%w: entry %d", ErrWrongAccount, id)
	}
	if entry.Undone {
		return Result{}, ErrAlreadyUndone
	}
	if entry.InverseJSON == "" || entry.InverseJSON == "{}" {
		return Result{}, ErrNotUndoable
	}
	var inv Inverse
	if err := json.Unmarshal([]byte(entry.InverseJSON), &inv); err != nil {
		return Result{}, fmt.Errorf("decode inverse of action %d: %w", id, err)
	}
	if inv.Type == "" {
		return Result{}, ErrNotUndoable
	}
	inv.Params.UndoOf = id
	a := Action{Type: Type(inv.Type), Params: inv.Params, MessageIDs: []string{entry.MID}}
	if err := Validate(a); err != nil {
		return Result{}, err
	}
	res, err := e.execute(ctx, a, []string{entry.MID}, id)
	if err != nil {
		return res, err
	}
	if len(res.Applied) == 0 {
		return res, nil
	}
	if err := e.audit.MarkUndone(ctx, id, e.opts.Now()); err != nil {
		return res, fmt.Errorf("mark action %d undone: %w", id, err)
	}
	return res, nil
}

// UndoLast undoes the newest verified action that is not undone yet.
func (e *Executor) UndoLast(ctx context.Context) (Result, error) {
	e.mu.Lock()
	entry, err := e.audit.LastUndoable(ctx)
	e.mu.Unlock()
	if err != nil {
		return Result{}, err
	}
	return e.Undo(ctx, entry.ID)
}

// Recent returns the newest audit entries, newest first.
func (e *Executor) Recent(ctx context.Context, limit int) ([]Entry, error) {
	return e.audit.Recent(ctx, limit)
}

// normalizeIDs trims blanks and removes duplicates, preserving input order.
func normalizeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// connect ensures the writer is logged in. A refused login trips the shared
// latch; a later call with the same credentials returns the latched error.
func (e *Executor) connect(ctx context.Context) error {
	if e.connected {
		return nil
	}
	acct, pw, err := e.opts.Resolve(ctx)
	if err != nil {
		return err
	}
	key := mailsync.CredsKey(acct, pw)
	if e.opts.Auth != nil {
		if err := e.opts.Auth.Check(key); err != nil {
			return err
		}
	}
	if err := e.w.Connect(ctx, acct, pw); err != nil {
		if e.opts.Auth != nil {
			e.opts.Auth.Trip(key, err)
		}
		return err
	}
	e.connected = true
	return nil
}

// drop forgets a broken connection, so the next call reconnects.
func (e *Executor) drop() {
	e.connected = false
	_ = e.w.Close()
}

// writeErr forgets a broken connection, so the next call reconnects. An auth
// failure also trips the latch on the connect path; the caller stops the batch.
func (e *Executor) writeErr(error) {
	e.drop()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
