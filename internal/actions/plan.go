package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// rowState is one message as the cache knows it before the action.
type rowState struct {
	mid        string
	placements []store.Placement
	flags      []string
}

// planItem is one message's write plan inside a group.
type planItem struct {
	row       *rowState
	sourceUID uint32
	destUID   uint32
}

// outcome is one message's result after the write and its verification.
type outcome struct {
	mid      string
	mailbox  string
	verified bool
	err      error
	update   store.ActionUpdate
	after    cachedState
	inverse  Inverse
}

// cachedState is the metadata recorded in the audit log. It never contains
// subjects, bodies or other message content.
type cachedState struct {
	Mailboxes []string `json:"mailboxes"`
	Flags     []string `json:"flags"`
}

// execute is the single write path for actions and undo. undoOf is non-zero for
// an inverse action; it is stored as params.undoOf and lets a restore target a
// system folder.
func (e *Executor) execute(ctx context.Context, a Action, ids []string, undoOf int64) (Result, error) {
	boxes, err := e.st.GetSyncState(ctx, e.opts.Account)
	if err != nil {
		return Result{}, fmt.Errorf("read mailboxes from cache: %w", err)
	}
	boxIndex := make(map[string]store.MailboxState, len(boxes))
	for _, b := range boxes {
		boxIndex[b.Name] = b
	}
	membership, err := e.st.MembershipOfMessages(ctx, e.opts.Account, ids)
	if err != nil {
		return Result{}, fmt.Errorf("read placements from cache: %w", err)
	}
	flags, err := e.st.FlagsOfMessages(ctx, e.opts.Account, ids)
	if err != nil {
		return Result{}, fmt.Errorf("read flags from cache: %w", err)
	}

	rows := make(map[string]*rowState, len(ids))
	for _, id := range ids {
		pls := membership[id]
		rows[id] = &rowState{mid: id, placements: pls, flags: normalizeFlags(flags[id])}
	}

	// Resolve the destination before connecting, so a typo or a missing mailbox
	// fails without a login.
	var moveTo, labelTo string
	switch a.Type {
	case Archive, Move, Trash, Spam, NotSpam:
		moveTo, err = e.moveTarget(a, boxIndex)
		if err != nil {
			return Result{}, err
		}
	case AddLabel, RemoveLabel:
		labelTo, err = labelMailbox(a.Params.Label, boxIndex)
		if err != nil {
			return Result{}, err
		}
	}

	if err := e.connect(ctx); err != nil {
		return Result{}, err
	}

	outs := make(map[string]*outcome, len(ids))
	var stopErr error
	switch a.Type {
	case MarkRead, MarkUnread, Star, Unstar:
		outs, stopErr = e.applyFlags(ctx, a, ids, rows)
	case Archive, Move, Trash, Spam, NotSpam:
		outs, stopErr = e.applyMove(ctx, a, ids, rows, moveTo)
	case AddLabel:
		outs, stopErr = e.applyAddLabel(ctx, a, ids, rows, labelTo)
	case RemoveLabel:
		outs, stopErr = e.applyRemoveLabel(ctx, a, ids, rows, labelTo)
	default:
		return Result{}, fmt.Errorf("%w: unknown action %q", ErrInvalidAction, a.Type)
	}

	// Missing rows and anything a family did not plan are failures here, so
	// every input id has an outcome and, when verified, an audit row.
	for _, id := range ids {
		if _, ok := outs[id]; ok {
			continue
		}
		msg := "message is not in the local cache"
		if rows[id] != nil && len(rows[id].placements) > 0 {
			msg = "action did not produce an outcome"
		}
		outs[id] = &outcome{mid: id, err: errors.New(msg), after: stateOf(rows[id])}
	}

	res, finishErr := e.finish(ctx, a, ids, rows, outs)
	if finishErr != nil {
		return res, finishErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, ctxErr
	}
	return res, stopErr
}

