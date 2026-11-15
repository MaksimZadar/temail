package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// Defaults for WatchOptions fields left at zero.
const (
	DefaultWatchInterval = 120 * time.Second // reconcile period while IDLE is active
	DefaultPollInterval  = 30 * time.Second  // reconcile period when IDLE is unavailable
	DefaultDebounce      = 500 * time.Millisecond
	DefaultFlagSweep     = 15 * time.Minute // All Mail flag resync
	DefaultIdleStable    = 2 * time.Minute  // session length that resets the backoff
	maxWatchBackoff      = 5 * time.Minute
)

// Changes is the result of Check: the tracked mailboxes whose server state
// differs from the cache, or Full when the server's mailbox list differs and a
// full Run is needed.
type Changes struct {
	Mailboxes []string
	Full      bool
}

// Check compares the server with the cache. It uses LIST and STATUS only, so it
// never SELECTs a mailbox (All Mail is expensive to select). A mailbox is changed
// when its UIDVALIDITY changed, when UIDNEXT is above the cached highest UID, when
// its message count or unseen count differs from the cache, or when it has not
// synced yet. Flag changes that do not move the unseen count are found by IDLE,
// not here. A STATUS failure that is not a permanent error marks the mailbox
// changed, so its sync reports the error.
func (s *Syncer) Check(ctx context.Context) (Changes, error) {
	if err := s.ensureConnected(ctx); err != nil {
		return Changes{}, err
	}
	var boxes []model.Mailbox
	if err := s.call(ctx, "list mailboxes", func(ctx context.Context) (err error) {
		boxes, err = s.client.ListMailboxes(ctx)
		return err
	}); err != nil {
		return Changes{}, err
	}
	cached, err := s.store.GetSyncState(ctx, s.opts.Account)
	if err != nil {
		return Changes{}, fmt.Errorf("read sync state: %w", err)
	}
	counts, err := s.store.MessageCounts(ctx, s.opts.Account)
	if err != nil {
		return Changes{}, fmt.Errorf("count cached messages: %w", err)
	}

	serverNames := make([]string, 0, len(boxes))
	for _, b := range boxes {
		serverNames = append(serverNames, b.Name)
	}
	cachedNames := make([]string, 0, len(cached))
	for _, c := range cached {
		cachedNames = append(cachedNames, c.Name)
	}
	slices.Sort(serverNames)
	slices.Sort(cachedNames)
	if !slices.Equal(serverNames, cachedNames) {
		return Changes{Full: true}, nil
	}

	var out Changes
	for _, c := range cached {
		var st mail.MailboxStatus
		err := s.call(ctx, "status of "+c.Name, func(ctx context.Context) (err error) {
			st, err = s.client.Status(ctx, c.Name)
			return err
		})
		if err != nil {
			if ctx.Err() != nil || permanent(err) || isTransport(err) {
				return Changes{}, err
			}
			out.Mailboxes = append(out.Mailboxes, c.Name)
			continue
		}
		if mailboxChanged(c, st, counts[c.Name]) {
			out.Mailboxes = append(out.Mailboxes, c.Name)
		}
	}
	return out, nil
}

// mailboxChanged compares one mailbox's STATUS with its cached state. A zero
// server UIDVALIDITY is unknown, so it never counts as a change.
//
// UIDNEXT is compared with the value stored at the last sync, not with the
// highest cached UID. A newest message that was expunged keeps UIDNEXT above
// the highest UID, so comparing with the highest UID would resync that mailbox
// on every check.
//
// The unseen count is not compared for All Mail. Reading a message there moves
// the count of every mailbox that holds it, and All Mail's count is the largest
// listing of all. Its flags are refreshed by the periodic flag sweep instead,
// and by IDLE on INBOX.
func mailboxChanged(c store.MailboxState, st mail.MailboxStatus, cachedMessages int) bool {
	switch {
	case c.LastSync.IsZero():
		return true
	case st.UIDValidity != 0 && st.UIDValidity != c.UIDValidity:
		return true
	case st.UIDNext != 0 && st.UIDNext != c.UIDNext:
		return true
	case st.Messages != uint32(cachedMessages):
		return true
	case c.Name != allMailName && st.Unseen != c.Unseen:
		return true
	}
	return false
}

// allMailName is Proton's All Mail mailbox.
const allMailName = "All Mail"

// RunChanged syncs only the named mailboxes, then the bodies that are missing.
// It is the incremental path: the planner only fetches UIDs that are not cached,
// the reconcile removes what is gone, and the body queue only has new messages.
// The error rules match Run.
func (s *Syncer) RunChanged(ctx context.Context, names []string) (Result, error) {
	var res Result
	if err := s.ensureConnected(ctx); err != nil {
		return res, err
	}
	states, err := s.store.GetSyncState(ctx, s.opts.Account)
	if err != nil {
		return res, fmt.Errorf("read sync state: %w", err)
	}
	byName := make(map[string]store.MailboxState, len(states))
	for _, st := range states {
		byName[st.Name] = st
	}
	for _, name := range names {
		st, ok := byName[name]
		if !ok {
			continue // removed since the check; the next Full run handles it
		}
		box := model.Mailbox{Name: st.Name, DisplayName: st.DisplayName, Kind: st.Kind, UIDValidity: st.UIDValidity}
		if err := s.syncMailbox(ctx, box, &res); err != nil {
			if fatal(ctx, err) {
				return res, err
			}
			res.MailboxErrors = append(res.MailboxErrors, MailboxError{Mailbox: name, Err: err})
		}
	}
	if err := s.syncBodies(ctx, &res); err != nil {
		return res, err
	}
	if len(res.MailboxErrors) > 0 {
		return res, ErrPartial
	}
	return res, nil
}

