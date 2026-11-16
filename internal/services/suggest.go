package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MaksimZadar/temail/internal/actions"
	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/llm"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
	"github.com/MaksimZadar/temail/internal/suggest"
)

// Suggestion events. One result per email, then one done for the job.
const (
	EventSuggestResult = "suggest:result"
	EventSuggestDone   = "suggest:done"
)

// Suggestion job limits. The batch cap lives in internal/suggest.
const (
	suggestConcurrency = 2
	// suggestCloudTimeout bounds one email on a cloud provider. Cloud endpoints
	// can be slower than the old 60 s, so the per-email bound is 120 s. A local
	// provider has no request timeout (see Suggest): it may run until the user
	// cancels the job.
	suggestCloudTimeout = 120 * time.Second
	suggestJobDrainWait = shutdownWait
)

// Suggestion targets. The Go side classifies each suggested name against the
// cached mailbox list, so the UI can say "move to folder", "add label" or
// "new label" without guessing.
const (
	SuggestTargetLabel  = "label"
	SuggestTargetFolder = "folder"
	SuggestTargetNew    = "new"
)

// Job states returned by Status.
const (
	SuggestStateRunning   = "running"
	SuggestStateDone      = "done"
	SuggestStateCancelled = "cancelled"
)

// Failure codes for one email. They are stable and safe to show.
const (
	CodeNotCached   = "not_cached"
	CodeCancelled   = "cancelled"
	CodeTooLarge    = "too_large"
	CodeInvalidJSON = "invalid_output"
	CodeCache       = "cache_error"
	CodeCreateLabel = "create_failed"
	CodeApplyLabel  = "apply_failed"
	CodeMoveFolder  = "move_failed"
	CodeRejected    = "rejected"
)

// Errors the suggestion methods return.
var (
	// ErrNoProvider means no LLM provider is configured (and no default names
	// one).
	ErrNoProvider = errors.New("no LLM provider is configured; add one in Settings")
	// ErrSuggestCloudDisabled refuses a disabled cloud provider before any
	// message content is sent. The prefix is matched by the UI.
	ErrSuggestCloudDisabled = errors.New("cloud_disabled: this cloud provider is turned off; enable it before sending message content")
	// ErrJobNotFound means no suggestion job has that id (or a new job replaced
	// it).
	ErrJobNotFound = errors.New("no such suggestion job")
)

// SuggestLabelDTO is one proposed label. Target is computed in Go against the
// cached mailbox list: "label" (Labels/<name> exists), "folder" (Folders/<name>
// exists, so applying it is a move) or "new" (create Labels/<name> first). The
// addition is additive; older UI code can ignore it.
type SuggestLabelDTO struct {
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	Existing   bool    `json:"existing"`
	Target     string  `json:"target"`
}

// SuggestResultDTO is the per-email suggestion result. Labels and Summary are
// plain text; Error holds a fixed, content-free message.
type SuggestResultDTO struct {
	JobID   string            `json:"jobId"`
	MID     string            `json:"mid"`
	OK      bool              `json:"ok"`
	Labels  []SuggestLabelDTO `json:"labels"`
	Summary string            `json:"summary"`
	Error   string            `json:"error"`
	Code    string            `json:"code"`
}

// SuggestDoneDTO is emitted once per job.
type SuggestDoneDTO struct {
	JobID  string `json:"jobId"`
	Total  int    `json:"total"`
	Failed int    `json:"failed"`
}

// SuggestJobDTO is the full state of one job, for a late-joining UI.
type SuggestJobDTO struct {
	JobID     string             `json:"jobId"`
	Provider  string             `json:"provider"`
	State     string             `json:"state"`
	Total     int                `json:"total"`
	Completed int                `json:"completed"`
	Failed    int                `json:"failed"`
	Started   time.Time          `json:"started"`
	Results   []SuggestResultDTO `json:"results"`
}

// SuggestApplyDTO is the outcome of applying accepted suggestions. It mirrors
// ActionResultDTO (applied ids, per-message failures with a code, batch and
// audit ids) and adds the labels created for this call.
type SuggestApplyDTO struct {
	Applied       []string           `json:"applied"`
	Failed        []ActionFailureDTO `json:"failed"`
	AppliedCount  int                `json:"appliedCount"`
	FailedCount   int                `json:"failedCount"`
	BatchIDs      []string           `json:"batchIds"`
	AuditIDs      []int64            `json:"auditIds"`
	CreatedLabels []string           `json:"createdLabels"`
}

