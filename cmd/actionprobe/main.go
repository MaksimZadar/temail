// Command actionprobe is an opt-in manual probe for the write recipes in
// internal/actions. It is not run in CI and the app never calls it.
//
// It changes one chosen message only. Without -yes it prints the plan and
// changes nothing. With -yes it toggles and restores flags, adds and removes a
// test label (when it exists), and archives the message and undoes it. Trash +
// undo runs only with -destructive, because a trash move is the one recipe a
// user may not want on a real message.
//
// Every write goes through the same internal/actions executor as the app, so
// the probe exercises the real recipes, verification, cache update and audit
// log. It requires a synced cache: run cmd/mailsync first.
//
// Usage:
//
//	actionprobe -mailbox INBOX -uid 42           # print the plan, write nothing
//	actionprobe -mailbox INBOX -uid 42 -yes      # run the reversible recipes
//	actionprobe -mailbox INBOX -uid 42 -yes -destructive
//
// The test label defaults to "temail-test"; create it in Proton if missing.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/MaksimZadar/temail/internal/actions"
	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/secrets"
	"github.com/MaksimZadar/temail/internal/store"
)

func main() {
	accountID := flag.String("account", "", "account ID from config.json (default: the first account)")
	mailbox := flag.String("mailbox", "", "mailbox that holds the message (for example INBOX)")
	uid := flag.Uint("uid", 0, "UID of the message in that mailbox")
	yes := flag.Bool("yes", false, "run the recipes; without it the probe only prints the plan")
	destructive := flag.Bool("destructive", false, "also run trash + undo")
	label := flag.String("label", "temail-test", "existing label to add and remove")
	cachePath := flag.String("cache", "", "cache database path (default: store.DefaultPath)")
	trust := flag.Bool("trust", false, "pin the presented certificate fingerprint if it is not pinned yet")
	flag.Usage = usage
	flag.Parse()

	if *mailbox == "" || *uid == 0 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *accountID, *mailbox, uint32(*uid), *yes, *destructive, *label, *cachePath, *trust); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "actionprobe: interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "actionprobe:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: actionprobe -mailbox <name> -uid <n> [-yes] [-destructive]\n\n")
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Without -yes nothing is written: the probe prints the plan.
-yes toggles and restores \Seen and \Flagged, adds and removes the -label label
if that label exists, and archives then undoes. -destructive adds trash + undo.
The cache must be synced first: run mailsync.
`)
}

func run(ctx context.Context, accountID, mailbox string, uid uint32, yes, destructive bool, label, cachePath string, trust bool) error {
	mgr, err := config.DefaultManager()
	if err != nil {
		return err
	}
	cfg, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, accountID)
	if err != nil {
		return err
	}
	password, err := bridge.Password(secrets.NewKeyringStore(), cfg.Bridge[idx].ID)
	if err != nil {
		return fmt.Errorf("read Bridge password: %w", err)
	}
	if err := bridge.EnsurePinned(ctx, mgr, &cfg, idx, password, trust, func(fp, subject string) {
		fmt.Printf("Bridge certificate is not pinned yet.\n  SHA-256: %s\n  Subject: %s\n", fp, subject)
		fmt.Println("Compare it with Proton Bridge before running with -trust.")
	}); err != nil {
		return err
	}
	acct := cfg.Bridge[idx]

	if cachePath == "" {
		if cachePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	st, err := store.Open(cachePath)
	if err != nil {
		return fmt.Errorf("open cache: %w", err)
	}
	defer st.Close()

	p, err := newProbe(ctx, st, acct, mailbox, uid)
	if err != nil {
		return err
	}

	if !yes {
		fmt.Printf("Message: mid %s in %s (UID %d)\n\n", p.mid, mailbox, uid)
		fmt.Println("Plan (nothing is written without -yes):")
		for _, r := range p.recipes(destructive) {
			fmt.Printf("  %-22s %s\n", r.name, r.expected)
		}
		fmt.Printf("\nThe test label is %q (create Labels/%s in Proton if it is missing).\n", label, label)
		return nil
	}

	audit, err := actions.OpenAudit("")
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer audit.Close()

	ex, err := actions.New(st, mail.NewWriter(), audit, actions.Options{
		Account: acct.ID,
		Resolve: func(context.Context) (config.BridgeAccount, string, error) {
			return acct, password, nil
		},
		Logf: func(format string, args ...any) {
			fmt.Printf("  note: "+format+"\n", args...)
		},
	})
	if err != nil {
		return err
	}
	defer ex.Close()
	p.ex = ex
	p.label = label

	fmt.Printf("Message: mid %s in %s (UID %d)\n\n", p.mid, mailbox, uid)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "recipe\texpected\tobserved\tOK")
	failed := false
	for _, r := range p.recipes(destructive) {
		observed, skipped, err := r.run()
		ok := "yes"
		switch {
		case skipped:
			ok = "skip"
		case err != nil:
			ok = "NO"
			failed = true
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.name, r.expected, observed, ok)
		if err != nil {
			fmt.Fprintf(w, "\t\t%v\t\n", err)
		}
	}
	_ = w.Flush()
	if failed {
		return errors.New("one or more recipes failed")
	}
	return nil
}

type probe struct {
	ctx     context.Context
	st      store.Store
	account string
	mailbox string
	uid     uint32
	mid     string
	label   string
	ex      *actions.Executor
}

func newProbe(ctx context.Context, st store.Store, acct config.BridgeAccount, mailbox string, uid uint32) (*probe, error) {
	states, err := st.GetSyncState(ctx, acct.ID)
	if err != nil {
		return nil, fmt.Errorf("read sync state: %w", err)
	}
	var uidValidity uint32
	found := false
	for _, s := range states {
		if s.Name == mailbox {
			uidValidity, found = s.UIDValidity, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("mailbox %q is not in the cache; run mailsync first", mailbox)
	}
	h, err := st.GetMessage(ctx, store.MessageKey{Account: acct.ID, Mailbox: mailbox, UIDValidity: uidValidity, UID: uid})
	if err != nil {
		return nil, fmt.Errorf("message UID %d is not in the cache at %s; run mailsync first: %w", uid, mailbox, err)
	}
	return &probe{ctx: ctx, st: st, account: acct.ID, mailbox: mailbox, uid: uid, mid: h.InternalID}, nil
}

type recipe struct {
	name     string
	expected string
	run      func() (observed string, skipped bool, err error)
}

func (p *probe) recipes(destructive bool) []recipe {
	out := []recipe{
		{"mark read", "\\Seen changes, then the original state", p.toggleSeen},
		{"star", "\\Flagged changes, then the original state", p.toggleFlagged},
		{"add/remove label", "label placement appears, then disappears", p.toggleLabel},
		{"archive + undo", "moves out of the folder, undo restores it", p.archiveUndo},
	}
	if destructive {
		out = append(out, recipe{"trash + undo", "moves to Trash, undo restores it", p.trashUndo})
	}
	return out
}

func (p *probe) flags() ([]string, error) {
	fl, err := p.st.FlagsOfMessages(p.ctx, p.account, []string{p.mid})
	return fl[p.mid], err
}

func (p *probe) mailboxes() (map[string]uint32, error) {
	mm, err := p.st.MembershipOfMessages(p.ctx, p.account, []string{p.mid})
	if err != nil {
		return nil, err
	}
	out := map[string]uint32{}
	for _, pl := range mm[p.mid] {
		out[pl.Mailbox] = pl.UID
	}
	return out, nil
}

func has(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

func (p *probe) apply(a actions.Action) error {
	if p.ex == nil {
		return errors.New("probe is not in write mode")
	}
	res, err := p.ex.Apply(p.ctx, a)
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("%s: %s", res.Failed[0].Mailbox, res.Failed[0].Message)
	}
	return nil
}

// undoNewest undoes the newest audit entry, which is the action just applied.
func (p *probe) undoNewest() error {
	entries, err := p.ex.Recent(p.ctx, 1)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("no audit entry to undo")
	}
	res, err := p.ex.Undo(p.ctx, entries[0].ID)
	if err != nil {
		return err
	}
	if len(res.Failed) > 0 {
		return fmt.Errorf("%s: %s", res.Failed[0].Mailbox, res.Failed[0].Message)
	}
	return nil
}

func (p *probe) toggleSeen() (string, bool, error) {
	before, err := p.flags()
	if err != nil {
		return "", false, err
	}
	if has(before, `\Seen`) {
		if err := p.apply(actions.Action{Type: actions.MarkUnread, MessageIDs: []string{p.mid}}); err != nil {
			return "", false, err
		}
		after, err := p.flags()
		if err != nil {
			return "", false, err
		}
		if has(after, `\Seen`) {
			return "still seen", false, errors.New("mark_unread did not clear \\Seen")
		}
		if err := p.apply(actions.Action{Type: actions.MarkRead, MessageIDs: []string{p.mid}}); err != nil {
			return "unseen, restore failed", false, err
		}
		return "unseen, restored seen", false, nil
	}
	if err := p.apply(actions.Action{Type: actions.MarkRead, MessageIDs: []string{p.mid}}); err != nil {
		return "", false, err
	}
	after, err := p.flags()
	if err != nil {
		return "", false, err
	}
	if !has(after, `\Seen`) {
		return "still unseen", false, errors.New("mark_read did not set \\Seen")
	}
	if err := p.apply(actions.Action{Type: actions.MarkUnread, MessageIDs: []string{p.mid}}); err != nil {
		return "seen, restore failed", false, err
	}
	return "seen, restored unseen", false, nil
}

func (p *probe) toggleFlagged() (string, bool, error) {
	before, err := p.flags()
	if err != nil {
		return "", false, err
	}
	typ, back := actions.Star, actions.Unstar
	label := "starred, restored unstarred"
	if has(before, `\Flagged`) {
		typ, back = actions.Unstar, actions.Star
		label = "unstarred, restored starred"
	}
	if err := p.apply(actions.Action{Type: typ, MessageIDs: []string{p.mid}}); err != nil {
		return "", false, err
	}
	after, err := p.flags()
	if err != nil {
		return "", false, err
	}
	want := typ == actions.Star
	if has(after, `\Flagged`) != want {
		return "", false, fmt.Errorf("%s did not change \\Flagged", typ)
	}
	if err := p.apply(actions.Action{Type: back, MessageIDs: []string{p.mid}}); err != nil {
		return label + ", restore failed", false, err
	}
	return label, false, nil
}

func (p *probe) toggleLabel() (string, bool, error) {
	states, err := p.st.GetSyncState(p.ctx, p.account)
	if err != nil {
		return "", false, err
	}
	full := "Labels/" + p.label
	exists := false
	for _, s := range states {
		if s.Name == full && s.Kind == model.KindLabel {
			exists = true
			break
		}
	}
	if !exists {
		return "label " + full + " is not in the cache; create it in Proton and sync", true, nil
	}
	if err := p.apply(actions.Action{Type: actions.AddLabel, Params: actions.Params{Label: p.label}, MessageIDs: []string{p.mid}}); err != nil {
		return "", false, err
	}
	boxes, err := p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes[full]; !ok {
		return "", false, fmt.Errorf("add_label did not add %s", full)
	}
	if err := p.apply(actions.Action{Type: actions.RemoveLabel, Params: actions.Params{Label: p.label}, MessageIDs: []string{p.mid}}); err != nil {
		return "label added, remove failed", false, err
	}
	boxes, err = p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes[full]; ok {
		return "", false, fmt.Errorf("remove_label left %s", full)
	}
	return "label added, then removed", false, nil
}

// folderSource returns the non-label placement the message can be moved from.
func (p *probe) folderSource() (string, bool, error) {
	boxes, err := p.mailboxes()
	if err != nil {
		return "", false, err
	}
	states, err := p.st.GetSyncState(p.ctx, p.account)
	if err != nil {
		return "", false, err
	}
	kinds := map[string]model.MailboxKind{}
	for _, s := range states {
		kinds[s.Name] = s.Kind
	}
	for _, name := range []string{"INBOX", "Sent", "Archive", "Trash", "Spam"} {
		if _, ok := boxes[name]; ok {
			return name, true, nil
		}
	}
	for name := range boxes {
		if kinds[name] == model.KindFolder && name != "All Mail" {
			return name, true, nil
		}
	}
	return "", false, nil
}

func (p *probe) archiveUndo() (string, bool, error) {
	source, ok, err := p.folderSource()
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "message has no folder placement (All Mail only); undo is not possible", true, nil
	}
	if source == "Archive" {
		return "message is already in Archive; skipped", true, nil
	}
	if err := p.apply(actions.Action{Type: actions.Archive, MessageIDs: []string{p.mid}}); err != nil {
		return "", false, err
	}
	boxes, err := p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes["Archive"]; !ok {
		return "", false, errors.New("archive did not add an Archive placement")
	}
	if err := p.undoNewest(); err != nil {
		return "archived, undo failed", false, err
	}
	boxes, err = p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes[source]; !ok {
		return "", false, fmt.Errorf("undo did not restore %s", source)
	}
	return "archived, undo restored " + source, false, nil
}

func (p *probe) trashUndo() (string, bool, error) {
	source, ok, err := p.folderSource()
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "message has no folder placement (All Mail only); trash + undo skipped", true, nil
	}
	if source == "Trash" {
		return "message is already in Trash; skipped", true, nil
	}
	if err := p.apply(actions.Action{Type: actions.Trash, MessageIDs: []string{p.mid}}); err != nil {
		return "", false, err
	}
	boxes, err := p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes["Trash"]; !ok {
		return "", false, errors.New("trash did not add a Trash placement")
	}
	if err := p.undoNewest(); err != nil {
		return "trashed, undo failed", false, err
	}
	boxes, err = p.mailboxes()
	if err != nil {
		return "", false, err
	}
	if _, ok := boxes[source]; !ok {
		return "", false, fmt.Errorf("undo did not restore %s", source)
	}
	return "trashed, undo restored " + source, false, nil
}
