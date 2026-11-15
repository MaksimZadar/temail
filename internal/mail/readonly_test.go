package mail

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
)

// writeCommandNames are the IMAP operations that change mailbox state or
// mailbox structure. The mail package must never issue any of them.
var writeCommandNames = []string{
	"STORE", "COPY", "MOVE", "EXPUNGE", "APPEND", "CREATE", "DELETE", "RENAME",
	"SUBSCRIBE", "UNSUBSCRIBE", "SETMETADATA", "SETQUOTA",
}

// TestClientInterfaceHasNoWriteMethods checks the Client interface's method
// names. Any method named after a write command fails the test.
func TestClientInterfaceHasNoWriteMethods(t *testing.T) {
	typ := reflect.TypeOf((*Client)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		name := strings.ToUpper(typ.Method(i).Name)
		for _, w := range writeCommandNames {
			if strings.Contains(name, w) {
				t.Errorf("Client method %s looks like a write operation (%s)", typ.Method(i).Name, w)
			}
		}
	}
	want := []string{"Connect", "ListMailboxes", "MailboxInfo", "FetchHeaders", "ListUIDs", "FetchMessage", "FetchAttachment", "Close"}
	if typ.NumMethod() != len(want) {
		t.Errorf("Client has %d methods, want exactly %d (%v): adding a method needs a read-only review", typ.NumMethod(), len(want), want)
	}
}

// writeCallPattern matches a call to an IMAP write method on any receiver.
// It is applied to the package's non-test source.
var writeCallPattern = regexp.MustCompile(
	`\.(Store|UIDStore|Copy|UIDCopy|Move|UIDMove|Expunge|UIDExpunge|UnselectAndExpunge|` +
		`Append|Create|Delete|Rename|Subscribe|Unsubscribe|SetMetadata|SetQuota)\(`)

// TestNoWriteCallsInSource is a static review guard. It parses every non-test
// file in the package and fails on a selector call to a write method. It
// complements the runtime check in TestReadOnlyAcrossAllOperations.
func TestNoWriteCallsInSource(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if writeCallPattern.MatchString("." + sel.Sel.Name + "(") {
				t.Errorf("%s: call to write method %s", fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no source files checked")
	}
}

// TestReadOnlyAcrossAllOperations runs every operation against the recording
// server. It then checks that no write call reached the server, that every
// SELECT was read-only (EXAMINE), and that every body fetch used PEEK.
func TestReadOnlyAcrossAllOperations(t *testing.T) {
	for _, mode := range bothModes {
		t.Run(mode, func(t *testing.T) {
			fx := newBridgeFixture(t, mode)
			fx.ensureMailbox("Folders/Work")
			fx.ensureMailbox("Archive")
			fx.appendMessage("INBOX", altMessage())
			fx.appendMessage("INBOX", remoteImageMessage())
			fx.appendMessage("INBOX", attachmentMessage())
			ctx := context.Background()

			c := fx.mustConnect()
			if _, err := c.ListMailboxes(ctx); err != nil {
				t.Fatalf("ListMailboxes: %v", err)
			}
			if _, err := c.MailboxInfo(ctx, "INBOX"); err != nil {
				t.Fatalf("MailboxInfo: %v", err)
			}
			if _, err := c.FetchHeaders(ctx, "INBOX", AllUIDs()); err != nil {
				t.Fatalf("FetchHeaders: %v", err)
			}
			if _, err := c.ListUIDs(ctx, "INBOX"); err != nil {
				t.Fatalf("ListUIDs: %v", err)
			}
			for uid := uint32(1); uid <= 3; uid++ {
				if _, err := c.FetchMessage(ctx, "INBOX", uid); err != nil {
					t.Fatalf("FetchMessage %d: %v", uid, err)
				}
			}
			if _, err := c.FetchAttachment(ctx, "INBOX", 3, "2"); err != nil {
				t.Fatalf("FetchAttachment: %v", err)
			}
			if err := c.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			st := fx.rec.stats()
			if len(st.writes) != 0 {
				t.Errorf("write commands reached the server: %v", st.writes)
			}
			if len(st.selectsRO) == 0 {
				t.Fatal("no SELECT/EXAMINE recorded; recorder not wired")
			}
			for i, ro := range st.selectsRO {
				if !ro {
					t.Errorf("SELECT #%d was not read-only (EXAMINE)", i)
				}
			}
			if st.nonPeek != 0 {
				t.Errorf("%d body fetches without PEEK (would set \\Seen)", st.nonPeek)
			}
		})
	}
}

// TestRecorderSeesWriteAttempts makes sure the recorder is not a no-op: a
// write sent straight to the recording session is rejected and recorded.
func TestRecorderSeesWriteAttempts(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	s := &recordingSession{rec: fx.rec}
	if err := s.Delete("INBOX"); err == nil {
		t.Fatal("write was not rejected")
	}
	if got := fx.rec.stats().writes; len(got) != 1 || got[0] != "DELETE" {
		t.Errorf("recorded writes = %v", got)
	}
}
