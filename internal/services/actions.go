package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/actions"
	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/store"
)

// EventActionsDone is emitted after an action batch, coalesced: one event per
// burst, with the applied and failed counts.
const EventActionsDone = "actions:done"

// actionTimeout bounds one bound action call, including reconnects and the
// eventual-removal recheck.
const actionTimeout = 120 * time.Second

// defaultEventDelay is the coalescing window for actions:done.
const defaultEventDelay = 100 * time.Millisecond

// ActionParamsDTO is the typed argument of one action.
type ActionParamsDTO struct {
	Folder string `json:"folder,omitempty"`
	Label  string `json:"label,omitempty"`
}

// ActionDTO is one daemon action request from the frontend: an allowlisted type,
// its parameters, and stable Proton message ids (never UIDs).
type ActionDTO struct {
	Type       string          `json:"type"`
	Params     ActionParamsDTO `json:"params"`
	MessageIDs []string        `json:"messageIds"`
}

// ActionFailureDTO is one message the action did not apply to. Message is safe
// to show: it names the mailbox and the failure, never message content.
type ActionFailureDTO struct {
	MID     string `json:"mid"`
	Mailbox string `json:"mailbox"`
	Message string `json:"message"`
}

// ActionResultDTO is the outcome of one action.
type ActionResultDTO struct {
	Applied      []string           `json:"applied"`
	Failed       []ActionFailureDTO `json:"failed"`
	AppliedCount int                `json:"appliedCount"`
	FailedCount  int                `json:"failedCount"`
	// BatchID groups the audit rows of this action.
	BatchID string `json:"batchId"`
	// AuditIDs are the audit rows that can be undone; a partial or unverified
	// message is not listed.
	AuditIDs []int64 `json:"auditIds"`
	// Code is a stable outcome code for undo calls: not_undoable, already_undone,
	// not_found, wrong_account or diverged. It is empty for a normal apply.
	Code string `json:"code"`
}

// AuditDTO is one audit row: metadata only, never message content. Recent
// returns one row per action batch, so Count is the number of messages.
type AuditDTO struct {
	ID       int64           `json:"id"`
	TS       time.Time       `json:"ts"`
	Type     string          `json:"type"`
	Params   ActionParamsDTO `json:"params"`
	Count    int             `json:"count"`
	Error    string          `json:"error"`
	Undone   bool            `json:"undone"`
	BatchID  string          `json:"batchId"`
	AuditIDs []int64         `json:"auditIds"`
	// Code explains why a row cannot be undone: not_undoable, already_undone or
	// diverged. Empty means Undo(id) will be attempted.
	Code string `json:"code"`
}

// Outcome codes returned in ActionResultDTO.Code and AuditDTO.Code.
const (
	CodeNotUndoable   = "not_undoable"
	CodeAlreadyUndone = "already_undone"
	CodeNotFound      = "not_found"
	CodeWrongAccount  = "wrong_account"
	CodeDiverged      = "diverged"
)

// ActionsDoneEvent is the payload of actions:done.
type ActionsDoneEvent struct {
	Type   string `json:"type"`
	Count  int    `json:"count"`
	Failed int    `json:"failed"`
}

// ActionEngine is the slice of actions.Executor the service uses. Tests supply
// a fake.
type ActionEngine interface {
	Apply(ctx context.Context, a actions.Action) (actions.Result, error)
	Undo(ctx context.Context, id int64) (actions.Result, error)
	UndoLast(ctx context.Context) (actions.Result, error)
	Recent(ctx context.Context, limit int) ([]actions.Entry, error)
}

// ActionService applies user actions and exposes the audit log with undo. It is
// the only bound service with mailbox write methods. The writes themselves live
// in internal/actions, behind verified recipes.
type ActionService struct {
	st      store.Store
	cfg     ConfigStore
	sec     PasswordSource
	syncSvc *SyncService // supplies the shared auth latch and credential key
	emitter Emitter

	// openWriter and openAudit are replaceable for tests.
	openWriter func() mail.Writer
	openAudit  func() (*actions.Audit, error)
	// newEngine builds the executor for one account. Tests replace it.
	newEngine func(account string, audit *actions.Audit) (ActionEngine, error)

	drainWait  time.Duration
	eventDelay time.Duration

	mu      sync.Mutex
	closed  bool
	audit   *actions.Audit
	engines map[string]ActionEngine
	wg      sync.WaitGroup

	event       *ActionsDoneEvent
	eventTimer  *time.Timer
	eventClosed bool
}

// NewActionService returns an ActionService. It never connects at startup; the
// first action opens the audit log and, lazily, the Bridge connection. syncSvc
// supplies the auth latch shared by every login path (nil means no latch).
func NewActionService(st store.Store, cfg ConfigStore, sec PasswordSource, newWriter func() mail.Writer, syncSvc *SyncService, emitter Emitter) *ActionService {
	s := &ActionService{
		st: st, cfg: cfg, sec: sec, syncSvc: syncSvc, emitter: emitter,
		openWriter: newWriter,
		openAudit:  func() (*actions.Audit, error) { return actions.OpenAudit("") },
		drainWait:  shutdownWait,
		eventDelay: defaultEventDelay,
		engines:    map[string]ActionEngine{},
	}
	s.newEngine = s.defaultEngine
	return s
}

