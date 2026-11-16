package actions

import (
	"context"
	"fmt"
)

// verifySourceGone confirms that a MOVE removed the source UIDs. Bridge's
// removal is eventual, so the check waits VerifyDelay and retries up to
// VerifyRetries times. A message still in the source after that is a partial
// write, not a silent success. A fetch failure closes the connection, because
// the next group must reconnect in a known state.
func (e *Executor) verifySourceGone(ctx context.Context, mailbox string, epoch uint32, items []*planItem) map[*planItem]error {
	errs := make(map[*planItem]error)
	pending := make([]*planItem, len(items))
	copy(pending, items)
	var lastErr error
	for attempt := 0; attempt <= e.opts.VerifyRetries; attempt++ {
		if err := e.sleep(ctx, e.opts.VerifyDelay); err != nil {
			e.drop()
			for _, item := range pending {
				errs[item] = err
			}
			return errs
		}
		uids := make([]uint32, 0, len(pending))
		for _, item := range pending {
			uids = append(uids, item.source.UID)
		}
		flags, err := e.w.FetchFlags(ctx, mailbox, epoch, uids)
		if err != nil {
			e.drop()
			lastErr = err
			continue
		}
		var still []*planItem
		for _, item := range pending {
			if _, ok := flags[item.source.UID]; ok {
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