type suggestJob struct {
	id       string
	provider string
	mids     []string
	started  time.Time
	cancel   context.CancelFunc

	mu      sync.Mutex
	state   string
	results map[string]SuggestResultDTO
	failed  int
}

func (j *suggestJob) snapshot() SuggestJobDTO {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := SuggestJobDTO{
		JobID: j.id, Provider: j.provider, State: j.state,
		Total: len(j.mids), Failed: j.failed, Started: j.started,
		Results: make([]SuggestResultDTO, 0, len(j.mids)),
	}
	for _, mid := range j.mids {
		if r, ok := j.results[mid]; ok {
			out.Results = append(out.Results, r)
		}
	}
	out.Completed = len(out.Results)
	return out
}

// SuggestionService suggests labels for up to 25 cached messages and applies
// the accepted ones through ActionService. A job runs in the background; the
// bound call returns its id immediately. Results are kept in memory only: they
// are discarded on cancel, on shutdown, and when a new job replaces the old one.
type SuggestionService struct {
	st      store.Store
	cfg     ConfigStore
	llm     *LLMService
	actions *ActionService
	emitter Emitter

	// one is replaceable for tests. concurrency, emailTimeout and newJobID are
	// pinned by the tests too.
	one         func(ctx context.Context, c *llm.Client, msg suggest.Message, existing, folders []string) (suggest.Suggestion, error)
	concurrency int
	// emailTimeout bounds one email on a cloud provider. It is not applied to
	// a local provider, which has no request timeout.
	emailTimeout time.Duration
	newJobID     func() string
	seq          uint64

	mu         sync.Mutex
	closed     bool
	base       context.Context
	cancelBase context.CancelFunc
	jobs       map[string]*suggestJob
	current    string
	wg         sync.WaitGroup
}

// NewSuggestionService returns a SuggestionService. It never connects at
// startup and never calls a provider until Suggest is called.
func NewSuggestionService(st store.Store, cfg ConfigStore, llmSvc *LLMService, actionSvc *ActionService, emitter Emitter) *SuggestionService {
	base, cancel := context.WithCancel(context.Background())
	s := &SuggestionService{
		st: st, cfg: cfg, llm: llmSvc, actions: actionSvc, emitter: emitter,
		one:          suggest.One,
		concurrency:  suggestConcurrency,
		emailTimeout: suggestCloudTimeout,
		base:         base,
		cancelBase:   cancel,
		jobs:         map[string]*suggestJob{},
	}
	s.newJobID = func() string {
		return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&s.seq, 1))
	}
	return s
}

// ServiceShutdown is the Wails lifecycle hook. The ordered teardown also
// cancels jobs, so this only repeats it.
func (s *SuggestionService) ServiceShutdown() error { return s.shutdown() }

// Suggest starts a bounded job and returns its id. messageIds are Proton
// internal ids; blanks are dropped and duplicates removed. More than 25 ids is
// refused. An empty providerName uses the configured default; when no default
// is set, an enabled local provider is preferred, then the first enabled one.
// A disabled cloud provider is refused before anything is sent. If an identical
// batch is already running, its job id is returned instead of a second job.
func (s *SuggestionService) Suggest(ctx context.Context, messageIDs []string, providerName string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ids, err := cleanMessageIDs(messageIDs)
	if err != nil {
		return "", err
	}
	cfg, err := s.loadConfig()
	if err != nil {
		return "", err
	}
	acct, err := accountOf(cfg)
	if err != nil {
		return "", err
	}
	name, prov, err := pickSuggestionProvider(cfg, strings.TrimSpace(providerName))
	if err != nil {
		return "", err
	}
	if prov.EffectiveKind() != config.KindLocal && !prov.IsEnabled() {
		return "", ErrSuggestCloudDisabled
	}
	// A local model can be extra slow, so it gets no request timeout: only the
	// Cancel button (job context) ends a call. A cloud provider keeps a bounded
	// per-email timeout. Connect and TLS handshake stay bounded for both.
	emailTimeout := s.emailTimeout
	if prov.EffectiveKind() == config.KindLocal {
		emailTimeout = 0
	}
	client, err := s.llm.clientForThinking(name, emailTimeout, suggestionThinking(prov))
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrClosed
	}
	for _, j := range s.jobs {
		j.mu.Lock()
		running := j.state == SuggestStateRunning && j.provider == name && sameIDs(j.mids, ids)
		j.mu.Unlock()
		if running {
			return j.id, nil
		}
	}
	jobCtx, cancel := context.WithCancel(s.base)
	j := &suggestJob{
		id: s.newJobID(), provider: name, mids: ids, started: time.Now(),
		cancel: cancel, state: SuggestStateRunning, results: map[string]SuggestResultDTO{},
	}
	// A new job replaces the previous one and its results.
	s.jobs = map[string]*suggestJob{j.id: j}
	s.current = j.id
	s.wg.Add(1)
	go s.run(jobCtx, j, client, acct, emailTimeout)
	return j.id, nil
}