// defaultEngine builds the real executor. The resolve closure reads the
// password only when a connection is needed, and shares the sync's latch key.
func (s *ActionService) defaultEngine(account string, audit *actions.Audit) (ActionEngine, error) {
	var latch actions.AuthLatch
	if s.syncSvc != nil {
		latch = s.syncSvc.auth
	}
	return actions.New(s.st, s.openWriter(), audit, actions.Options{
		Account: account,
		Resolve: func(context.Context) (config.BridgeAccount, string, error) {
			c, err := s.cfg.Load()
			if err != nil {
				return config.BridgeAccount{}, "", fmt.Errorf("load config: %w", err)
			}
			idx, err := bridge.PickAccount(c, account)
			if err != nil {
				return config.BridgeAccount{}, "", err
			}
			acct := c.Bridge[idx]
			if acct.CertFingerprint == "" {
				return config.BridgeAccount{}, "", bridge.ErrNotPinned
			}
			pw, err := bridge.Password(s.sec, acct.ID)
			if err != nil {
				return config.BridgeAccount{}, "", err
			}
			return acct, pw, nil
		},
		Auth: latch,
		Resync: func(ctx context.Context, mailboxes []string) {
			if err := s.st.MarkMailboxesStale(ctx, account, mailboxes); err != nil {
				log.Printf("actions: mark mailboxes stale: %v", err)
			}
			if s.syncSvc != nil {
				s.syncSvc.kickWatcher()
			}
		},
	})
}

// ServiceShutdown is the Wails lifecycle hook. The ordered teardown in Shutdown
// already stopped actions; this only repeats it.
func (s *ActionService) ServiceShutdown() error {
	return s.shutdown()
}

