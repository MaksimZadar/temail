package actions

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/store"
)

const (
	flagSeen    = `\Seen`
	flagFlagged = `\Flagged`
)

// applyFlags handles mark_read/mark_unread/star/unstar. Proton keeps \Seen and
// \Flagged global per message, so one STORE covers every placement. The write
// runs in All Mail when the message is there, otherwise in one placement.
func (e *Executor) applyFlags(ctx context.Context, a Action, ids []string, rows map[string]*rowState) (map[string]*outcome, error) {
	add, remove := flagChange(a.Type)
	out := make(map[string]*outcome, len(ids))

	type group struct {
		mailbox string
		items   []*planItem
	}
	groups := map[string]*group{}
	var order []string
	for _, id := range ids {
		r := rows[id]
		pl, ok := flagsMailbox(r.placements)
		if !ok {
			out[id] = &outcome{mid: id, err: errors.New("message has no cached placement"), after: stateOf(r)}
			continue
		}
		g := groups[pl.Mailbox]
		if g == nil {
			g = &group{mailbox: pl.Mailbox}
			groups[pl.Mailbox] = g
			order = append(order, pl.Mailbox)
		}
		item := &planItem{row: r, sourceUID: pl.UID}
		g.items = append(g.items, item)
	}
	sort.Strings(order)

	var stopErr error
	for _, mailbox := range order {
		g := groups[mailbox]
		uids := sourceUIDs(g.items)
		err := e.w.StoreFlags(ctx, g.mailbox, uids, add, remove)
		if err != nil {
			e.writeErr(err)
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: err, after: stateOf(item.row)}
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		actual, ferr := e.w.FetchFlags(ctx, g.mailbox, uids)
		for _, item := range g.items {
			if ferr != nil {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("read flags back from %s: %w", g.mailbox, ferr)}
				continue
			}
			got, present := actual[item.sourceUID]
			if !present {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("UID %d is no longer in %s", item.sourceUID, g.mailbox)}
				continue
			}
			if !flagsApplied(got, add, remove) {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("flags in %s do not show the change", g.mailbox)}
				continue
			}
			after := stateOf(item.row)
			after.Flags = normalizeFlags(got)
			out[item.row.mid] = &outcome{
				mid:      item.row.mid,
				mailbox:  g.mailbox,
				verified: true,
				update:   store.ActionUpdate{MID: item.row.mid, Flags: after.Flags},
				after:    after,
				inverse:  Inverse{Type: string(inverseFlagType(a.Type))},
			}
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyMove handles archive/move/trash/spam/not_spam. One UID MOVE runs per
// source mailbox. From All Mail, or when Bridge declines MOVE to Archive, a COPY
// is used and the source is left in place.
func (e *Executor) applyMove(ctx context.Context, a Action, ids []string, rows map[string]*rowState, target string) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))

	type group struct {
		source   string
		copyOnly bool
		items    []*planItem
	}
	groups := map[string]*group{}
	var order []string
	for _, id := range ids {
		r := rows[id]
		if _, ok := placementIn(r.placements, target); ok {
			// Already there: nothing to write, and nothing to undo.
			out[id] = &outcome{mid: id, mailbox: target, verified: true, after: stateOf(r)}
			continue
		}
		src, ok := sourceFolder(r.placements, target)
		copyOnly := false
		if !ok {
			if am, found := allMailPlacement(r.placements); found {
				src, copyOnly = am, true
			} else {
				out[id] = &outcome{mid: id, mailbox: target, err: errors.New("message has no folder placement to move from"), after: stateOf(r)}
				continue
			}
		}
		key := fmt.Sprintf("%t\x00%s", copyOnly, src.Mailbox)
		g := groups[key]
		if g == nil {
			g = &group{source: src.Mailbox, copyOnly: copyOnly}
			groups[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, &planItem{row: r, sourceUID: src.UID})
	}
	sort.Strings(order)

	var stopErr error
	for _, key := range order {
		g := groups[key]
		uids := sourceUIDs(g.items)
		var data mail.CopyUID
		var err error
		if g.copyOnly {
			data, err = e.w.Copy(ctx, g.source, uids, target)
		} else {
			data, err = e.w.Move(ctx, g.source, uids, target)
			if err != nil && a.Type == Archive && isNo(err) {
				// Bridge's Archive behaviour is unverified: a NO answer falls back
				// to COPY, which leaves the source placement in place.
				data, err = e.w.Copy(ctx, g.source, uids, target)
				if err == nil {
					g.copyOnly = true
				}
			}
		}
		if err != nil {
			e.writeErr(err)
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source, err: err, after: stateOf(item.row)}
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		for _, item := range g.items {
			item.destUID = destUIDFor(data, item.sourceUID)
		}
		verr := e.verifyPresent(ctx, target, g.items)
		var goneErr map[*planItem]error
		if !g.copyOnly {
			goneErr = e.verifySourceGone(ctx, g.source, g.items)
		}
		for _, item := range g.items {
			if item.destUID == 0 {
				item.destUID = lookupDestUID(item, g.items, data)
			}
			verrText := verr[item]
			if verrText == nil {
				verrText = goneErr[item]
			}
			if verrText != nil {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source, err: verrText, after: stateOf(item.row)}
				continue
			}
			after := stateOf(item.row)
			removeFrom := g.source
			if g.copyOnly {
				removeFrom = ""
			}
			after.Mailboxes = replaceName(after.Mailboxes, removeFrom, target)
			inv := Inverse{}
			if !g.copyOnly && g.source != target {
				inv = Inverse{Type: string(Move), Params: Params{Folder: g.source}}
			}
			out[item.row.mid] = &outcome{
				mid:      item.row.mid,
				mailbox:  g.source,
				verified: true,
				update: store.ActionUpdate{
					MID:           item.row.mid,
					RemoveMailbox: removeFrom,
					AddMailbox:    target,
					AddUID:        item.destUID,
				},
				after:   after,
				inverse: inv,
			}
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyAddLabel copies messages into Labels/<label>. A COPY only adds a
// placement; nothing is removed, and the inverse is remove_label.
func (e *Executor) applyAddLabel(ctx context.Context, a Action, ids []string, rows map[string]*rowState, target string) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))
	type group struct {
		source string
		items  []*planItem
	}
	groups := map[string]*group{}
	var order []string
	for _, id := range ids {
		r := rows[id]
		if _, ok := placementIn(r.placements, target); ok {
			out[id] = &outcome{mid: id, mailbox: target, verified: true, after: stateOf(r)}
			continue
		}
		src, ok := copySource(r.placements)
		if !ok {
			out[id] = &outcome{mid: id, mailbox: target, err: errors.New("message has no cached placement"), after: stateOf(r)}
			continue
		}
		g := groups[src.Mailbox]
		if g == nil {
			g = &group{source: src.Mailbox}
			groups[src.Mailbox] = g
			order = append(order, src.Mailbox)
		}
		g.items = append(g.items, &planItem{row: r, sourceUID: src.UID})
	}
	sort.Strings(order)

	var stopErr error
	for _, source := range order {
		g := groups[source]
		uids := sourceUIDs(g.items)
		data, err := e.w.Copy(ctx, g.source, uids, target)
		if err != nil {
			e.writeErr(err)
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source, err: err, after: stateOf(item.row)}
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		for _, item := range g.items {
			item.destUID = destUIDFor(data, item.sourceUID)
		}
		verr := e.verifyPresent(ctx, target, g.items)
		for _, item := range g.items {
			if item.destUID == 0 {
				item.destUID = lookupDestUID(item, g.items, data)
			}
			if err := verr[item]; err != nil {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: err, after: stateOf(item.row)}
				continue
			}
			after := stateOf(item.row)
			after.Mailboxes = replaceName(after.Mailboxes, "", target)
			out[item.row.mid] = &outcome{
				mid:      item.row.mid,
				mailbox:  target,
				verified: true,
				update:   store.ActionUpdate{MID: item.row.mid, AddMailbox: target, AddUID: item.destUID},
				after:    after,
				inverse:  Inverse{Type: string(RemoveLabel), Params: a.Params},
			}
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyRemoveLabel removes a message from one label with STORE +Deleted and UID
// EXPUNGE inside that label mailbox. This is the only place actions expunges.
func (e *Executor) applyRemoveLabel(ctx context.Context, a Action, ids []string, rows map[string]*rowState, target string) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))
	var items []*planItem
	for _, id := range ids {
		r := rows[id]
		pl, ok := placementIn(r.placements, target)
		if !ok {
			out[id] = &outcome{mid: id, mailbox: target, verified: true, after: stateOf(r)}
			continue
		}
		items = append(items, &planItem{row: r, sourceUID: pl.UID})
	}
	if len(items) == 0 {
		return out, nil
	}
	uids := sourceUIDs(items)
	if err := e.w.ExpungeDeleted(ctx, target, uids); err != nil {
		e.writeErr(err)
		for _, item := range items {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: err, after: stateOf(item.row)}
		}
		if errors.Is(err, mail.ErrAuthFailed) {
			return out, err
		}
		return out, nil
	}
	remaining, ferr := e.w.FetchFlags(ctx, target, uids)
	for _, item := range items {
		if ferr != nil {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("read %s back: %w", target, ferr), after: stateOf(item.row)}
			continue
		}
		if _, still := remaining[item.sourceUID]; still {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("UID %d is still in %s", item.sourceUID, target), after: stateOf(item.row)}
			continue
		}
		if found, serr := e.w.SearchInternalID(ctx, target, item.row.mid); serr != nil {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("verify %s: %w", target, serr), after: stateOf(item.row)}
			continue
		} else if len(found) > 0 {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("the label placement is still present in %s", target), after: stateOf(item.row)}
			continue
		}
		after := stateOf(item.row)
		after.Mailboxes = replaceName(after.Mailboxes, target, "")
		out[item.row.mid] = &outcome{
			mid:      item.row.mid,
			mailbox:  target,
			verified: true,
			update:   store.ActionUpdate{MID: item.row.mid, RemoveMailbox: target},
			after:    after,
			inverse:  Inverse{Type: string(AddLabel), Params: a.Params},
		}
	}
	if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
		return out, err
	}
	return out, nil
}

