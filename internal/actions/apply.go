package actions

import (
	"context"
	"encoding/json"
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

// preAudit inserts one verified = 0 row per planned message before its group is
// written. If the process dies or the call is cancelled mid-write, the row is
// the evidence that a write was attempted; finish updates it afterwards.
func (e *Executor) preAudit(ctx context.Context, a Action, batchID string, undoOf int64, items []*planItem) error {
	if len(items) == 0 {
		return nil
	}
	auditCtx := context.WithoutCancel(ctx)
	paramsJSON, err := json.Marshal(a.Params)
	if err != nil {
		return err
	}
	for _, item := range items {
		beforeJSON, _ := json.Marshal(stateOf(item.row))
		id, err := e.audit.Insert(auditCtx, Entry{
			TS:          e.opts.Now(),
			Account:     e.opts.Account,
			Type:        string(a.Type),
			ParamsJSON:  string(paramsJSON),
			InverseJSON: "{}",
			MID:         item.row.mid,
			BeforeJSON:  string(beforeJSON),
			AfterJSON:   "{}",
			Verified:    false,
			BatchID:     batchID,
			UndoOf:      undoOf,
		})
		if err != nil {
			return fmt.Errorf("write audit entry: %w", err)
		}
		item.auditID = id
	}
	return nil
}

// requireSynced refuses a write whose placement or target has never completed a
// sync: a UID (or epoch) from such a mailbox is a guess.
func requireSynced(box store.MailboxState, what string) error {
	if box.LastSync.IsZero() {
		return fmt.Errorf("the %s has not been synced yet; sync first and try again", what)
	}
	return nil
}

// checkIdentity reads the Proton internal id of every source UID and keeps only
// the items whose id matches the cache. A synthetic mid has no identity header,
// so it is kept and verified through COPYUID instead. A read failure closes the
// connection, because it may be in an unknown state.
func (e *Executor) checkIdentity(ctx context.Context, mailbox string, epoch uint32, items []*planItem) ([]*planItem, map[*planItem]error, error) {
	failed := make(map[*planItem]error)
	var uids []uint32
	canonical := make(map[uint32]*planItem)
	for _, item := range items {
		if !item.source.Canonical {
			continue
		}
		uids = append(uids, item.source.UID)
		canonical[item.source.UID] = item
	}
	if len(uids) == 0 {
		return items, failed, nil
	}
	ids, err := e.w.FetchInternalIDs(ctx, mailbox, epoch, uids)
	if err != nil {
		e.drop()
		return nil, failed, fmt.Errorf("verify the message identity in %s: %w", mailbox, err)
	}
	keep := make([]*planItem, 0, len(items))
	for _, item := range items {
		if !item.source.Canonical {
			keep = append(keep, item)
			continue
		}
		got, ok := ids[item.source.UID]
		if !ok {
			failed[item] = fmt.Errorf("UID %d is no longer in %s", item.source.UID, mailbox)
			continue
		}
		if got != item.row.mid {
			failed[item] = fmt.Errorf("the cached UID no longer names this message in %s; nothing was written", mailbox)
			continue
		}
		keep = append(keep, item)
	}
	return keep, failed, nil
}

// locateDest finds the destination UID of one item after a COPY or MOVE. A
// canonical message must be found by exactly one X-Pm-Internal-Id hit; a
// synthetic mid falls back to the COPYUID pairing, because a header search
// cannot find it. A multi-hit answer is an error, not an index guess.
func (e *Executor) locateDest(ctx context.Context, target string, epoch uint32, item *planItem, data mail.CopyUID) (uint32, error) {
	if !item.source.Canonical {
		uid := destUIDFor(data, item.source.UID)
		if uid == 0 {
			return 0, errors.New("no COPYUID for the message")
		}
		return uid, nil
	}
	uids, err := e.w.SearchInternalID(ctx, target, epoch, item.row.mid)
	if err != nil {
		e.drop()
		return 0, fmt.Errorf("verify the message in %s: %w", target, err)
	}
	switch len(uids) {
	case 1:
		return uids[0], nil
	case 0:
		return 0, errors.New("the message was not found in the target after the copy")
	default:
		return 0, fmt.Errorf("the message matches %d copies in the target; the placement was left unknown", len(uids))
	}
}

// applyFlags handles mark_read/mark_unread/star/unstar. Proton keeps \Seen and
// \Flagged global per message, so one STORE covers every placement. The write
// runs in All Mail when the message is there, otherwise in one placement. A
// message already in the requested state is a no-op with no inverse.
func (e *Executor) applyFlags(ctx context.Context, a Action, ids []string, rows map[string]*rowState, batchID string, undoOf int64) (map[string]*outcome, error) {
	add, remove := flagChange(a.Type)
	out := make(map[string]*outcome, len(ids))

	type group struct {
		mailbox     string
		uidValidity uint32
		items       []*planItem
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
		if flagStateMatches(r.flags, add, remove) {
			// Already in the desired state: no write, and nothing to undo.
			out[id] = &outcome{mid: id, mailbox: pl.Mailbox, verified: true, after: stateOf(r)}
			continue
		}
		key := fmt.Sprintf("%s\x00%d", pl.Mailbox, pl.UIDValidity)
		g := groups[key]
		if g == nil {
			g = &group{mailbox: pl.Mailbox, uidValidity: pl.UIDValidity}
			groups[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, &planItem{row: r, source: pl})
	}
	sort.Strings(order)

	var stopErr error
	for _, key := range order {
		g := groups[key]
		if err := e.connect(ctx); err != nil {
			stopErr = err
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: err, after: stateOf(item.row)}
			}
			break
		}
		if err := e.preAudit(ctx, a, batchID, undoOf, g.items); err != nil {
			return out, err
		}
		uids := sourceUIDs(g.items)
		err := e.w.StoreFlags(ctx, g.mailbox, g.uidValidity, uids, add, remove)
		if err != nil {
			e.noteWriteErr(err)
			for _, item := range g.items {
				o := &outcome{mid: item.row.mid, mailbox: g.mailbox, err: err, after: stateOf(item.row), auditID: item.auditID}
				if isEpochChanged(err) {
					o.resync = []string{g.mailbox}
				}
				out[item.row.mid] = o
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		actual, ferr := e.w.FetchFlags(ctx, g.mailbox, g.uidValidity, uids)
		if ferr != nil {
			e.drop()
		}
		for _, item := range g.items {
			if ferr != nil {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("read flags back from %s: %w", g.mailbox, ferr), after: stateOf(item.row), auditID: item.auditID, resync: []string{g.mailbox}}
				continue
			}
			got, present := actual[item.source.UID]
			if !present {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("UID %d is no longer in %s", item.source.UID, g.mailbox), after: stateOf(item.row), auditID: item.auditID}
				continue
			}
			if !flagsApplied(got, add, remove) {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.mailbox, err: fmt.Errorf("flags in %s do not show the change", g.mailbox), after: stateOf(item.row), auditID: item.auditID}
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
				auditID:  item.auditID,
			}
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			e.drop()
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyMove handles archive/move/trash/spam/not_spam. One UID MOVE runs per
// source mailbox. From All Mail, or when Bridge declines MOVE to Archive, a COPY
// is used and the source is left in place; that is reported as a partial
// failure, never as full success.
func (e *Executor) applyMove(ctx context.Context, a Action, ids []string, rows map[string]*rowState, boxes map[string]store.MailboxState, target string, batchID string, undoOf int64) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))
	targetBox := boxes[target]
	if err := requireSynced(targetBox, "target folder "+target); err != nil {
		for _, id := range ids {
			out[id] = &outcome{mid: id, mailbox: target, err: err, after: stateOf(rows[id])}
		}
		return out, nil
	}

	type group struct {
		source   store.Placement
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
		if err := requireSynced(boxes[src.Mailbox], "folder "+src.Mailbox); err != nil {
			out[id] = &outcome{mid: id, mailbox: src.Mailbox, err: err, after: stateOf(r)}
			continue
		}
		key := fmt.Sprintf("%t\x00%s\x00%d", copyOnly, src.Mailbox, src.UIDValidity)
		g := groups[key]
		if g == nil {
			g = &group{source: src, copyOnly: copyOnly}
			groups[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, &planItem{row: r, source: src})
	}
	sort.Strings(order)

	var stopErr error
	for _, key := range order {
		g := groups[key]
		if err := e.connect(ctx); err != nil {
			stopErr = err
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: err, after: stateOf(item.row)}
			}
			break
		}
		keep, mismatched, ierr := e.checkIdentity(ctx, g.source.Mailbox, g.source.UIDValidity, g.items)
		for item, err := range mismatched {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: err, after: stateOf(item.row)}
		}
		if ierr != nil {
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: ierr, after: stateOf(item.row), resync: []string{g.source.Mailbox}}
			}
			continue
		}
		if len(keep) == 0 {
			continue
		}
		g.items = keep
		if err := e.preAudit(ctx, a, batchID, undoOf, g.items); err != nil {
			return out, err
		}
		uids := sourceUIDs(g.items)
		var data mail.CopyUID
		var err error
		copyOnly := g.copyOnly
		if copyOnly {
			data, err = e.w.Copy(ctx, g.source.Mailbox, g.source.UIDValidity, uids, target)
		} else {
			data, err = e.w.Move(ctx, g.source.Mailbox, g.source.UIDValidity, uids, target)
			if err != nil && a.Type == Archive && isNo(err) {
				// Bridge's Archive behaviour is unverified: a NO answer falls
				// back to COPY, which leaves the source placement in place.
				data, err = e.w.Copy(ctx, g.source.Mailbox, g.source.UIDValidity, uids, target)
				if err == nil {
					copyOnly = true
				}
			}
		}
		if err != nil {
			e.noteWriteErr(err)
			for _, item := range g.items {
				o := &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: err, after: stateOf(item.row), auditID: item.auditID}
				if isEpochChanged(err) {
					o.resync = []string{g.source.Mailbox}
				}
				out[item.row.mid] = o
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		if data.UIDValidity != 0 && targetBox.UIDValidity != 0 && data.UIDValidity != targetBox.UIDValidity {
			// The server copied into a different epoch than the cache knows.
			// Writing a placement from this answer would be wrong.
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("unknown outcome: %s changed UIDVALIDITY; re-syncing", target), after: stateOf(item.row), auditID: item.auditID, resync: []string{target}}
			}
			continue
		}

		// Locate the copy in the target first: a search error drops the
		// connection, so the source recheck below must not depend on it.
		dest := make(map[*planItem]uint32, len(g.items))
		destErr := make(map[*planItem]error, len(g.items))
		for _, item := range g.items {
			uid, derr := e.locateDest(ctx, target, targetBox.UIDValidity, item, data)
			if derr != nil {
				destErr[item] = derr
				continue
			}
			dest[item] = uid
		}
		var goneErr map[*planItem]error
		if !copyOnly && e.connected {
			goneErr = e.verifySourceGone(ctx, g.source.Mailbox, g.source.UIDValidity, g.items)
		} else if !copyOnly {
			goneErr = make(map[*planItem]error, len(g.items))
			for _, item := range g.items {
				goneErr[item] = errors.New("the connection closed before the source could be checked")
			}
		}
		for _, item := range g.items {
			o := &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, after: stateOf(item.row), auditID: item.auditID}
			if derr := destErr[item]; derr != nil {
				o.err = derr
				o.auditErr = fmt.Errorf("unknown outcome: %w", derr)
				o.resync = []string{target, g.source.Mailbox}
				o.mailbox = target
				out[item.row.mid] = o
				continue
			}
			destUID := dest[item]
			if goneErr != nil && goneErr[item] != nil {
				// The copy exists, so keep the target placement; the source is
				// still there, so this is a partial write and needs a resync.
				after := stateOf(item.row)
				after.Mailboxes = replaceName(after.Mailboxes, "", target)
				o.verified = true
				o.err = fmt.Errorf("%v; the original is still in %s — try again after the folders finish syncing", goneErr[item], g.source.Mailbox)
				o.auditErr = fmt.Errorf("unknown outcome: %w", o.err)
				o.update = store.ActionUpdate{MID: item.row.mid, AddMailbox: target, AddUID: destUID, AddUIDValidity: targetBox.UIDValidity}
				o.after = after
				o.resync = []string{g.source.Mailbox, target}
				o.mailbox = g.source.Mailbox
				out[item.row.mid] = o
				continue
			}
			item.destUID = destUID
			after := stateOf(item.row)
			inverse := Inverse{}
			update := store.ActionUpdate{MID: item.row.mid}
			if copyOnly {
				// The copy is confirmed, the source stays: partial success.
				after.Mailboxes = replaceName(after.Mailboxes, "", target)
				update.AddMailbox = target
				update.AddUID = destUID
				update.AddUIDValidity = targetBox.UIDValidity
				o.verified = true
				o.err = errors.New(partialCopyText(a, g.source.Mailbox))
				o.auditErr = fmt.Errorf("unknown outcome: %w", o.err)
				o.resync = []string{g.source.Mailbox}
				o.mailbox = g.source.Mailbox
			} else {
				after.Mailboxes = replaceName(after.Mailboxes, g.source.Mailbox, target)
				update.RemoveMailbox = g.source.Mailbox
				update.AddMailbox = target
				update.AddUID = destUID
				update.AddUIDValidity = targetBox.UIDValidity
				inverse = Inverse{Type: string(Move), Params: Params{Folder: g.source.Mailbox}}
				o.verified = true
				o.mailbox = g.source.Mailbox
			}
			o.update = update
			o.after = after
			o.inverse = inverse
			out[item.row.mid] = o
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			e.drop()
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyAddLabel copies messages into Labels/<label>. A COPY only adds a
// placement; nothing is removed, and the inverse is remove_label.
func (e *Executor) applyAddLabel(ctx context.Context, a Action, ids []string, rows map[string]*rowState, boxes map[string]store.MailboxState, target string, batchID string, undoOf int64) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))
	type group struct {
		source store.Placement
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
		if err := requireSynced(boxes[src.Mailbox], "folder "+src.Mailbox); err != nil {
			out[id] = &outcome{mid: id, mailbox: src.Mailbox, err: err, after: stateOf(r)}
			continue
		}
		key := fmt.Sprintf("%s\x00%d", src.Mailbox, src.UIDValidity)
		g := groups[key]
		if g == nil {
			g = &group{source: src}
			groups[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, &planItem{row: r, source: src})
	}
	sort.Strings(order)

	targetBox := boxes[target]
	var stopErr error
	for _, key := range order {
		g := groups[key]
		if err := e.connect(ctx); err != nil {
			stopErr = err
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: err, after: stateOf(item.row)}
			}
			break
		}
		if err := e.preAudit(ctx, a, batchID, undoOf, g.items); err != nil {
			return out, err
		}
		uids := sourceUIDs(g.items)
		data, err := e.w.Copy(ctx, g.source.Mailbox, g.source.UIDValidity, uids, target)
		if err != nil {
			e.noteWriteErr(err)
			for _, item := range g.items {
				o := &outcome{mid: item.row.mid, mailbox: g.source.Mailbox, err: err, after: stateOf(item.row), auditID: item.auditID}
				if isEpochChanged(err) {
					o.resync = []string{g.source.Mailbox}
				}
				out[item.row.mid] = o
			}
			if errors.Is(err, mail.ErrAuthFailed) {
				stopErr = err
				break
			}
			continue
		}
		if data.UIDValidity != 0 && targetBox.UIDValidity != 0 && data.UIDValidity != targetBox.UIDValidity {
			for _, item := range g.items {
				out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: fmt.Errorf("unknown outcome: %s changed UIDVALIDITY; re-syncing", target), after: stateOf(item.row), auditID: item.auditID, resync: []string{target}}
			}
			continue
		}
		for _, item := range g.items {
			destUID, derr := e.locateDest(ctx, target, targetBox.UIDValidity, item, data)
			o := &outcome{mid: item.row.mid, mailbox: target, after: stateOf(item.row), auditID: item.auditID}
			if derr != nil {
				o.err = derr
				o.auditErr = fmt.Errorf("unknown outcome: %w", derr)
				o.resync = []string{target}
				out[item.row.mid] = o
				continue
			}
			after := stateOf(item.row)
			after.Mailboxes = replaceName(after.Mailboxes, "", target)
			o.verified = true
			o.update = store.ActionUpdate{MID: item.row.mid, AddMailbox: target, AddUID: destUID, AddUIDValidity: targetBox.UIDValidity}
			o.after = after
			o.inverse = Inverse{Type: string(RemoveLabel), Params: a.Params}
			out[item.row.mid] = o
		}
		if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
			e.drop()
			stopErr = err
			break
		}
	}
	return out, stopErr
}

