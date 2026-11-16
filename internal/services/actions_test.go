package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/actions"
	"github.com/MaksimZadar/temail/internal/config"
)

type fakeActionEngine struct {
	mu      sync.Mutex
	applied []actions.Action
	res     actions.Result
	err     error
	undoID  int64
	recent  []actions.Entry
}

func (f *fakeActionEngine) Apply(_ context.Context, a actions.Action) (actions.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, a)
	return f.res, f.err
}

func (f *fakeActionEngine) Undo(_ context.Context, id int64) (actions.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.undoID = id
	return f.res, f.err
}

func (f *fakeActionEngine) UndoLast(context.Context) (actions.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.res, f.err
}

func (f *fakeActionEngine) Recent(context.Context, int) ([]actions.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recent, f.err
}

func (f *fakeActionEngine) last() actions.Action {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		return actions.Action{}
	}
	return f.applied[len(f.applied)-1]
}

func newActionSvc(t *testing.T, eng ActionEngine, emitter Emitter) *ActionService {
	t.Helper()
	cfg := newFakeConfig(config.Config{Bridge: []config.BridgeAccount{{ID: "acct"}}})
	s := NewActionService(nil, cfg, nil, nil, nil, emitter)
	s.eventDelay = 5 * time.Millisecond
	s.newEngine = func(string, *actions.Audit) (ActionEngine, error) { return eng, nil }
	t.Cleanup(func() { _ = s.shutdown() })
	return s
}

func TestActionServiceApplyMapsDTOAndEmitsOneEvent(t *testing.T) {
	eng := &fakeActionEngine{res: actions.Result{Applied: []string{"m1", "m2"}, Failed: []actions.Failure{{MID: "m3", Mailbox: "INBOX", Message: "boom"}}}}
	em := &fakeEmitter{}
	svc := newActionSvc(t, eng, em)

	res, err := svc.Apply(context.Background(), ActionDTO{
		Type: "move", Params: ActionParamsDTO{Folder: "Folders/Work"}, MessageIDs: []string{"m1", "m2", "m3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := eng.last()
	if got.Type != actions.Move || got.Params.Folder != "Folders/Work" || len(got.MessageIDs) != 3 {
		t.Fatalf("engine got %+v", got)
	}
	if res.AppliedCount != 2 || res.FailedCount != 1 || res.Failed[0].MID != "m3" {
		t.Fatalf("result = %+v", res)
	}
	if _, err := svc.Apply(context.Background(), ActionDTO{Type: "star", MessageIDs: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		em.mu.Lock()
		n := len(em.events)
		em.mu.Unlock()
		if n > 0 {
			if n != 1 {
				t.Fatalf("events = %v, want one coalesced event", em.names())
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no event emitted")
		}
		time.Sleep(time.Millisecond)
	}
	em.mu.Lock()
	ev, ok := em.events[0].data.(ActionsDoneEvent)
	em.mu.Unlock()
	if !ok || ev.Count != 4 || ev.Failed != 2 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestActionServiceRejectsInvalidInput(t *testing.T) {
	eng := &fakeActionEngine{}
	svc := newActionSvc(t, eng, &fakeEmitter{})
	if _, err := svc.Apply(context.Background(), ActionDTO{Type: "send", MessageIDs: []string{"m1"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.Apply(context.Background(), ActionDTO{Type: "star"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty ids: err = %v", err)
	}
	eng.mu.Lock()
	n := len(eng.applied)
	eng.mu.Unlock()
	if n != 0 {
		t.Fatal("engine was called for invalid input")
	}
}

func TestActionServiceUndoAndRecent(t *testing.T) {
	eng := &fakeActionEngine{
		res: actions.Result{Applied: []string{"m1"}},
		recent: []actions.Entry{{
			ID: 7, TS: time.Unix(1_700_000_000, 0), Type: "move",
			ParamsJSON: `{"folder":"Folders/Work"}`, Error: "", Undone: false,
			Count: 1, Undoable: true, AuditIDs: []int64{7},
		}},
	}
	svc := newActionSvc(t, eng, nil)
	if res, err := svc.Undo(context.Background(), 7); err != nil || res.AppliedCount != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if eng.undoID != 7 {
		t.Fatalf("undo id = %d", eng.undoID)
	}
	recent, err := svc.Recent(context.Background(), 10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("%+v %v", recent, err)
	}
	if recent[0].Params.Folder != "Folders/Work" || recent[0].Count != 1 || recent[0].Undone {
		t.Fatalf("recent = %+v", recent[0])
	}
	if len(recent[0].AuditIDs) != 1 || recent[0].Code != "" {
		t.Fatalf("recent undo handles = %+v", recent[0])
	}
	if _, err := svc.Undo(context.Background(), 0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("id 0: err = %v", err)
	}
}

// TestActionServiceUndoCodes: undo refusals come back as a DTO code with a nil
// error, so the frontend can render them without treating them as failures.
func TestActionServiceUndoCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code string
	}{
		{"not undoable", actions.ErrNotUndoable, CodeNotUndoable},
		{"already undone", actions.ErrAlreadyUndone, CodeAlreadyUndone},
		{"not found", actions.ErrAuditNotFound, CodeNotFound},
		{"wrong account", actions.ErrWrongAccount, CodeWrongAccount},
		{"diverged", actions.ErrDiverged, CodeDiverged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &fakeActionEngine{err: tc.err}
			svc := newActionSvc(t, eng, nil)
			res, err := svc.Undo(context.Background(), 5)
			if err != nil {
				t.Fatalf("error = %v, want nil with a code", err)
			}
			if res.Code != tc.code {
				t.Fatalf("code = %q, want %q", res.Code, tc.code)
			}
			res, err = svc.UndoLast(context.Background())
			if err != nil || res.Code != tc.code {
				t.Fatalf("UndoLast: code=%q err=%v", res.Code, err)
			}
		})
	}
}

func TestActionServiceRefusesAfterShutdown(t *testing.T) {
	svc := newActionSvc(t, &fakeActionEngine{}, nil)
	if err := svc.shutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(context.Background(), ActionDTO{Type: "star", MessageIDs: []string{"m1"}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestActionDTOsAreCamelCaseAndSafe(t *testing.T) {
	out, err := json.Marshal(ActionResultDTO{
		Applied: []string{"m1"}, Failed: []ActionFailureDTO{{MID: "m2", Mailbox: "INBOX", Message: "x"}},
		AppliedCount: 1, FailedCount: 1, BatchID: "b1", AuditIDs: []int64{3}, Code: CodeDiverged,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"appliedCount":1`, `"failedCount":1`, `"message"`, `"batchId":"b1"`, `"auditIds":[3]`, `"code":"diverged"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("JSON %s lacks %s", out, want)
		}
	}
	audit, err := json.Marshal(AuditDTO{
		ID: 1, TS: time.Unix(0, 0), Type: "star", Count: 2, BatchID: "b1", AuditIDs: []int64{1, 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"undone":false`, `"count":2`, `"batchId":"b1"`, `"auditIds":[1,2]`} {
		if !strings.Contains(string(audit), want) {
			t.Errorf("audit JSON %s lacks %s", audit, want)
		}
	}
	if strings.Contains(string(audit), "subject") || strings.Contains(string(audit), "body") {
		t.Errorf("audit DTO carries message content: %s", audit)
	}
}