// ensureConnected connects if the Syncer has no working connection.
func (s *Syncer) ensureConnected(ctx context.Context) error {
	if s.connected {
		return nil
	}
	return s.connectWithRetry(ctx)
}

// WatchOptions configures a Watcher. Zero fields take the Default* values.
type WatchOptions struct {
	// Interval is the reconcile period while IDLE is active. It is a backstop:
	// IDLE triggers a check sooner.
	Interval time.Duration
	// PollInterval is the reconcile period when IDLE is unavailable.
	PollInterval time.Duration
	// Debounce collects a burst of triggers into one check.
	Debounce time.Duration
	// Backoff is the first delay after a failed check or IDLE connection. It
	// doubles up to five minutes.
	Backoff time.Duration
	// FlagSweepInterval is how often All Mail is resynced for flag changes. It
	// is slow on purpose: All Mail is the largest mailbox, and IDLE on INBOX
	// covers the flags that matter most.
	FlagSweepInterval time.Duration
	// IdleStableAfter is how long an IDLE session must last before its failure
	// resets the backoff. A session that drops at once keeps the backoff growing.
	IdleStableAfter time.Duration
	// OnChanged receives the names of the mailboxes that were synced after a
	// check found changes (all mailboxes after a Full run).
	OnChanged func(mailboxes []string)
}

// Watcher keeps the cache current after the first sync. It runs two things:
//   - an IDLE connection on INBOX that triggers a check on each unsolicited
//     update. All Mail changes are found by the reconcile timer, not by IDLE,
//     because a SELECT of All Mail is expensive;
//   - a reconcile timer that checks every tracked mailbox with STATUS, and a
//     slower flag sweep of All Mail.
//
// Checks run on the Syncer's connection, which the sync leaves open for the
// watcher to reuse. Changed mailboxes are synced incrementally. An auth failure
// is latched (see AuthLatch) and stops the watcher: neither the poll nor the
// IDLE connection tries again with the same credentials. Transport failures back
// off and retry.
type Watcher struct {
	syn         *Syncer
	idler       mail.Watcher // nil: no IDLE (the client does not support it)
	connectIdle ConnectFunc
	opts        WatchOptions
	sleep       func(context.Context, time.Duration) error
	kick        chan struct{} // one pending check request; see Kick

	mu          sync.Mutex
	idle        bool // IDLE has started and is watching
	lastChecked time.Time
	lastSweep   time.Time
}

// NewWatcher returns a Watcher. idler may be nil. connectIdle connects it (with
// the same account and password as the Syncer).
func NewWatcher(syn *Syncer, idler mail.Watcher, connectIdle ConnectFunc, opts WatchOptions) *Watcher {
	if opts.Interval <= 0 {
		opts.Interval = DefaultWatchInterval
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.Debounce <= 0 {
		opts.Debounce = DefaultDebounce
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	if opts.FlagSweepInterval <= 0 {
		opts.FlagSweepInterval = DefaultFlagSweep
	}
	if opts.IdleStableAfter <= 0 {
		opts.IdleStableAfter = DefaultIdleStable
	}
	return &Watcher{syn: syn, idler: idler, connectIdle: connectIdle, opts: opts,
		sleep: defaultSleep, kick: make(chan struct{}, 1)}
}

// Kick asks for a check as soon as the debounce allows. It does not block, and
// a request made while one is already pending is merged with it. The services
// layer calls it when the user asks to sync while the watcher is running.
func (w *Watcher) Kick() {
	select {
	case w.kick <- struct{}{}:
	default:
	}
}

// IdleActive reports whether the IDLE connection is currently watching. It is
// true only after IDLE has started, not while the connection is being made.
func (w *Watcher) IdleActive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.idle
}

// LastChecked returns the time of the last completed check, or the zero time.
func (w *Watcher) LastChecked() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastChecked
}

func (w *Watcher) setIdle(on bool) {
	w.mu.Lock()
	w.idle = on
	w.mu.Unlock()
}

func (w *Watcher) interval() time.Duration {
	if w.IdleActive() {
		return w.opts.Interval
	}
	return w.opts.PollInterval
}

