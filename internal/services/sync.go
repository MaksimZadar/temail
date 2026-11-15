package services

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/store"
	mailsync "github.com/MaksimZadar/temail/internal/sync"
)

// SyncService runs internal/sync in the background and reports it to the
// frontend with events. At most one run is active at a time. The run starts
// with the app when an account is ready (in the background, so a locked
// keychain cannot block startup), and again after SetupService changes the
// account or the pin.
//
// A generation counter (gen) guards launches: Stop bumps it, and a launch that
// read its credentials before a Stop or restart drops itself. So a restart
// starts exactly one run, with credentials read after the restart.
//
// Events: sync:progress (ProgressEvent), sync:done (SyncResult, which may list
// MailboxErrors), sync:error (SyncErrorEvent). A run stopped by Stop or by
// shutdown emits no outcome event; Status shows it as not running.
type SyncService struct {
	st        store.Store
	cfg       ConfigStore
	sec       PasswordSource
	newClient ClientFactory
	emitter   Emitter

	// bodyDelay and backoff override the sync defaults. Zero means default.
	bodyDelay time.Duration
	backoff   time.Duration
	// stopWait bounds Stop, and autostartWait bounds the startup launch. Zero
	// means the package default.
	stopWait      time.Duration
	autostartWait time.Duration

	mu           sync.Mutex
	base         context.Context // parent of every run; set by ServiceStartup
	gen          uint64          // bumped by Stop; stale launches drop out
	cancel       context.CancelFunc
	done         chan struct{} // non-nil while a run is active; closed when it has finished emitting
	stopTimedOut bool          // Stop gave up waiting for the active run
	lastResult   *SyncResult
	lastError    string
}

// NewSyncService returns a SyncService. It does not start a run; the app
// starts one through ServiceStartup, or Start is called.
func NewSyncService(st store.Store, cfg ConfigStore, sec PasswordSource, newClient ClientFactory, emitter Emitter) *SyncService {
	return &SyncService{st: st, cfg: cfg, sec: sec, newClient: newClient, emitter: emitter}
}

var (
	_ application.ServiceStartup  = (*SyncService)(nil)
	_ application.ServiceShutdown = (*SyncService)(nil)
)

// SyncStatus is the state of the sync. It contains no secrets.
type SyncStatus struct {
	Running      bool        `json:"running"`
	StopTimedOut bool        `json:"stopTimedOut"`
	LastResult   *SyncResult `json:"lastResult"`
	LastError    string      `json:"lastError"`
}

// SyncResult summarises one run, as in internal/sync.Result.
type SyncResult struct {
	Mailboxes      int               `json:"mailboxes"`
	HeadersFetched int               `json:"headersFetched"`
	Deleted        int               `json:"deleted"`
	BodiesFetched  int               `json:"bodiesFetched"`
	BodiesFailed   int               `json:"bodiesFailed"`
	MailboxErrors  []MailboxErrorDTO `json:"mailboxErrors"`
}

// MailboxErrorDTO is one mailbox that failed during a run.
type MailboxErrorDTO struct {
	Mailbox string `json:"mailbox"`
	Message string `json:"message"`
}

// ProgressEvent is the payload of sync:progress. Counts are per mailbox.
type ProgressEvent struct {
	State        string `json:"state"`
	Mailbox      string `json:"mailbox"`
	HeadersDone  int    `json:"headersDone"`
	HeadersTotal int    `json:"headersTotal"`
	BodiesDone   int    `json:"bodiesDone"`
	BodiesTotal  int    `json:"bodiesTotal"`
}

// SyncErrorEvent is the payload of sync:error.
type SyncErrorEvent struct {
	Message string `json:"message"`
}

// ServiceStartup records the app context and starts the autostart in the
// background. It returns at once, so startup never waits for the keychain.
func (s *SyncService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	go s.autostart()
	return nil
}

// autostart runs start with a deadline. If the credentials read (which may wait
// on a locked keychain) takes too long, the launch is cancelled by bumping gen,
// so it cannot start a run later without the user asking.
func (s *SyncService) autostart() {
	wait := s.autostartWait
	if wait <= 0 {
		wait = autostartWait
	}
	res := make(chan error, 1)
	go func() { res <- s.start() }()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-res:
		if err != nil {
			log.Printf("temail: sync not started: %v", err)
		}
	case <-timer.C:
		s.mu.Lock()
		s.gen++
		s.mu.Unlock()
		log.Printf("temail: sync autostart timed out (the keychain may be locked); start sync from the app")
	}
}

// ServiceShutdown stops the active run. It is a lifecycle hook, not a bound
// method. Shutdown (in shutdown.go) does the ordered teardown.
func (s *SyncService) ServiceShutdown() error {
	return s.Stop()
}

// Start begins a run. If one is already active it does nothing. It returns
// bridge.ErrNotConfigured, bridge.ErrNoPassword or bridge.ErrNotPinned when the
// account is not ready.
func (s *SyncService) Start() error {
	return s.start()
}

