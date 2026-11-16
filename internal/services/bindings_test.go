package services

import (
	"context"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// wantBound is the complete list of methods the webview can call. Adding a
// bound method means updating this list and docs/ARCHITECTURE.md §3.3, which
// is deliberate: every entry is a trust boundary.
//
// ActionService is the one intentional exception to the read-only rule: Apply,
// Undo, UndoLast and Recent are mailbox writes / audit reads. They are safe
// because every write goes through internal/actions, which only accepts the
// allowlisted v1 action types, resolves UIDs from the cache (never from the
// frontend), applies reversible recipes with post-write verification, and
// records metadata only in audit.db. v1 has no permanent delete, no label
// create/rename and no drafts.
var wantBound = map[string][]string{
	"SetupService": {
		"GetStatus", "ProbeCert", "SaveAccount", "TestConnection", "TrustCert",
	},
	"MailService": {
		"CacheInfo", "GetAttachment", "GetMessage", "ListMailboxes", "ListMessages",
	},
	"SyncService": {
		"Start", "Status", "Stop",
	},
	"LLMService": {
		"DeleteProvider", "ListModels", "ListProviders", "SaveProvider", "SetDefaultProvider", "TestProvider",
	},
	"ActionService": {
		"Apply", "Recent", "Undo", "UndoLast",
	},
}

// lifecycle methods are called by Wails, not bound to the frontend.
var lifecycle = map[string]bool{"ServiceStartup": true, "ServiceShutdown": true}

// secretField matches struct field names that would suggest a secret.
var secretField = regexp.MustCompile(`(?i)password|passwd|secret|token|apikey|api_key|credential|^key$`)

func boundServices() map[string]reflect.Type {
	return map[string]reflect.Type{
		"SetupService":  reflect.TypeOf(&SetupService{}),
		"MailService":   reflect.TypeOf(&MailService{}),
		"SyncService":   reflect.TypeOf(&SyncService{}),
		"LLMService":    reflect.TypeOf(&LLMService{}),
		"ActionService": reflect.TypeOf(&ActionService{}),
	}
}

func TestBoundMethodSetIsExact(t *testing.T) {
	for name, typ := range boundServices() {
		var got []string
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i).Name
			if !lifecycle[m] {
				got = append(got, m)
			}
		}
		sort.Strings(got)
		want := append([]string(nil), wantBound[name]...)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s bound methods = %v, want %v", name, got, want)
		}
	}
}

func TestBoundMethodsAreReadOnlyByName(t *testing.T) {
	// A name check is a tripwire, not a proof. The mailbox-write verbs must not
	// appear anywhere. LLMService may change its own config (save, delete and
	// default a provider), but it must not reach the mailbox or send mail.
	// ActionService is the intentional write service; its write path is the
	// allowlisted internal/actions executor, covered by actions' own tests.
	mailboxWrites := regexp.MustCompile(`(?i)send|move|flag|expunge|append|smtp|draft|archive`)
	strict := regexp.MustCompile(`(?i)send|delete|move|flag|store|expunge|append|smtp|llm|draft|archive`)
	for name, typ := range boundServices() {
		re := strict
		if name == "LLMService" {
			re = mailboxWrites
		}
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i).Name
			if re.MatchString(m) {
				t.Errorf("%s.%s looks like a write or out-of-scope action", name, m)
			}
		}
	}
}

func TestBoundReturnTypesHaveNoSecretFields(t *testing.T) {
	for name, typ := range boundServices() {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			if lifecycle[m.Name] {
				continue
			}
			for j := 0; j < m.Type.NumOut(); j++ {
				checkNoSecretFields(t, name+"."+m.Name, m.Type.Out(j), map[reflect.Type]bool{})
			}
			for j := 1; j < m.Type.NumIn(); j++ {
				// Inputs may carry a secret, but only into SaveAccount (password) and
				// SaveProvider (write-only API key).
				if m.Name == "SaveAccount" || m.Name == "SaveProvider" {
					continue
				}
				checkNoSecretFields(t, name+"."+m.Name+" input", m.Type.In(j), map[reflect.Type]bool{})
			}
		}
	}
}

func checkNoSecretFields(t *testing.T, where string, typ reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return
	}
	seen[typ] = true
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if secretField.MatchString(f.Name) && !strings.HasPrefix(typ.String(), "time.") {
			t.Errorf("%s: %s.%s looks like a secret field", where, typ.Name(), f.Name)
		}
		checkNoSecretFields(t, where, f.Type, seen)
	}
}

// TestActionDTOContract pins the fields the frontend relies on. Adding a field
// is fine; renaming or dropping one on this list breaks PR #22.
func TestActionDTOContract(t *testing.T) {
	result := reflect.TypeOf(ActionResultDTO{})
	for _, name := range []string{"Applied", "Failed", "AppliedCount", "FailedCount", "BatchID", "AuditIDs", "Code"} {
		if _, ok := result.FieldByName(name); !ok {
			t.Errorf("ActionResultDTO lacks %s", name)
		}
	}
	audit := reflect.TypeOf(AuditDTO{})
	for _, name := range []string{"ID", "TS", "Type", "Params", "Count", "Error", "Undone", "BatchID", "AuditIDs", "Code"} {
		if _, ok := audit.FieldByName(name); !ok {
			t.Errorf("AuditDTO lacks %s", name)
		}
	}
}

// wailsCtxMethods take the Wails call context first. It is not visible to
// JavaScript; it ends when the call is cancelled or the app shuts down.
var wailsCtxMethods = map[string][]string{
	"SetupService":  {"GetStatus", "ProbeCert", "SaveAccount", "TestConnection", "TrustCert"},
	"MailService":   {"CacheInfo", "GetAttachment", "GetMessage", "ListMailboxes", "ListMessages"},
	"SyncService":   {},
	"LLMService":    {"ListModels", "TestProvider"},
	"ActionService": {"Apply", "Recent", "Undo", "UndoLast"},
}

func TestBoundMethodsTakeWailsContext(t *testing.T) {
	ctxType := reflect.TypeFor[context.Context]()
	for name, typ := range boundServices() {
		want := map[string]bool{}
		for _, m := range wailsCtxMethods[name] {
			want[m] = true
		}
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			if lifecycle[m.Name] {
				continue
			}
			// Method.Type includes the receiver as input 0.
			hasCtx := m.Type.NumIn() > 1 && m.Type.In(1) == ctxType
			if hasCtx != want[m.Name] {
				t.Errorf("%s.%s: context first = %v, want %v", name, m.Name, hasCtx, want[m.Name])
			}
		}
	}
}
