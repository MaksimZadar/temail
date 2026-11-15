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
}

// lifecycle methods are called by Wails, not bound to the frontend.
var lifecycle = map[string]bool{"ServiceStartup": true, "ServiceShutdown": true}

// secretField matches struct field names that would suggest a secret.
var secretField = regexp.MustCompile(`(?i)password|passwd|secret|token|apikey|api_key|credential|^key$`)

func boundServices() map[string]reflect.Type {
	return map[string]reflect.Type{
		"SetupService": reflect.TypeOf(&SetupService{}),
		"MailService":  reflect.TypeOf(&MailService{}),
		"SyncService":  reflect.TypeOf(&SyncService{}),
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
	// A name check is a tripwire, not a proof. The write verbs must not appear.
	forbidden := regexp.MustCompile(`(?i)send|delete|move|flag|store|expunge|append|smtp|llm|draft|archive`)
	for name, typ := range boundServices() {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i).Name
			if forbidden.MatchString(m) {
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
				// Inputs may carry the password, but only into SaveAccount.
				if m.Name == "SaveAccount" {
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

// wailsCtxMethods take the Wails call context first. It is not visible to
// JavaScript; it ends when the call is cancelled or the app shuts down.
var wailsCtxMethods = map[string][]string{
	"SetupService": {"GetStatus", "ProbeCert", "SaveAccount", "TestConnection", "TrustCert"},
	"MailService":  {"CacheInfo", "GetAttachment", "GetMessage", "ListMailboxes", "ListMessages"},
	"SyncService":  {},
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