// Stop cancels the active run and waits up to the stop wait for it. It always
// invalidates launches that have not started yet. It is a no-op if no run is
// active. On timeout it returns ErrSyncStopTimeout and Status reports
// StopTimedOut until the run ends.
func (s *SyncService) Stop() error {
	s.mu.Lock()
	s.gen++
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	cancel()
	wait := s.stopWait
	if wait <= 0 {
		wait = syncStopWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		s.mu.Lock()
		s.stopTimedOut = true
		s.lastError = "sync did not stop within the time limit; it may still be running"
		s.mu.Unlock()
		return ErrSyncStopTimeout
	}
}

// Status reports whether a run is active, whether a stop timed out, and the
// outcome of the last run.
func (s *SyncService) Status() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyncStatus{
		Running:      s.done != nil,
		StopTimedOut: s.stopTimedOut,
		LastResult:   s.lastResult,
		LastError:    s.lastError,
	}
}

// stillActive reports whether a run is still active (used by Shutdown).
func (s *SyncService) stillActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done != nil
}

// restart stops the active run (if any) and starts again. Stop bumps gen, so
// the start below reads credentials after the stop and is the only launch.
func (s *SyncService) restart() {
	if err := s.Stop(); err != nil {
		log.Printf("temail: restart sync: %v", err)
		return
	}
	if err := s.start(); err != nil {
		log.Printf("temail: sync not started: %v", err)
	}
}

func (s *SyncService) start() error {
	s.mu.Lock()
	if s.done != nil {
		s.mu.Unlock()
		return nil
	}
	gen := s.gen
	base := s.base
	s.mu.Unlock()
	if base == nil {
		base = context.Background()
	}

	// Credentials are read outside the lock: the keyring may block.
	acct, pw, err := bridge.Credentials(s.cfg, s.sec)
	if err != nil {
		return err
	}
	if acct.CertFingerprint == "" {
		return bridge.ErrNotPinned
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen {
		// A Stop (or restart) ran while we were reading credentials. Drop this
		// launch; the stopper decides what runs next.
		return nil
	}
	if s.done != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(base)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.lastError = ""
	go s.run(ctx, cancel, acct, pw, done)
	return nil
}

// run performs one sync and reports the outcome. It runs in its own goroutine.
func (s *SyncService) run(ctx context.Context, cancel context.CancelFunc, acct config.BridgeAccount, pw string, done chan struct{}) {
	defer close(done)
	defer cancel()

	client := s.newClient()
	syn, err := mailsync.New(s.st, client, func(ctx context.Context) error {
		return client.Connect(ctx, acct, pw)
	}, mailsync.Options{
		Account:    acct.ID,
		BodyDelay:  s.bodyDelay,
		Backoff:    s.backoff,
		OnProgress: s.progress,
	})
	var res mailsync.Result
	var runErr error
	if err != nil {
		_ = client.Close()
		runErr = err
	} else {
		res, runErr = syn.Run(ctx)
	}
	stopped := ctx.Err() != nil
	dto := toSyncResult(res)

	s.mu.Lock()
	s.cancel = nil
	s.done = nil
	s.stopTimedOut = false
	s.lastResult = &dto
	switch {
	case stopped:
		s.lastError = "sync stopped"
	case runErr != nil && !errors.Is(runErr, mailsync.ErrPartial):
		s.lastError = runErr.Error()
	}
	errText := s.lastError
	s.mu.Unlock()

	switch {
	case stopped:
		// No outcome event: Stop and shutdown are not failures the user must see.
	case runErr != nil && !errors.Is(runErr, mailsync.ErrPartial):
		s.emit(EventSyncError, SyncErrorEvent{Message: errText})
	default:
		s.emit(EventSyncDone, dto)
	}
}

func (s *SyncService) progress(p mailsync.Progress) {
	s.emit(EventSyncProgress, ProgressEvent{
		State:        string(p.State),
		Mailbox:      p.Mailbox,
		HeadersDone:  p.HeadersDone,
		HeadersTotal: p.HeadersTotal,
		BodiesDone:   p.BodiesDone,
		BodiesTotal:  p.BodiesTotal,
	})
}

func (s *SyncService) emit(name string, data any) {
	if s.emitter != nil {
		s.emitter.Emit(name, data)
	}
}

func toSyncResult(r mailsync.Result) SyncResult {
	out := SyncResult{
		Mailboxes:      r.Mailboxes,
		HeadersFetched: r.HeadersFetched,
		Deleted:        r.Deleted,
		BodiesFetched:  r.BodiesFetched,
		BodiesFailed:   r.BodiesFailed,
		MailboxErrors:  make([]MailboxErrorDTO, 0, len(r.MailboxErrors)),
	}
	for _, me := range r.MailboxErrors {
		out.MailboxErrors = append(out.MailboxErrors, MailboxErrorDTO{Mailbox: me.Mailbox, Message: me.Err.Error()})
	}
	return out
}
