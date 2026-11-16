package actions

import (
	"errors"
	"strings"
	"testing"

	"github.com/MaksimZadar/temail/internal/model"
)

func TestCreateLabelCreatesVerifiesCachesAndAudits(t *testing.T) {
	fx := newFixture(t)
	if err := fx.ex.CreateLabel(fx.ctx, "Receipts"); err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if !fx.fw.hasMailbox("Labels/Receipts") {
		t.Fatal("the writer did not create Labels/Receipts")
	}
	if n := fx.fw.callCount("CREATE Labels/Receipts"); n != 1 {
		t.Errorf("CREATE calls = %d, want 1", n)
	}
	states, err := fx.st.GetSyncState(fx.ctx, testAccount)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range states {
		if s.Name == "Labels/Receipts" {
			found = true
			if s.Kind != model.KindLabel || s.DisplayName != "Receipts" {
				t.Errorf("cached state = %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("the new label is not in the cache, so add_label cannot resolve it")
	}
	entries, err := fx.audit.Recent(fx.ctx, testAccount, 10)
	if err != nil {
		t.Fatal(err)
	}
	var create *Entry
	for i := range entries {
		if entries[i].Type == AuditTypeCreateLabel {
			create = &entries[i]
		}
	}
	if create == nil {
		t.Fatal("no create_label audit row")
	}
	if create.Undoable {
		t.Error("a create_label row must not be undoable")
	}
	if !strings.Contains(create.ParamsJSON, "Receipts") {
		t.Errorf("params = %s", create.ParamsJSON)
	}
	if _, err := fx.ex.Undo(fx.ctx, create.ID); !errors.Is(err, ErrNotUndoable) {
		t.Errorf("Undo(create_label) = %v, want ErrNotUndoable", err)
	}
}

func TestCreateLabelRefusesUnsafeNames(t *testing.T) {
	fx := newFixture(t)
	for _, name := range []string{"INBOX", "All Mail", "a/b", `a\b`, " padded", strings.Repeat("x", 65), ".."} {
		if err := fx.ex.CreateLabel(fx.ctx, name); !errors.Is(err, ErrInvalidAction) {
			t.Errorf("CreateLabel(%q) = %v, want ErrInvalidAction", name, err)
		}
	}
	if n := fx.fw.callCount("CREATE "); n != 0 {
		t.Errorf("unsafe names reached the writer: %d CREATE calls", n)
	}
}