// applyRemoveLabel removes a message from one label with STORE +Deleted and UID
// EXPUNGE inside that label mailbox. This is the only place actions expunges.
// The placement's identity is checked first, so a stale UID cannot remove the
// wrong message.
func (e *Executor) applyRemoveLabel(ctx context.Context, a Action, ids []string, rows map[string]*rowState, boxes map[string]store.MailboxState, target string, batchID string, undoOf int64) (map[string]*outcome, error) {
	out := make(map[string]*outcome, len(ids))
	var items []*planItem
	for _, id := range ids {
		r := rows[id]
		pl, ok := placementIn(r.placements, target)
		if !ok {
			out[id] = &outcome{mid: id, mailbox: target, verified: true, after: stateOf(r)}
			continue
		}
		items = append(items, &planItem{row: r, source: pl})
	}
	if len(items) == 0 {
		return out, nil
	}
	if err := e.connect(ctx); err != nil {
		for _, item := range items {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: err, after: stateOf(item.row)}
		}
		return out, err
	}
	epoch := boxes[target].UIDValidity
	keep, mismatched, ierr := e.checkIdentity(ctx, target, epoch, items)
	for item, err := range mismatched {
		out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: err, after: stateOf(item.row)}
	}
	if ierr != nil {
		for _, item := range items {
			out[item.row.mid] = &outcome{mid: item.row.mid, mailbox: target, err: ierr, after: stateOf(item.row), resync: []string{target}}
		}
		return out, nil
	}
	if len(keep) == 0 {
		return out, nil
	}
	items = keep
	if err := e.preAudit(ctx, a, batchID, undoOf, items); err != nil {
		return out, err
	}
	uids := sourceUIDs(items)
	if err := e.w.ExpungeDeleted(ctx, target, epoch, uids); err != nil {
		e.noteWriteErr(err)
		for _, item := range items {
			o := &outcome{mid: item.row.mid, mailbox: target, err: err, after: stateOf(item.row), auditID: item.auditID}
			if isEpochChanged(err) {
				o.resync = []string{target}
			}
			out[item.row.mid] = o
		}
		if errors.Is(err, mail.ErrAuthFailed) {
			return out, err
		}
		return out, nil
	}
	remaining, ferr := e.w.FetchFlags(ctx, target, epoch, uids)
	if ferr != nil {
		e.drop()
	}
	dead := ferr != nil
	for _, item := range items {
		o := &outcome{mid: item.row.mid, mailbox: target, after: stateOf(item.row), auditID: item.auditID}
		_, still := remaining[item.source.UID]
		switch {
		case ferr != nil:
			o.err = fmt.Errorf("read %s back: %w", target, ferr)
			o.auditErr = fmt.Errorf("unknown outcome: %w", o.err)
			o.resync = []string{target}
			out[item.row.mid] = o
			continue
		case still:
			o.err = fmt.Errorf("UID %d is still in %s", item.source.UID, target)
			o.resync = []string{target}
			out[item.row.mid] = o
			continue
		}
		if dead {
			o.err = errors.New("the connection closed before the label removal could be checked")
			o.auditErr = fmt.Errorf("unknown outcome: %w", o.err)
			o.resync = []string{target}
			out[item.row.mid] = o
			continue
		}
		found, serr := e.w.SearchInternalID(ctx, target, epoch, item.row.mid)
		if serr != nil {
			e.drop()
			dead = true
			o.err = fmt.Errorf("verify %s: %w", target, serr)
			o.auditErr = fmt.Errorf("unknown outcome: %w", o.err)
			o.resync = []string{target}
			out[item.row.mid] = o
			continue
		}
		if len(found) > 0 {
			o.err = fmt.Errorf("the label placement is still present in %s", target)
			o.resync = []string{target}
			out[item.row.mid] = o
			continue
		}
		after := stateOf(item.row)
		after.Mailboxes = replaceName(after.Mailboxes, target, "")
		o.verified = true
		o.update = store.ActionUpdate{MID: item.row.mid, RemoveMailbox: target}
		o.after = after
		o.inverse = Inverse{Type: string(AddLabel), Params: a.Params}
		out[item.row.mid] = o
	}
	if err := e.sleep(ctx, e.opts.ChunkDelay); err != nil {
		e.drop()
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

// flagStateMatches reports whether the cached flags already satisfy add/remove.
func flagStateMatches(before, add, remove []string) bool {
	return flagsApplied(before, add, remove)
}

func sourceUIDs(items []*planItem) []uint32 {
	out := make([]uint32, 0, len(items))
	for _, item := range items {
		out = append(out, item.source.UID)
	}
	return out
}