func flagChange(t Type) (add, remove []string) {
	switch t {
	case MarkRead:
		return []string{flagSeen}, nil
	case MarkUnread:
		return nil, []string{flagSeen}
	case Star:
		return []string{flagFlagged}, nil
	case Unstar:
		return nil, []string{flagFlagged}
	}
	return nil, nil
}

func inverseFlagType(t Type) Type {
	switch t {
	case MarkRead:
		return MarkUnread
	case MarkUnread:
		return MarkRead
	case Star:
		return Unstar
	case Unstar:
		return Star
	}
	return ""
}

func flagsApplied(got, add, remove []string) bool {
	has := make(map[string]bool, len(got))
	for _, f := range got {
		has[f] = true
	}
	for _, f := range add {
		if !has[f] {
			return false
		}
	}
	for _, f := range remove {
		if has[f] {
			return false
		}
	}
	return true
}

func sourceUIDs(items []*planItem) []uint32 {
	out := make([]uint32, 0, len(items))
	for _, item := range items {
		out = append(out, item.sourceUID)
	}
	return out
}

// lookupDestUID is a last-resort pairing when a COPYUID answer had sets that do
// not line up one-to-one: the n-th source UID maps to the n-th destination UID.
func lookupDestUID(item *planItem, items []*planItem, data mail.CopyUID) uint32 {
	if len(data.SourceUIDs) == len(items) && len(data.DestUIDs) == len(items) {
		for i, it := range items {
			if it == item {
				return data.DestUIDs[i]
			}
		}
	}
	return 0
}
