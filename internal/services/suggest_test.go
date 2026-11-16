package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/actions"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/llm"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
	"github.com/MaksimZadar/temail/internal/suggest"
)

func suggestTestConfig() config.Config {
	c := readyConfig()
	c.LLM.Providers = []config.LLMProvider{
		{Name: "local", BaseURL: "http://127.0.0.1:11434/v1", Model: "m", Kind: config.KindLocal,
			Capabilities: config.LLMCapabilities{StructuredOutput: true}},
		{Name: "cloud", BaseURL: "https://llm.example.test/v1", Model: "m", Kind: config.KindCloud},
	}
	c.LLM.DefaultProvider = "local"
	return c
}

type suggestFixture struct {
	svc *SuggestionService
	st  *fakeStore
	em  *fakeEmitter
	cfg *fakeConfig
	eng *fakeActionEngine
}

func newSuggestFixture(t *testing.T) *suggestFixture {
	t.Helper()
	st := newFakeStore()
	cfg := newFakeConfig(suggestTestConfig())
	eng := &fakeActionEngine{}
	em := &fakeEmitter{}
	llmSvc := NewLLMService(cfg, newFakeKeys(), nil)
	actionSvc := NewActionService(st, cfg, nil, nil, nil, em)
	actionSvc.eventDelay = 5 * time.Millisecond
	actionSvc.newEngine = func(string, *actions.Audit) (ActionEngine, error) { return eng, nil }
	svc := NewSuggestionService(st, cfg, llmSvc, actionSvc, em)
	svc.emailTimeout = time.Second
	t.Cleanup(func() { _ = svc.shutdown() })
	t.Cleanup(func() { _ = actionSvc.shutdown() })
	return &suggestFixture{svc: svc, st: st, em: em, cfg: cfg, eng: eng}
}

// addMessage puts one cached message with a folder and optional labels into the
// fake store.
func (f *suggestFixture) addMessage(mid, mailbox string, uid uint32, subject, body string, labels ...string) {
	key := store.MessageKey{Account: "main", Mailbox: mailbox, UIDValidity: 1, UID: uid}
	h := model.MessageHeader{
		Mailbox: mailbox, UIDValidity: 1, UID: uid, InternalID: mid,
		Subject: subject, From: []model.Address{{Name: "Sender", Email: "sender@example.test"}},
	}
	placements := []store.Placement{{
		Mailbox: mailbox, DisplayName: mailbox, Kind: model.KindInbox,
		UIDValidity: 1, UID: uid, Canonical: true,
	}}
	if mailbox == "INBOX" {
		placements[0].Kind = model.KindInbox
	}
	for _, name := range labels {
		placements = append(placements, store.Placement{
			Mailbox: "Labels/" + name, DisplayName: name, Kind: model.KindLabel,
			UIDValidity: 1, UID: uid, Canonical: true,
		})
	}
	f.st.headers[key] = h
	if body != "" {
		f.st.bodies[key] = store.Body{Text: body}
	}
	f.st.membersByMID[mid] = placements
}

func (f *suggestFixture) addMailboxes() {
	f.st.states = []store.MailboxState{
		{Name: "INBOX", DisplayName: "INBOX", Kind: model.KindInbox, UIDValidity: 1},
		{Name: "Labels/Work", DisplayName: "Work", Kind: model.KindLabel},
		{Name: "Folders/Projects", DisplayName: "Projects", Kind: model.KindFolder},
	}
}

func (e *fakeEmitter) snapshot() []emitted {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]emitted(nil), e.events...)
}

func waitDone(t *testing.T, em *fakeEmitter) SuggestDoneDTO {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range em.snapshot() {
			if ev.name == EventSuggestDone {
				d, _ := ev.data.(SuggestDoneDTO)
				return d
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no suggest:done event")
	return SuggestDoneDTO{}
}

func resultsOf(em *fakeEmitter, jobID string) []SuggestResultDTO {
	var out []SuggestResultDTO
	for _, ev := range em.snapshot() {
		if ev.name != EventSuggestResult {
			continue
		}
		if r, ok := ev.data.(SuggestResultDTO); ok && r.JobID == jobID {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeActionEngine) createdLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.created...)
}

func (f *fakeActionEngine) appliedActions() []actions.Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]actions.Action(nil), f.applied...)
}

