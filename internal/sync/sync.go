// Package sync copies mail from Proton Bridge into the local cache (internal/store).
//
// A run has four steps, in order:
//
//  1. Mailboxes: list the server's mailboxes and upsert them. A changed
//     UIDVALIDITY purges that mailbox's cache (done by the store).
//  2. Headers, per mailbox: fetch the UIDs that are on the server but not in
//     the cache, newest first, in windows of mail.BatchSize. Each window is
//     committed before the next is fetched.
//  3. Reconcile, per mailbox: delete cached messages the server no longer has,
//     and copy the server's flags into the cache.
//  4. Bodies: fetch bodies of cached messages that have none, newest first,
//     in small batches with a delay between fetches.
//
// Sync never writes to the mailbox. It uses only the read-only mail.Client
// operations. A Syncer uses one connection and is not safe for concurrent Run
// calls. See README.md for the resume semantics.
package sync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// Defaults for Options fields left at zero.
const (
	DefaultBodyBatchSize = 10
	DefaultBodyDelay     = 500 * time.Millisecond
	DefaultRetries       = 3
	DefaultBackoff       = time.Second
)

// State names the phase of a run, as reported in Progress.
type State string

const (
	StateConnecting  State = "connecting"
	StateMailboxes   State = "mailboxes"
	StateHeaders     State = "headers"
	StateReconciling State = "reconciling"
	StateBodies      State = "bodies"
	StateDone        State = "done"
)

// Progress is one progress event. Mailbox is empty for account-level states.
// Headers counts are per mailbox and are about the current run's work. Bodies
// counts are per mailbox: BodiesDone is the cached bodies and BodiesTotal the
// cached messages.
type Progress struct {
	Account      string
	State        State
	Mailbox      string
	HeadersDone  int
	HeadersTotal int
	BodiesDone   int
	BodiesTotal  int
}

// ProgressFunc receives progress events on the goroutine that runs Sync.
type ProgressFunc func(Progress)

// ConnectFunc connects the client to Bridge. In the app and cmd/mailsync it
// wraps mail.Client.Connect with the account and password.
type ConnectFunc func(ctx context.Context) error

// Options configures a Syncer. Zero fields take the Default* values.
type Options struct {
	// Account is the Bridge login ID; it keys all cached rows.
	Account string
	// BodyBatchSize is how many messages are read from the cache per body batch.
	BodyBatchSize int
	// BodyDelay is waited between body fetches, so Bridge is not hammered.
	BodyDelay time.Duration
	// Retries is how many times a failed operation is retried after
	// reconnecting.
	Retries int
	// Backoff is the delay before the first retry. It doubles, up to 30 s.
	Backoff time.Duration
	// OnProgress, if set, receives progress events.
	OnProgress ProgressFunc
}

// Result summarises one completed or interrupted run.
type Result struct {
	Mailboxes      int
	HeadersFetched int
	Deleted        int
	BodiesFetched  int
	BodiesFailed   int
}

// Syncer runs syncs for one account. Create it with New.
type Syncer struct {
	store   store.Store
	client  mail.Client
	connect ConnectFunc
	opts    Options
	sleep   func(context.Context, time.Duration) error
}