// Status returns one job's state and results. An empty id returns the current
// job. A job replaced by a newer one is gone.
func (s *SuggestionService) Status(jobID string) (SuggestJobDTO, error) {
	j := s.job(jobID)
	if j == nil {
		return SuggestJobDTO{}, ErrJobNotFound
	}
	return j.snapshot(), nil
}

// Cancel stops a running job and discards its results, so nothing from it can
// be applied. Cancelling a finished job is a no-op.
func (s *SuggestionService) Cancel(jobID string) error {
	j := s.job(jobID)
	if j == nil {
		return ErrJobNotFound
	}
	j.mu.Lock()
	if j.state != SuggestStateRunning {
		j.mu.Unlock()
		return nil
	}
	j.state = SuggestStateCancelled
	j.results = map[string]SuggestResultDTO{}
	j.failed = len(j.mids)
	cancel := j.cancel
	j.mu.Unlock()
	cancel()
	return nil
}

// ApplySuggestions applies the accepted labels of one job. accepted maps a
// message id to label names. Only labels that job proposed for that message are
// accepted; anything else is refused. Each name is resolved against the cached
// mailbox list: Labels/<name> -> add_label; else Folders/<name> -> move to that
// folder (Proton folders and labels are distinct); else create Labels/<name>
// (verified with LIST and audited as a non-undoable create_label row), then add
// the label. Per-message failures carry a code, and the call itself fails only
// for invalid input, a missing job, or a refusal before any write.
func (s *SuggestionService) ApplySuggestions(ctx context.Context, jobID string, accepted map[string][]string) (SuggestApplyDTO, error) {
	if err := ctx.Err(); err != nil {
		return SuggestApplyDTO{}, err
	}
	j := s.job(jobID)
	if j == nil {
		return SuggestApplyDTO{}, ErrJobNotFound
	}
	j.mu.Lock()
	state := j.state
	results := make(map[string]SuggestResultDTO, len(j.results))
	for k, v := range j.results {
		results[k] = v
	}
	j.mu.Unlock()
	if state == SuggestStateCancelled {
		return SuggestApplyDTO{}, fmt.Errorf("%w: the job was cancelled; nothing can be applied", ErrInvalidInput)
	}

	// Build the allowlist from this job's own results.
	wanted := map[string]map[string]SuggestLabelDTO{} // mid -> lower(name) -> label
	order := make([]string, 0, len(accepted))
	for mid, names := range accepted {
		r, ok := results[mid]
		if !ok || !r.OK {
			return SuggestApplyDTO{}, fmt.Errorf("%w: %s is not a message with a suggestion in this job", ErrInvalidInput, mid)
		}
		allowed := make(map[string]SuggestLabelDTO, len(r.Labels))
		for _, l := range r.Labels {
			allowed[strings.ToLower(l.Name)] = l
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if _, ok := allowed[strings.ToLower(name)]; !ok {
				return SuggestApplyDTO{}, fmt.Errorf("%w: %q was not suggested for this message", ErrInvalidInput, name)
			}
		}
		wanted[mid] = allowed
		order = append(order, mid)
	}
	sort.Strings(order)

	// label -> mids that accept it, in job order.
	byLabel := map[string][]string{}
	labelName := map[string]string{}
	for _, mid := range j.mids {
		allowed, ok := wanted[mid]
		if !ok {
			continue
		}
		for _, raw := range accepted[mid] {
			key := strings.ToLower(strings.TrimSpace(raw))
			l, ok := allowed[key]
			if !ok {
				continue
			}
			if _, seen := labelName[key]; !seen {
				labelName[key] = l.Name
				byLabel[key] = nil
			}
			byLabel[key] = append(byLabel[key], mid)
		}
	}
	if len(byLabel) == 0 {
		return SuggestApplyDTO{}, fmt.Errorf("%w: no labels were accepted", ErrInvalidInput)
	}

	cfg, err := s.loadConfig()
	if err != nil {
		return SuggestApplyDTO{}, err
	}
	acct, err := accountOf(cfg)
	if err != nil {
		return SuggestApplyDTO{}, err
	}
	mailboxes, err := s.labelContext(ctx, acct)
	if err != nil {
		return SuggestApplyDTO{}, err
	}

	keys := make([]string, 0, len(byLabel))
	for key := range byLabel {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := SuggestApplyDTO{
		Applied: []string{}, Failed: []ActionFailureDTO{},
		BatchIDs: []string{}, AuditIDs: []int64{}, CreatedLabels: []string{},
	}
	failed := map[string]ActionFailureDTO{}
	applyOK := map[string]bool{}

	// Proton folders and labels are distinct. A suggested name that exists as
	// Folders/<name> but not as Labels/<name> is an existing folder target:
	// apply it as a move. Only a truly unknown name creates Labels/<name>.
	// Create the unknown labels first. A creation failure fails every message
	// that accepted that label, but other labels and messages still run.
	created := map[string]bool{}
	for _, key := range keys {
		if mailboxes.target(key) != SuggestTargetNew {
			continue
		}
		name := labelName[key]
		if err := s.actions.createLabel(ctx, name); err != nil {
			msg := "the new label could not be created"
			// Bridge's own NO text is the useful part for a failed CREATE.
			// ServerRefusalMessage keeps only that line: no local path, request
			// body or credential temail built.
			if detail := actions.ServerRefusalMessage(err); detail != "" {
				msg += ": " + detail
			}
			for _, mid := range byLabel[key] {
				failed[mid] = ActionFailureDTO{MID: mid, Message: msg, Code: CodeCreateLabel}
			}
			continue
		}
		out.CreatedLabels = append(out.CreatedLabels, name)
		created[key] = true
	}

	for _, key := range keys {
		name := labelName[key]
		mids := byLabel[key]
		var (
			in     ActionDTO
			code   string
			errMsg string
		)
		if mailboxes.target(key) == SuggestTargetFolder {
			in = ActionDTO{
				Type:       string(actions.Move),
				Params:     ActionParamsDTO{Folder: mailboxes.foldersByKey[key]},
				MessageIDs: mids,
			}
			code, errMsg = CodeMoveFolder, "the message could not be moved to the folder"
		} else {
			if mailboxes.target(key) == SuggestTargetNew && !created[key] {
				continue // creation failed; failures are recorded above
			}
			in = ActionDTO{
				Type:       string(actions.AddLabel),
				Params:     ActionParamsDTO{Label: name},
				MessageIDs: mids,
			}
			code, errMsg = CodeApplyLabel, "the label could not be applied"
		}
		res, err := s.actions.Apply(ctx, in)
		if err != nil {
			for _, mid := range mids {
				failed[mid] = ActionFailureDTO{MID: mid, Message: errMsg, Code: code}
			}
			continue
		}
		if res.BatchID != "" {
			out.BatchIDs = append(out.BatchIDs, res.BatchID)
		}
		out.AuditIDs = append(out.AuditIDs, res.AuditIDs...)
		for _, mid := range res.Applied {
			applyOK[mid] = true
		}
		for _, f := range res.Failed {
			failed[f.MID] = ActionFailureDTO{MID: f.MID, Mailbox: f.Mailbox, Message: f.Message, Code: code}
			delete(applyOK, f.MID)
		}
	}

	for _, mid := range order {
		if f, ok := failed[mid]; ok {
			out.Failed = append(out.Failed, f)
			continue
		}
		if applyOK[mid] {
			out.Applied = append(out.Applied, mid)
		}
	}
	out.AppliedCount = len(out.Applied)
	out.FailedCount = len(out.Failed)
	return out, nil
}

// run loads the cache context once, then suggests for each message with at most
// two calls in flight. A cancelled job stops without recording results.
func (s *SuggestionService) run(ctx context.Context, j *suggestJob, client *llm.Client, account string, emailTimeout time.Duration) {
	defer s.wg.Done()
	defer j.cancel()

	mailboxes, lerr := s.labelContext(ctx, account)
	if lerr != nil {
		s.failJob(j, CodeCache, "the mailbox list could not be read")
		s.finish(j)
		return
	}
	labels, folders := mailboxes.labels, mailboxes.folders
	members, merr := s.st.MembershipOfMessages(ctx, account, j.mids)
	if merr != nil {
		s.failJob(j, CodeCache, "the message list could not be read")
		s.finish(j)
		return
	}

	type prepared struct {
		mid string
		msg suggest.Message
	}
	ready := make([]prepared, 0, len(j.mids))
	for _, mid := range j.mids {
		if ctx.Err() != nil {
			break
		}
		msg, code, err := s.buildMessage(ctx, account, mid, members[mid])
		if err != nil {
			res := SuggestResultDTO{JobID: j.id, MID: mid, Labels: []SuggestLabelDTO{}, Code: code, Error: err.Error()}
			s.record(j, res)
			continue
		}
		ready = append(ready, prepared{mid: mid, msg: msg})
	}

	sem := make(chan struct{}, maxInt(s.concurrency, 1))
	var wg sync.WaitGroup
	for _, p := range ready {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(p prepared) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// A local provider has no deadline: only Cancel (or shutdown) ends
			// the call. A cloud provider is bounded per email.
			ectx := ctx
			if emailTimeout > 0 {
				var cancel context.CancelFunc
				ectx, cancel = context.WithTimeout(ctx, emailTimeout)
				defer cancel()
			}
			raw, err := s.one(ectx, client, p.msg, labels, folders)
			res := SuggestResultDTO{JobID: j.id, MID: p.mid, Labels: []SuggestLabelDTO{}}
			if err != nil {
				res.Code = suggestionCode(err)
				res.Error = suggestionMessage(err)
				s.record(j, res)
				return
			}
			norm := suggest.NormalizeWithFolders(raw, labels, folders)
			res.OK = true
			res.Summary = norm.Summary
			for _, l := range norm.Labels {
				res.Labels = append(res.Labels, SuggestLabelDTO{
					Name: l.Name, Confidence: l.Confidence, Reason: l.Reason, Existing: l.Existing,
					Target: mailboxes.target(l.Name),
				})
			}
			s.record(j, res)
		}(p)
	}
	wg.Wait()
	s.finish(j)
}