// finish writes one audit row per message, then applies the cache effects of
// every verified message in one transaction. The audit write uses a context
// without cancellation, so a cancelled call still leaves evidence.
func (e *Executor) finish(ctx context.Context, a Action, ids []string, rows map[string]*rowState, outs map[string]*outcome) (Result, error) {
	auditCtx := context.WithoutCancel(ctx)
	paramsJSON, err := json.Marshal(a.Params)
	if err != nil {
		return Result{}, err
	}
	var updates []store.ActionUpdate
	res := Result{Applied: make([]string, 0, len(ids))}
	for _, id := range ids {
		o := outs[id]
		before := stateOf(rows[id])
		after := o.after
		if after.Mailboxes == nil {
			after = before
		}
		if o.err != nil {
			e.opts.Logf("actions: %s on %s failed: %v", a.Type, id, o.err)
		}
		verified := o.verified && o.err == nil
		inverseJSON, err := json.Marshal(o.inverse)
		if err != nil {
			return res, err
		}
		if o.inverse.Type == "" {
			inverseJSON = []byte("{}")
		}
		beforeJSON, _ := json.Marshal(before)
		afterJSON, _ := json.Marshal(after)
		if _, err := e.audit.Insert(auditCtx, Entry{
			TS:          e.opts.Now(),
			Account:     e.opts.Account,
			Type:        string(a.Type),
			ParamsJSON:  string(paramsJSON),
			InverseJSON: string(inverseJSON),
			MID:         id,
			BeforeJSON:  string(beforeJSON),
			AfterJSON:   string(afterJSON),
			Verified:    verified,
			Error:       errText(o.err),
		}); err != nil {
			return res, fmt.Errorf("write audit entry: %w", err)
		}
		if verified {
			res.Applied = append(res.Applied, id)
			if o.update.MID != "" {
				updates = append(updates, o.update)
			}
			continue
		}
		res.Failed = append(res.Failed, Failure{
			MID:     id,
			Mailbox: o.mailbox,
			Message: failureText(o),
		})
	}
	if err := e.st.ApplyActionUpdates(auditCtx, e.opts.Account, updates); err != nil {
		return res, fmt.Errorf("update cache after %s: %w", a.Type, err)
	}
	return res, nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func failureText(o *outcome) string {
	if o.err != nil {
		return o.err.Error()
	}
	if !o.verified {
		return "the write could not be verified"
	}
	return "not applied"
}

// stateOf returns the audit metadata of one message before an action.
func stateOf(r *rowState) cachedState {
	if r == nil {
		return cachedState{Mailboxes: []string{}, Flags: []string{}}
	}
	return cachedState{Mailboxes: mailboxesOf(r.placements), Flags: normalizeFlags(r.flags)}
}

func mailboxesOf(places []store.Placement) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(places))
	for _, p := range places {
		if !seen[p.Mailbox] {
			seen[p.Mailbox] = true
			out = append(out, p.Mailbox)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeFlags(flags []string) []string {
	out := store.NormalizeFlags(flags)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// flagsMailbox picks where a flag write runs: All Mail when present (one global
// flag set covers every placement), otherwise any non-label placement, or the
// only placement.
func flagsMailbox(places []store.Placement) (store.Placement, bool) {
	for _, p := range places {
		if p.Mailbox == "All Mail" {
			return p, true
		}
	}
	for _, p := range places {
		if p.Kind != model.KindLabel {
			return p, true
		}
	}
	if len(places) > 0 {
		return places[0], true
	}
	return store.Placement{}, false
}

// folderPriority orders the non-label placements a MOVE can start from.
var folderPriority = map[model.MailboxKind]int{
	model.KindInbox:   0,
	model.KindSent:    1,
	model.KindFolder:  2,
	model.KindArchive: 3,
	model.KindTrash:   4,
	model.KindSpam:    5,
	model.KindDrafts:  6,
}

// sourceFolder picks the system/folder placement a MOVE starts from. All Mail is
// excluded (Bridge refuses MOVE from it) and so is the target (a message already
// in the target is a no-op).
func sourceFolder(places []store.Placement, target string) (store.Placement, bool) {
	var candidates []store.Placement
	for _, p := range places {
		if p.Kind == model.KindLabel || p.Mailbox == "All Mail" || p.Mailbox == target {
			continue
		}
		if _, ok := folderPriority[p.Kind]; ok {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return store.Placement{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, pj := folderPriority[candidates[i].Kind], folderPriority[candidates[j].Kind]
		if pi != pj {
			return pi < pj
		}
		return candidates[i].Mailbox < candidates[j].Mailbox
	})
	return candidates[0], true
}

func allMailPlacement(places []store.Placement) (store.Placement, bool) {
	for _, p := range places {
		if p.Mailbox == "All Mail" {
			return p, true
		}
	}
	return store.Placement{}, false
}

func placementIn(places []store.Placement, mailbox string) (store.Placement, bool) {
	for _, p := range places {
		if p.Mailbox == mailbox {
			return p, true
		}
	}
	return store.Placement{}, false
}

// copySource picks the mailbox a COPY starts from: All Mail when present, else
// the first placement.
func copySource(places []store.Placement) (store.Placement, bool) {
	if p, ok := allMailPlacement(places); ok {
		return p, true
	}
	if len(places) > 0 {
		return places[0], true
	}
	return store.Placement{}, false
}

// moveTarget resolves the destination mailbox for a move-family action using
// the cached mailbox listing, never a hard-coded assumption beyond the system
// folder names.
func (e *Executor) moveTarget(a Action, boxes map[string]store.MailboxState) (string, error) {
	name := func(kind model.MailboxKind, fallback string) (string, error) {
		for _, b := range boxes {
			if b.Kind == kind {
				return b.Name, nil
			}
		}
		if _, ok := boxes[fallback]; ok {
			return fallback, nil
		}
		return "", fmt.Errorf("%w: the %s mailbox is not in the cache; sync first", ErrInvalidAction, fallback)
	}
	switch a.Type {
	case Archive:
		return name(model.KindArchive, "Archive")
	case Trash:
		return name(model.KindTrash, "Trash")
	case Spam:
		return name(model.KindSpam, "Spam")
	case NotSpam:
		return name(model.KindInbox, "INBOX")
	case Move:
		b, ok := boxes[a.Params.Folder]
		if !ok {
			return "", fmt.Errorf("%w: %s is not a known mailbox; sync first", ErrInvalidAction, a.Params.Folder)
		}
		if b.Kind == model.KindLabel {
			return "", fmt.Errorf("%w: use add_label/remove_label for labels", ErrInvalidAction)
		}
		if a.Params.UndoOf == 0 && b.Kind != model.KindFolder {
			return "", fmt.Errorf("%w: move target must be a folder (Folders/<name>)", ErrInvalidAction)
		}
		return b.Name, nil
	default:
		return "", fmt.Errorf("%w: %s is not a move action", ErrInvalidAction, a.Type)
	}
}

// labelMailbox resolves Labels/<name> and requires it to exist as a label.
func labelMailbox(label string, boxes map[string]store.MailboxState) (string, error) {
	full := "Labels/" + label
	b, ok := boxes[full]
	if !ok {
		return "", fmt.Errorf("%w: label %q is not in the cache; sync first", ErrInvalidAction, label)
	}
	if b.Kind != model.KindLabel {
		return "", fmt.Errorf("%w: %s is not a label", ErrInvalidAction, full)
	}
	return full, nil
}

func isNo(err error) bool {
	var ie *imap.Error
	return errors.As(err, &ie) && ie.Type == imap.StatusResponseTypeNo
}

// destUIDFor maps a source UID to the UID it got in the target.
func destUIDFor(data mail.CopyUID, source uint32) uint32 {
	if len(data.SourceUIDs) != len(data.DestUIDs) {
		return 0
	}
	for i, uid := range data.SourceUIDs {
		if uid == source {
			return data.DestUIDs[i]
		}
	}
	return 0
}

// replaceName returns list with from removed and to added, sorted.
func replaceName(list []string, from, to string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list)+1)
	for _, name := range list {
		if name == from || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if to != "" && !seen[to] {
		out = append(out, to)
	}
	sort.Strings(out)
	return out
}