// Run blocks until ctx ends or a permanent error stops the watcher. Every
// goroutine it started has finished when Run returns.
func (w *Watcher) Run(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	fatalCh := make(chan error, 1)
	if w.idler != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.idleLoop(wctx, fatalCh)
		}()
	}
	defer func() {
		cancel()
		wg.Wait()
	}()

	w.mu.Lock()
	w.lastSweep = time.Now()
	w.mu.Unlock()
	backoff := w.opts.Backoff
	for {
		timer := time.NewTimer(w.interval())
		select {
		case <-wctx.Done():
			timer.Stop()
			return ctx.Err()
		case err := <-fatalCh:
			timer.Stop()
			return err
		case <-timer.C:
		case <-w.kick:
			timer.Stop()
		}
		if err := w.sleep(wctx, w.opts.Debounce); err != nil {
			return ctx.Err()
		}
		drain(w.kick)

		changes, err := w.syn.Check(wctx)
		w.mu.Lock()
		w.lastChecked = time.Now()
		sweep := time.Since(w.lastSweep) >= w.opts.FlagSweepInterval
		w.mu.Unlock()
		if err == nil && sweep {
			changes = w.withAllMail(ctx, changes)
		}
		if err == nil {
			err = w.apply(wctx, changes)
		}
		if err == nil {
			if sweep {
				w.mu.Lock()
				w.lastSweep = time.Now()
				w.mu.Unlock()
			}
			backoff = w.opts.Backoff
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if permanent(err) {
			return err
		}
		log.Printf("temail: watch check failed, retrying in %v: %v", backoff, err)
		if serr := w.sleep(wctx, backoff); serr != nil {
			return ctx.Err()
		}
		backoff = min(backoff*2, maxWatchBackoff)
	}
}

// withAllMail adds All Mail to a set of changes, for the flag sweep. It does
// nothing after a full run (which covers every mailbox) or when All Mail is not
// cached (for example when Bridge hides it).
func (w *Watcher) withAllMail(ctx context.Context, changes Changes) Changes {
	if changes.Full || slices.Contains(changes.Mailboxes, allMailName) {
		return changes
	}
	names := w.allNames(ctx)
	if slices.Contains(names, allMailName) {
		changes.Mailboxes = append(changes.Mailboxes, allMailName)
	}
	return changes
}

// apply syncs what a check found and reports it. A partial run counts as
// success: its mailbox errors are kept in the cache state, and the next check
// finds the mailbox still changed.
func (w *Watcher) apply(ctx context.Context, changes Changes) error {
	if !changes.Full && len(changes.Mailboxes) == 0 {
		return nil
	}
	var names []string
	var err error
	if changes.Full {
		_, err = w.syn.Run(ctx)
		names = w.allNames(ctx)
	} else {
		_, err = w.syn.RunChanged(ctx, changes.Mailboxes)
		names = changes.Mailboxes
	}
	if err != nil && !errors.Is(err, ErrPartial) {
		return err
	}
	if w.opts.OnChanged != nil && len(names) > 0 {
		w.opts.OnChanged(names)
	}
	return nil
}

// allNames lists the cached mailbox names, for the notification after a Full run.
func (w *Watcher) allNames(ctx context.Context) []string {
	states, err := w.syn.store.GetSyncState(ctx, w.syn.opts.Account)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(states))
	for _, s := range states {
		out = append(out, s.Name)
	}
	return out
}

// idleLoop keeps the IDLE connection running. It reconnects after transport
// failures with backoff. A session that lasted at least IdleStableAfter resets
// the backoff, so one long session does not leave the next retry at its
// maximum. It stops for good when IDLE is not offered (the poll timer then runs
// faster) and reports permanent errors through fatal.
func (w *Watcher) idleLoop(ctx context.Context, fatal chan<- error) {
	defer func() { _ = w.idler.Close() }()
	defer w.setIdle(false)
	backoff := w.opts.Backoff
	for {
		started := time.Now()
		err := w.idleOnce(ctx)
		w.setIdle(false)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, mail.ErrIdleUnsupported) {
			return
		}
		if permanent(err) {
			select {
			case fatal <- err:
			default:
			}
			return
		}
		if time.Since(started) >= w.opts.IdleStableAfter {
			backoff = w.opts.Backoff
		}
		if serr := w.sleep(ctx, backoff); serr != nil {
			return
		}
		backoff = min(backoff*2, maxWatchBackoff)
	}
}

// idleOnce connects the IDLE connection, then idles on INBOX until the
// connection ends. The auth latch is checked first, so a refused login is not
// tried again, and a refused login trips it. Watching is reported only once IDLE
// has started.
func (w *Watcher) idleOnce(ctx context.Context) error {
	auth := w.syn.opts.Auth
	if err := auth.Check(w.syn.opts.CredsKey); err != nil {
		return err
	}
	if err := w.connectIdle(ctx); err != nil {
		auth.Trip(w.syn.opts.CredsKey, err)
		return err
	}
	if !slices.Contains(w.idler.Capabilities(), "IDLE") {
		return mail.ErrIdleUnsupported
	}
	return w.idler.Idle(ctx, "INBOX",
		func() { w.setIdle(true) },
		func() { w.Kick() })
}

// drain empties a one-slot signal channel, so a burst becomes one check.
func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