// failJob records one failure for every message that has no result yet.
func (s *SuggestionService) failJob(j *suggestJob, code, msg string) {
	for _, mid := range j.mids {
		j.mu.Lock()
		_, done := j.results[mid]
		running := j.state == SuggestStateRunning
		j.mu.Unlock()
		if done || !running {
			continue
		}
		s.record(j, SuggestResultDTO{JobID: j.id, MID: mid, Labels: []SuggestLabelDTO{}, Code: code, Error: msg})
	}
}

// record stores one result and emits it, unless the job was cancelled.
func (s *SuggestionService) record(j *suggestJob, res SuggestResultDTO) {
	j.mu.Lock()
	if j.state != SuggestStateRunning {
		j.mu.Unlock()
		return
	}
	if _, ok := j.results[res.MID]; ok {
		j.mu.Unlock()
		return
	}
	j.results[res.MID] = res
	if !res.OK {
		j.failed++
	}
	j.mu.Unlock()
	if s.emitter != nil {
		s.emitter.Emit(EventSuggestResult, res)
	}
}

// finish marks a running job done and emits one done event. A cancelled job
// keeps its cancelled state and reports every message as failed.
func (s *SuggestionService) finish(j *suggestJob) {
	j.mu.Lock()
	if j.state == SuggestStateRunning {
		j.state = SuggestStateDone
	}
	state, total, failed := j.state, len(j.mids), j.failed
	if state == SuggestStateCancelled {
		failed = total
	}
	j.mu.Unlock()
	if s.emitter != nil {
		s.emitter.Emit(EventSuggestDone, SuggestDoneDTO{JobID: j.id, Total: total, Failed: failed})
	}
}