// Apply runs one action batch. Invalid input returns ErrInvalidInput. A refused
// login trips the shared latch and stops the batch. Per-message failures are in
// the result, not in the error.
func (s *ActionService) Apply(ctx context.Context, in ActionDTO) (ActionResultDTO, error) {
	cctx, end, err := s.begin(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	defer end()
	a := actions.Action{
		Type:       actions.Type(in.Type),
		Params:     actions.Params{Folder: in.Params.Folder, Label: in.Params.Label},
		MessageIDs: in.MessageIDs,
	}
	if err := actions.Validate(a); err != nil {
		return ActionResultDTO{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	eng, err := s.engineFor(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	res, err := eng.Apply(cctx, a)
	if len(res.Applied) > 0 || len(res.Failed) > 0 {
		s.noteDone(in.Type, res)
	}
	return toActionResult(res), err
}

// Undo applies the inverse of one audit entry.
func (s *ActionService) Undo(ctx context.Context, id int64) (ActionResultDTO, error) {
	if id <= 0 {
		return ActionResultDTO{}, fmt.Errorf("%w: audit id must be positive", ErrInvalidInput)
	}
	cctx, end, err := s.begin(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	defer end()
	eng, err := s.engineFor(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	res, err := eng.Undo(cctx, id)
	if len(res.Applied) > 0 || len(res.Failed) > 0 {
		s.noteDone("undo", res)
	}
	out := toActionResult(res)
	if code := actionCode(err); code != "" {
		out.Code = code
		return out, nil
	}
	return out, err
}

// UndoLast undoes the newest verified action that is not undone yet.
func (s *ActionService) UndoLast(ctx context.Context) (ActionResultDTO, error) {
	cctx, end, err := s.begin(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	defer end()
	eng, err := s.engineFor(ctx)
	if err != nil {
		return ActionResultDTO{}, err
	}
	res, err := eng.UndoLast(cctx)
	if len(res.Applied) > 0 || len(res.Failed) > 0 {
		s.noteDone("undo", res)
	}
	out := toActionResult(res)
	if code := actionCode(err); code != "" {
		out.Code = code
		return out, nil
	}
	return out, err
}

// Recent returns the newest audit rows, newest first. limit defaults to 50 and
// is capped at 500.
func (s *ActionService) Recent(ctx context.Context, limit int) ([]AuditDTO, error) {
	if _, end, err := s.begin(ctx); err != nil {
		return nil, err
	} else {
		defer end()
	}
	eng, err := s.engineFor(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := eng.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AuditDTO, 0, len(entries))
	for _, e := range entries {
		var p ActionParamsDTO
		if e.ParamsJSON != "" {
			_ = json.Unmarshal([]byte(e.ParamsJSON), &p)
		}
		dto := AuditDTO{
			ID: e.ID, TS: e.TS, Type: e.Type, Params: p,
			Count: e.Count, Error: e.Error, Undone: e.Undone,
			BatchID: e.BatchID, AuditIDs: e.AuditIDs,
		}
		if !e.Undoable {
			if e.Undone {
				dto.Code = CodeAlreadyUndone
			} else {
				dto.Code = CodeNotUndoable
			}
		}
		out = append(out, dto)
	}
	return out, nil
}

// actionCode maps an undo refusal to a stable DTO code. A mapped error is not
// returned to the frontend as an error: the code says what happened.
func actionCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, actions.ErrDiverged):
		return CodeDiverged
	case errors.Is(err, actions.ErrAlreadyUndone):
		return CodeAlreadyUndone
	case errors.Is(err, actions.ErrAuditNotFound):
		return CodeNotFound
	case errors.Is(err, actions.ErrWrongAccount):
		return CodeWrongAccount
	case errors.Is(err, actions.ErrNotUndoable):
		return CodeNotUndoable
	}
	return ""
}

// begin registers one bound call, bounded by actionTimeout and by shutdown.
func (s *ActionService) begin(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrClosed
	}
	s.wg.Add(1)
	cctx, cancel := context.WithTimeout(ctx, actionTimeout)
	return cctx, func() {
		cancel()
		s.wg.Done()
	}, nil
}

// engineFor returns the executor for the currently configured account. A changed
// account gets its own executor; old ones stay open until shutdown, so an
// in-flight call is never closed under it.
func (s *ActionService) engineFor(ctx context.Context) (ActionEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	acct, err := s.accountID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if eng, ok := s.engines[acct]; ok {
		s.mu.Unlock()
		return eng, nil
	}
	audit := s.audit
	s.mu.Unlock()

	if audit == nil {
		a, err := s.openAudit()
		if err != nil {
			return nil, fmt.Errorf("open audit log: %w", err)
		}
		s.mu.Lock()
		if s.audit == nil {
			s.audit = a
		} else {
			_ = a.Close()
		}
		audit = s.audit
		s.mu.Unlock()
	}

	eng, err := s.newEngine(acct, audit)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if c, ok := eng.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		return nil, ErrClosed
	}
	if existing, ok := s.engines[acct]; ok {
		// Another call built it first; keep that one and close the spare.
		if c, ok := eng.(interface{ Close() error }); ok {
			_ = c.Close()
		}
		return existing, nil
	}
	s.engines[acct] = eng
	return eng, nil
}

// accountID returns the ID of the first account in config.json.
func (s *ActionService) accountID() (string, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return "", err
	}
	return cfg.Bridge[idx].ID, nil
}

// noteDone coalesces bursts of action results into one actions:done event.
func (s *ActionService) noteDone(typ string, res actions.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventClosed {
		return
	}
	if s.event == nil {
		s.event = &ActionsDoneEvent{}
	}
	s.event.Type = typ
	s.event.Count += len(res.Applied)
	s.event.Failed += len(res.Failed)
	if s.eventTimer == nil {
		s.eventTimer = time.AfterFunc(s.eventDelay, s.flushEvent)
	}
}

func (s *ActionService) flushEvent() {
	s.mu.Lock()
	ev := s.event
	s.event = nil
	s.eventTimer = nil
	emitter := s.emitter
	s.mu.Unlock()
	if ev != nil && emitter != nil {
		emitter.Emit(EventActionsDone, *ev)
	}
}

// shutdown refuses new calls, waits for in-flight calls, then closes the write
// connections and the audit log. It is idempotent.
func (s *ActionService) shutdown() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.eventClosed = true
	if s.eventTimer != nil {
		s.eventTimer.Stop()
		s.eventTimer = nil
	}
	s.event = nil
	engines := make([]ActionEngine, 0, len(s.engines))
	for _, e := range s.engines {
		engines = append(engines, e)
	}
	s.engines = map[string]ActionEngine{}
	audit := s.audit
	s.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()
	wait := s.drainWait
	if wait <= 0 {
		wait = shutdownWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var errs []error
	select {
	case <-drained:
	case <-timer.C:
		errs = append(errs, errors.New("actions did not finish in time"))
	}
	for _, eng := range engines {
		if c, ok := eng.(interface{ Close() error }); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if audit != nil {
		if err := audit.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func toActionResult(r actions.Result) ActionResultDTO {
	out := ActionResultDTO{
		Applied:      r.Applied,
		Failed:       make([]ActionFailureDTO, 0, len(r.Failed)),
		AppliedCount: len(r.Applied),
		FailedCount:  len(r.Failed),
		BatchID:      r.BatchID,
		AuditIDs:     []int64{},
	}
	if out.Applied == nil {
		out.Applied = []string{}
	}
	for _, f := range r.Failed {
		out.Failed = append(out.Failed, ActionFailureDTO{MID: f.MID, Mailbox: f.Mailbox, Message: f.Message})
	}
	for _, e := range r.Entries {
		if e.Undoable {
			out.AuditIDs = append(out.AuditIDs, e.AuditID)
		}
	}
	return out
}
