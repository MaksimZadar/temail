package actions

import (
	"context"
	"fmt"
)

// verifyPresent confirms that each item got a placement in mailbox. A COPYUID
// destination UID is the server's own confirmation (UIDPLUS); when it is absent,
// the X-Pm-Internal-Id header search is the fallback. A synthetic mid (a message
// without a Proton id) can only be verified through COPYUID; if Bridge did not
// report one, the write is reported as unverified rather than assumed.
func (e *Executor) verifyPresent(ctx context.Context, mailbox string, items []*planItem) map[*planItem]error {
	errs := make(map[*planItem]error)
	for _, item := range items {
		if item.destUID != 0 {
			continue
		}
		uids, err := e.w.SearchInternalID(ctx, mailbox, item.row.mid)
		if err != nil {
			errs[item] = fmt.Errorf("verify the message in %s: %w", mailbox, err)
			continue
		}
		if len(uids) == 0 {
			errs[item] = fmt.Errorf("the message was not found in %s after the copy", mailbox)
			continue
		}
		item.destUID = uids[0]
	}
	return errs
}

// verifySourceGone confirms that a MOVE removed the source UIDs. Bridge's
// removal is eventual, so the check waits VerifyDelay and retries up to
// VerifyRetries times. A message still in the source after that is a failure,
// not a silent success.
func (e *Executor) verifySourceGone(ctx context.Context, mailbox string, items []*planItem) map[*planItem]error {
	errs := make(map[*planItem]error)
	pending := make([]*planItem, len(items))
	copy(pending, items)
	var lastErr error
	for attempt := 0; attempt <= e.opts.VerifyRetries; attempt++ {
		if err := e.sleep(ctx, e.opts.VerifyDelay); err != nil {
			for _, item := range pending {
				errs[item] = err
			}
			return errs
		}
		uids := make([]uint32, 0, len(pending))
		for _, item := range pending {
			uids = append(uids, item.sourceUID)
		}
		flags, err := e.w.FetchFlags(ctx, mailbox, uids)
		if err != nil {
			lastErr = err
			continue
		}
		var still []*planItem
		for _, item := range pending {
			if _, ok := flags[item.sourceUID]; ok {
				still = append(still, item)
			}
		}
		pending = still
		if len(pending) == 0 {
			return errs
		}
	}
	for _, item := range pending {
		if lastErr != nil {
			errs[item] = fmt.Errorf("the move could not be confirmed in %s: %w", mailbox, lastErr)
		} else {
			errs[item] = fmt.Errorf("the message is still in %s after the move", mailbox)
		}
	}
	return errs
}