// buildMessage reads one message from the cache. A missing message or body-less
// message degrades safely: only a missing placement or header is a failure.
func (s *SuggestionService) buildMessage(ctx context.Context, account, mid string, placements []store.Placement) (suggest.Message, string, error) {
	key, ok := bodyKey(placements)
	if !ok {
		return suggest.Message{}, CodeNotCached, errors.New("the message is not in the local cache")
	}
	key.Account = account
	hdr, err := s.st.GetMessage(ctx, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return suggest.Message{}, CodeNotCached, errors.New("the message is not in the local cache")
		}
		return suggest.Message{}, CodeCache, errors.New("the message could not be read from the cache")
	}
	msg := suggest.Message{MID: mid, Subject: hdr.Subject, From: hdr.From}
	msg.Folder = folderOf(placements)
	msg.Labels = labelsOf(placements)
	body, err := s.st.GetBody(ctx, key)
	if err == nil {
		msg.Body = body.Text
		if strings.TrimSpace(msg.Body) == "" {
			msg.Body = suggest.HTMLToText(body.HTML)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return suggest.Message{}, CodeCache, errors.New("the message body could not be read from the cache")
	}
	msg.Body = suggest.TruncateBody(msg.Body)
	return msg, "", nil
}

// bodyKey picks the placement to read the body through: All Mail when present,
// then a folder, then any placement.
func bodyKey(placements []store.Placement) (store.MessageKey, bool) {
	if len(placements) == 0 {
		return store.MessageKey{}, false
	}
	best := placements[0]
	rank := func(p store.Placement) int {
		switch {
		case p.Mailbox == "All Mail":
			return 0
		case p.Kind != model.KindLabel:
			return 1
		default:
			return 2
		}
	}
	for _, p := range placements[1:] {
		if rank(p) < rank(best) {
			best = p
		}
	}
	return store.MessageKey{Mailbox: best.Mailbox, UIDValidity: best.UIDValidity, UID: best.UID}, best.UID != 0
}