func TestSuggestStreamsResultsAndStatus(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "Invoice", "Please pay the invoice.", "Work")
	fx.addMessage("m2", "INBOX", 2, "Newsletter", "Weekly digest.")

	var (
		mu                    sync.Mutex
		gotLabels, gotFolders []string
	)
	setOne(fx.svc, func(ctx context.Context, msg suggest.Message, existing, folders []string) (suggest.Suggestion, error) {
		mu.Lock()
		gotLabels, gotFolders = existing, folders
		mu.Unlock()
		return suggest.Suggestion{
			Summary: "A message.",
			Labels: []suggest.Label{
				{Name: "Work", Confidence: 0.9, Reason: "work"},
				{Name: "Receipts", Confidence: 0.8, Reason: "invoice"},
				{Name: "Extra", Confidence: 0.7, Reason: "new"},
				{Name: "Third", Confidence: 0.6, Reason: "capped"},
			},
		}, nil
	})

	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1", "m2", "m1", " "}, "")
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	done := waitDone(t, fx.em)
	if done.JobID != jobID || done.Total != 2 || done.Failed != 0 {
		t.Fatalf("done = %+v", done)
	}
	if !strings.Contains(strings.Join(gotLabels, ","), "Work") {
		t.Errorf("existing labels passed to the model = %v", gotLabels)
	}
	if !strings.Contains(strings.Join(gotFolders, ","), "Projects") {
		t.Errorf("folders passed to the model = %v", gotFolders)
	}
	res := resultsOf(fx.em, jobID)
	if len(res) != 2 {
		t.Fatalf("result events = %d, want 2", len(res))
	}
	for _, r := range res {
		if !r.OK || r.Code != "" {
			t.Errorf("result = %+v", r)
		}
		if len(r.Labels) != 3 {
			t.Errorf("%s labels = %d, want 3 (2 new + 1 existing)", r.MID, len(r.Labels))
		}
	}
	st, err := fx.svc.Status(jobID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != SuggestStateDone || st.Total != 2 || st.Completed != 2 || len(st.Results) != 2 {
		t.Errorf("status = %+v", st)
	}
	if st.Provider != "local" {
		t.Errorf("provider = %q, want local", st.Provider)
	}
	if _, err := fx.svc.Status("nope"); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("Status(unknown) = %v, want ErrJobNotFound", err)
	}
}

func TestSuggestBatchCapAndDedupe(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	ids := make([]string, 0, suggest.MaxBatch+1)
	for i := 0; i <= suggest.MaxBatch; i++ {
		ids = append(ids, "m"+string(rune('a'+i)))
	}
	if _, err := fx.svc.Suggest(context.Background(), ids, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Suggest(26) = %v, want ErrInvalidInput", err)
	}
	if _, err := fx.svc.Suggest(context.Background(), []string{" ", ""}, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Suggest(empty) = %v, want ErrInvalidInput", err)
	}
}

func TestSuggestRefusesDisabledCloudAndUnknownProvider(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "Hi", "Body")
	if _, err := fx.svc.Suggest(context.Background(), []string{"m1"}, "cloud"); !errors.Is(err, ErrSuggestCloudDisabled) {
		t.Fatalf("disabled cloud = %v, want ErrSuggestCloudDisabled", err)
	}
	if strings.Contains(ErrSuggestCloudDisabled.Error(), "secret") {
		t.Error("error text mentions a secret")
	}
	if _, err := fx.svc.Suggest(context.Background(), []string{"m1"}, "missing"); !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("unknown provider = %v, want ErrProviderNotFound", err)
	}
	if _, err := fx.svc.Status(""); !errors.Is(err, ErrJobNotFound) {
		t.Error("a refused call must not create a job")
	}
}