// New returns a Syncer. It does not connect; Run does.
func New(st store.Store, client mail.Client, connect ConnectFunc, opts Options) (*Syncer, error) {
	if st == nil || client == nil || connect == nil {
		return nil, errors.New("sync: store, client and connect are required")
	}
	if opts.Account == "" {
		return nil, errors.New("sync: account is required")
	}
	if opts.BodyBatchSize <= 0 {
		opts.BodyBatchSize = DefaultBodyBatchSize
	}
	if opts.BodyDelay < 0 {
		return nil, errors.New("sync: body delay must not be negative")
	}
	if opts.BodyDelay == 0 {
		opts.BodyDelay = DefaultBodyDelay
	}
	if opts.Retries < 0 {
		return nil, errors.New("sync: retries must not be negative")
	}
	if opts.Retries == 0 {
		opts.Retries = DefaultRetries
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	return &Syncer{store: st, client: client, connect: connect, opts: opts, sleep: defaultSleep}, nil
}

// Run performs a full sync: mailboxes, headers, reconcile and bodies. Every
// step commits its work as it goes, so an error or a cancelled context leaves
// the cache consistent. Calling Run again continues from there.
func (s *Syncer) Run(ctx context.Context) (Result, error) {
	var res Result
	s.emit(Progress{Account: s.opts.Account, State: StateConnecting})
	if err := s.connectWithRetry(ctx); err != nil {
		return res, err
	}
	defer func() { _ = s.client.Close() }()

	s.emit(Progress{Account: s.opts.Account, State: StateMailboxes})
	var boxes []model.Mailbox
	if err := s.call(ctx, "list mailboxes", func(ctx context.Context) (err error) {
		boxes, err = s.client.ListMailboxes(ctx)
		return err
	}); err != nil {
		return res, err
	}
	boxes = orderMailboxes(boxes)
	if err := s.store.UpsertMailboxes(ctx, s.opts.Account, boxes); err != nil {
		return res, fmt.Errorf("cache mailboxes: %w", err)
	}
	names := make([]string, 0, len(boxes))
	for _, b := range boxes {
		names = append(names, b.Name)
	}
	if err := s.store.DeleteMailboxesNotIn(ctx, s.opts.Account, names); err != nil {
		return res, fmt.Errorf("remove deleted mailboxes: %w", err)
	}
	res.Mailboxes = len(boxes)

	for _, b := range boxes {
		if err := s.syncMailbox(ctx, b, &res); err != nil {
			return res, err
		}
	}
	if err := s.syncBodies(ctx, &res); err != nil {
		return res, err
	}
	s.emit(Progress{Account: s.opts.Account, State: StateDone})
	return res, nil
}

// syncMailbox runs steps 2 and 3 for one mailbox.
func (s *Syncer) syncMailbox(ctx context.Context, box model.Mailbox, res *Result) error {
	var listing mail.UIDListing
	if err := s.call(ctx, "list UIDs of "+box.Name, func(ctx context.Context) (err error) {
		listing, err = s.client.ListUIDs(ctx, box.Name)
		return err
	}); err != nil {
		return err
	}
	epoch := listing.UIDValidity
	if epoch != box.UIDValidity {
		// The epoch changed after ListMailboxes. The store purges on upsert.
		box.UIDValidity = epoch
		if err := s.store.UpsertMailboxes(ctx, s.opts.Account, []model.Mailbox{box}); err != nil {
			return fmt.Errorf("cache mailbox %s: %w", box.Name, err)
		}
	}

	serverUIDs := make([]uint32, 0, len(listing.Messages))
	for _, m := range listing.Messages {
		serverUIDs = append(serverUIDs, m.UID)
	}
	cachedList, err := s.store.CachedUIDs(ctx, s.opts.Account, box.Name, epoch)
	if err != nil {
		return fmt.Errorf("read cache of %s: %w", box.Name, err)
	}
	cached := make(map[uint32]bool, len(cachedList))
	for _, uid := range cachedList {
		cached[uid] = true
	}

	// Step 2: headers, newest first, committed per window.
	chunks, pending := planChunks(serverUIDs, cached, mail.BatchSize)
	done := 0
	s.emitMailbox(StateHeaders, box.Name, done, pending, 0, 0)
	for _, c := range chunks {
		var hdrs []model.MessageHeader
		rng := mail.UIDRange{First: c.First, Last: c.Last}
		if err := s.call(ctx, "fetch headers of "+box.Name, func(ctx context.Context) (err error) {
			hdrs, err = s.client.FetchHeaders(ctx, box.Name, rng)
			return err
		}); err != nil {
			return err
		}
		if err := s.store.UpsertHeaders(ctx, s.opts.Account, hdrs); err != nil {
			return fmt.Errorf("cache headers of %s: %w", box.Name, err)
		}
		res.HeadersFetched += len(hdrs)
		done += c.Count
		s.emitMailbox(StateHeaders, box.Name, done, pending, 0, 0)
	}

	// Step 3: reconcile against the listing taken at the start of this pass.
	s.emitMailbox(StateReconciling, box.Name, pending, pending, 0, 0)
	deleted, err := s.store.DeleteMissing(ctx, s.opts.Account, box.Name, epoch, serverUIDs)
	if err != nil {
		return fmt.Errorf("remove deleted messages of %s: %w", box.Name, err)
	}
	res.Deleted += deleted
	updates := make([]store.FlagUpdate, 0, len(listing.Messages))
	for _, m := range listing.Messages {
		updates = append(updates, store.FlagUpdate{UID: m.UID, Flags: m.Flags})
	}
	if err := s.store.UpdateFlags(ctx, s.opts.Account, box.Name, epoch, updates); err != nil {
		return fmt.Errorf("update flags of %s: %w", box.Name, err)
	}
	if err := s.store.MarkSynced(ctx, s.opts.Account, box.Name, time.Now()); err != nil {
		return fmt.Errorf("record sync of %s: %w", box.Name, err)
	}
	return nil
}

// orderMailboxes puts INBOX first and sorts the rest by name.
func orderMailboxes(boxes []model.Mailbox) []model.Mailbox {
	out := append([]model.Mailbox(nil), boxes...)
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Name == "INBOX", out[j].Name == "INBOX"
		if ai != aj {
			return ai
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Syncer) emit(p Progress) {
	if s.opts.OnProgress != nil {
		s.opts.OnProgress(p)
	}
}

func (s *Syncer) emitMailbox(state State, mailbox string, hdone, htotal, bdone, btotal int) {
	s.emit(Progress{
		Account: s.opts.Account, State: state, Mailbox: mailbox,
		HeadersDone: hdone, HeadersTotal: htotal, BodiesDone: bdone, BodiesTotal: btotal,
	})
}