// folderOf returns the display name of the message's folder (not a label).
func folderOf(placements []store.Placement) string {
	for _, p := range placements {
		if p.Kind == model.KindLabel || p.Mailbox == "All Mail" {
			continue
		}
		return displayName(p)
	}
	for _, p := range placements {
		if p.Kind != model.KindLabel {
			return displayName(p)
		}
	}
	return ""
}

func labelsOf(placements []store.Placement) []string {
	out := make([]string, 0, len(placements))
	for _, p := range placements {
		if p.Kind == model.KindLabel {
			out = append(out, displayName(p))
		}
	}
	sort.Strings(out)
	return out
}

func displayName(p store.Placement) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	for _, prefix := range []string{"Folders/", "Labels/"} {
		if strings.HasPrefix(p.Mailbox, prefix) {
			return strings.TrimPrefix(p.Mailbox, prefix)
		}
	}
	return p.Mailbox
}

// mailboxContext is the cached mailbox list, indexed for suggestion targets.
// Labels and folders are separate namespaces in Proton, so a name can be a
// label, a folder, both (the label wins) or neither.
type mailboxContext struct {
	labels  []string
	folders []string
	// labelsByKey maps a lower-cased label display name to its canonical name.
	labelsByKey map[string]string
	// foldersByKey maps a lower-cased folder display name to the full cached
	// mailbox name (for example Folders/Finance).
	foldersByKey map[string]string
}

// target classifies a suggested name as an existing label, an existing folder
// or a new label.
func (c mailboxContext) target(name string) string {
	key := strings.ToLower(name)
	if _, ok := c.labelsByKey[key]; ok {
		return SuggestTargetLabel
	}
	if _, ok := c.foldersByKey[key]; ok {
		return SuggestTargetFolder
	}
	return SuggestTargetNew
}

// labelContext returns the cached labels and folders, sorted and deduplicated,
// with the name indexes used by target and ApplySuggestions.
func (s *SuggestionService) labelContext(ctx context.Context, account string) (mailboxContext, error) {
	states, err := s.st.GetSyncState(ctx, account)
	if err != nil {
		return mailboxContext{}, fmt.Errorf("read mailboxes: %w", err)
	}
	out := mailboxContext{
		labelsByKey:  map[string]string{},
		foldersByKey: map[string]string{},
	}
	for _, st := range states {
		name := st.DisplayName
		if name == "" {
			name = displayName(store.Placement{Mailbox: st.Name})
		}
		key := strings.ToLower(name)
		if st.Kind == model.KindLabel {
			if _, ok := out.labelsByKey[key]; !ok {
				out.labelsByKey[key] = name
				out.labels = append(out.labels, name)
			}
			continue
		}
		if _, ok := out.foldersByKey[key]; !ok {
			out.foldersByKey[key] = st.Name
			out.folders = append(out.folders, name)
		}
	}
	sort.Strings(out.labels)
	sort.Strings(out.folders)
	return out, nil
}