func TestSuggestFailureIsolation(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "One", "Body")
	fx.addMessage("m2", "INBOX", 2, "Two", "Body")
	setOne(fx.svc, func(ctx context.Context, msg suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		if msg.MID == "m1" {
			return suggest.Suggestion{}, errors.New("provider exploded")
		}
		return suggest.Suggestion{Summary: "ok", Labels: []suggest.Label{{Name: "Work", Confidence: 1}}}, nil
	})
	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1", "m2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, fx.em)
	if done.Failed != 1 {
		t.Fatalf("done = %+v", done)
	}
	byMID := map[string]SuggestResultDTO{}
	for _, r := range resultsOf(fx.em, jobID) {
		byMID[r.MID] = r
	}
	if byMID["m1"].OK || byMID["m1"].Code != CategoryOther || byMID["m1"].Error == "" {
		t.Errorf("m1 = %+v", byMID["m1"])
	}
	if !byMID["m2"].OK || len(byMID["m2"].Labels) != 1 {
		t.Errorf("m2 = %+v", byMID["m2"])
	}
}

func TestSuggestTimeoutAndMissingMessage(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "One", "Body")
	fx.svc.emailTimeout = 20 * time.Millisecond
	setOne(fx.svc, func(ctx context.Context, _ suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		select {
		case <-ctx.Done():
			return suggest.Suggestion{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
			return suggest.Suggestion{Summary: "late"}, nil
		}
	})
	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1", "ghost"}, "")
	if err != nil {
		t.Fatal(err)
	}
	done := waitDone(t, fx.em)
	if done.Total != 2 || done.Failed != 2 {
		t.Fatalf("done = %+v", done)
	}
	byMID := map[string]SuggestResultDTO{}
	for _, r := range resultsOf(fx.em, jobID) {
		byMID[r.MID] = r
	}
	if byMID["ghost"].Code != CodeNotCached {
		t.Errorf("ghost = %+v", byMID["ghost"])
	}
	if byMID["m1"].Code != string(llm.CategoryTimeout) {
		t.Errorf("timeout code = %q", byMID["m1"].Code)
	}
}

func TestSuggestCancelDiscardsResults(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "One", "Body")
	started := make(chan struct{}, 1)
	setOne(fx.svc, func(ctx context.Context, msg suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		time.Sleep(200 * time.Millisecond)
		return suggest.Suggestion{Summary: "late"}, nil
	})
	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the job never started")
	}
	if err := fx.svc.Cancel(jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitDone(t, fx.em)
	st, err := fx.svc.Status(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != SuggestStateCancelled || len(st.Results) != 0 {
		t.Errorf("status after cancel = %+v", st)
	}
	if _, err := fx.svc.ApplySuggestions(context.Background(), jobID, map[string][]string{"m1": {"Work"}}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("apply after cancel = %v, want ErrInvalidInput", err)
	}
}

func TestSuggestConcurrencyIsTwo(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	for i := 0; i < 4; i++ {
		fx.addMessage("m"+string(rune('1'+i)), "INBOX", uint32(i+1), "S", "B")
	}
	var mu sync.Mutex
	active, peak := 0, 0
	setOne(fx.svc, func(ctx context.Context, _ suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		mu.Lock()
		active++
		if active > peak {
			peak = active
		}
		mu.Unlock()
		time.Sleep(25 * time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		return suggest.Suggestion{Summary: "ok"}, nil
	})
	if _, err := fx.svc.Suggest(context.Background(), []string{"m1", "m2", "m3", "m4"}, ""); err != nil {
		t.Fatal(err)
	}
	waitDone(t, fx.em)
	mu.Lock()
	defer mu.Unlock()
	if peak != 2 {
		t.Errorf("peak concurrency = %d, want 2", peak)
	}
}

func TestApplySuggestionsCreatesLabelAndAddsLabels(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "Invoice", "Body", "Work")
	setOne(fx.svc, func(ctx context.Context, _ suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		return suggest.Suggestion{Summary: "ok", Labels: []suggest.Label{
			{Name: "Work", Confidence: 1, Existing: true},
			{Name: "Receipts", Confidence: 0.9},
		}}, nil
	})
	fx.eng.res = actions.Result{
		Applied: []string{"m1"}, BatchID: "b1",
		Entries: []actions.ResultEntry{{MID: "m1", AuditID: 7, Undoable: true}},
	}

	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, fx.em)

	res, err := fx.svc.ApplySuggestions(context.Background(), jobID, map[string][]string{"m1": {"Work", "Receipts"}})
	if err != nil {
		t.Fatalf("ApplySuggestions: %v", err)
	}
	if len(res.CreatedLabels) != 1 || res.CreatedLabels[0] != "Receipts" {
		t.Errorf("created labels = %v", res.CreatedLabels)
	}
	if res.AppliedCount != 1 || res.FailedCount != 0 || len(res.Applied) != 1 {
		t.Errorf("result = %+v", res)
	}
	if got := fx.eng.createdLabels(); len(got) != 1 || got[0] != "Receipts" {
		t.Errorf("engine created = %v", got)
	}
	applied := fx.eng.appliedActions()
	if len(applied) != 2 {
		t.Fatalf("apply calls = %d, want one per label", len(applied))
	}
	labels := map[string]bool{}
	for _, a := range applied {
		if a.Type != actions.AddLabel {
			t.Errorf("type = %s", a.Type)
		}
		labels[a.Params.Label] = true
		if len(a.MessageIDs) != 1 || a.MessageIDs[0] != "m1" {
			t.Errorf("ids = %v", a.MessageIDs)
		}
	}
	if !labels["Work"] || !labels["Receipts"] {
		t.Errorf("applied labels = %v", labels)
	}
}

