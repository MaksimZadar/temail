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
// with the app when an account is configured, pinned and has a password, and
// again after SetupService changes the account or the pin.
//
// Events: sync:progress (ProgressEvent), sync:done (SyncResult, which may list
// MailboxErrors), sync:error (SyncErrorEvent). A run stopped by Stop or by
// shutdown emits no event; Status shows it as not running.
type SyncService struct {
	st        store.Store
	cfg       ConfigStore
	sec       PasswordSource
	newClient ClientFactory
	emitter   Emitter

	// bodyDelay and backoff override the sync defaults. Zero means default.
	bodyDelay time.Duration
	backoff   time.Duration
	// stopWait bounds Stop. Zero means syncStopWait.
	stopWait time.Duration

	mu         sync.Mutex
	base       context.Context // parent of every run; set by ServiceStartup
	cancel     context.CancelFunc
	done       chan struct{} // closed when the active run has finished emitting
	lastResult *SyncResult
	lastError  string
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
	Running    bool        `json:"running"`
	LastResult *SyncResult `json:"lastResult"`
	LastError  string      `json:"lastError"`
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

// ServiceStartup records the app context, which lives until shutdown, and
// starts a run if the account is ready. Not being ready is not an error here.
func (s *SyncService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	s.mu.Lock()
	s.base = ctx
	s.mu.Unlock()
	if err := s.start(); err != nil {
		log.Printf("temail: sync not started: %v", err)
	}
	return nil
}

// ServiceShutdown stops the active run and waits for it.
func (s *SyncService) ServiceShutdown() error {
	return s.Stop()
}

// Start begins a run. If one is already active it does nothing. It returns
// bridge.ErrNotConfigured, bridge.ErrNoPassword or bridge.ErrNotPinned when the
// account is not ready.
func (s *SyncService) Start() error {
	return s.start()
}

// Stop cancels the active run and waits for it to finish. It is a no-op if no
// run is active.
func (s *SyncService) Stop() error {
	s.mu.Lock()
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
		return ErrSyncStopTimeout
	}
}

// Status reports whether a run is active, and the outcome of the last one.
func (s *SyncService) Status() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SyncStatus{Running: s.done != nil, LastResult: s.lastResult, LastError: s.lastError}
}

// restart stops the active run, if any, and starts again with the current
// configuration. A run that cannot start is not an error for the caller.
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
	running := s.done != nil
	base := s.base
	s.mu.Unlock()
	if running {
		return nil
	}
	if base == nil {
		base = context.Background()
	}

	// Credentials are read outside the lock: the keyring may be slow.
	acct, pw, err := bridge.Credentials(s.cfg, s.sec)
	if err != nil {
		return err
	}
	if acct.CertFingerprint == "" {
		return bridge.ErrNotPinned
	}

	s.mu.Lock()
	defer s.mu.Unlock()
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
		// No event: Stop and shutdown are not failures the user must see.
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