func (s *SuggestionService) job(jobID string) *suggestJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if strings.TrimSpace(jobID) == "" {
		jobID = s.current
	}
	return s.jobs[jobID]
}

// shutdown cancels every job, refuses new calls and waits for the workers. It
// is idempotent. The service owns no files, so nothing is flushed.
func (s *SuggestionService) shutdown() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancelBase()
	for _, j := range s.jobs {
		j.cancel()
	}
	s.jobs = map[string]*suggestJob{}
	s.current = ""
	s.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(drained)
	}()
	wait := suggestJobDrainWait
	if wait <= 0 {
		wait = shutdownWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-drained:
		return nil
	case <-timer.C:
		return errors.New("suggestion jobs did not finish in time")
	}
}

func (s *SuggestionService) loadConfig() (config.Config, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

// cleanMessageIDs trims blanks, removes duplicates and enforces the batch cap.
func cleanMessageIDs(ids []string) ([]string, error) {
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
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: at least one message id is required", ErrInvalidInput)
	}
	if len(out) > suggest.MaxBatch {
		return nil, fmt.Errorf("%w: at most %d messages per suggestion batch", ErrInvalidInput, suggest.MaxBatch)
	}
	return out, nil
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// suggestionThinking picks the reasoning effort for a suggestion job.
//
// Label classification does not need reasoning: on qwen3:8b with Ollama 0.40.16
// the default thinking mode took 11.3 s and 306 completion tokens per call,
// while "none" took 0.27 s and 7 tokens. So a local provider defaults to
// "none" unless its config sets a value. A cloud provider keeps its configured
// value, or the provider default when none is set: a cloud model may not
// accept "none", and its config is the user's explicit choice.
func suggestionThinking(p config.LLMProvider) string {
	if p.Thinking != "" {
		return p.Thinking
	}
	if p.EffectiveKind() == config.KindLocal {
		return config.ThinkingNone
	}
	return ""
}

// pickSuggestionProvider resolves the named provider, or the default one. With
// no default, an enabled local provider is preferred, then the first enabled
// one.
func pickSuggestionProvider(cfg config.Config, name string) (string, config.LLMProvider, error) {
	if name != "" {
		if !providerNameRE.MatchString(name) {
			return "", config.LLMProvider{}, fmt.Errorf("%w: provider name", ErrInvalidInput)
		}
		idx := findProvider(cfg, name)
		if idx < 0 {
			return "", config.LLMProvider{}, ErrProviderNotFound
		}
		return name, cfg.LLM.Providers[idx], nil
	}
	if cfg.LLM.DefaultProvider != "" {
		if idx := findProvider(cfg, cfg.LLM.DefaultProvider); idx >= 0 {
			return cfg.LLM.DefaultProvider, cfg.LLM.Providers[idx], nil
		}
	}
	for _, p := range cfg.LLM.Providers {
		if p.EffectiveKind() == config.KindLocal && p.IsEnabled() {
			return p.Name, p, nil
		}
	}
	for _, p := range cfg.LLM.Providers {
		if p.IsEnabled() {
			return p.Name, p, nil
		}
	}
	return "", config.LLMProvider{}, ErrNoProvider
}

// accountOf returns the first configured Bridge account id.
func accountOf(cfg config.Config) (string, error) {
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return "", err
	}
	return cfg.Bridge[idx].ID, nil
}

// suggestionCode maps an error to a stable, safe code for one email.
func suggestionCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return CodeCancelled
	case errors.Is(err, llm.ErrCloudDisabled):
		return CategoryCloudDisabled
	case errors.Is(err, llm.ErrTooLarge):
		return CodeTooLarge
	case errors.Is(err, llm.ErrInvalidOutput):
		return CodeInvalidJSON
	case errors.Is(err, context.DeadlineExceeded):
		return string(llm.CategoryTimeout)
	default:
		if cat := llm.CategoryOf(err); cat != llm.CategoryOK {
			return string(cat)
		}
		return CategoryOther
	}
}

// suggestionMessage maps an error to a fixed, content-free message.
func suggestionMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "the suggestion was cancelled"
	}
	return categorizeLLM(err).Message
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