func TestApplySuggestionsRejectsUnknownAndReportsCreateFailure(t *testing.T) {
	fx := newSuggestFixture(t)
	fx.addMailboxes()
	fx.addMessage("m1", "INBOX", 1, "Invoice", "Body")
	setOne(fx.svc, func(ctx context.Context, _ suggest.Message, _, _ []string) (suggest.Suggestion, error) {
		return suggest.Suggestion{Summary: "ok", Labels: []suggest.Label{{Name: "Receipts", Confidence: 0.9}}}, nil
	})
	jobID, err := fx.svc.Suggest(context.Background(), []string{"m1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, fx.em)

	if _, err := fx.svc.ApplySuggestions(context.Background(), jobID, map[string][]string{"m1": {"NotSuggested"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown label = %v, want ErrInvalidInput", err)
	}
	if _, err := fx.svc.ApplySuggestions(context.Background(), jobID, map[string][]string{"other": {"Receipts"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown message = %v, want ErrInvalidInput", err)
	}
	if got := fx.eng.createdLabels(); len(got) != 0 {
		t.Fatalf("a rejected call created labels: %v", got)
	}

	fx.eng.createErr = errors.New("bridge refused")
	res, err := fx.svc.ApplySuggestions(context.Background(), jobID, map[string][]string{"m1": {"Receipts"}})
	if err != nil {
		t.Fatalf("ApplySuggestions: %v", err)
	}
	if res.FailedCount != 1 || res.Failed[0].Code != CodeCreateLabel || res.CreatedLabels == nil || len(res.CreatedLabels) != 0 {
		t.Errorf("result = %+v", res)
	}
	if got := len(fx.eng.appliedActions()); got != 0 {
		t.Errorf("apply ran after a failed create: %d calls", got)
	}
}

// setOne replaces the per-email call with a fake that ignores the llm client.
// The service type is concrete, so the adapter keeps the real signature.
func setOne(s *SuggestionService, fake func(ctx context.Context, msg suggest.Message, existing, folders []string) (suggest.Suggestion, error)) {
	s.one = func(ctx context.Context, _ *llm.Client, msg suggest.Message, existing, folders []string) (suggest.Suggestion, error) {
		return fake(ctx, msg, existing, folders)
	}
}
