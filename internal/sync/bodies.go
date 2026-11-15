package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// syncBodies runs step 4. It reads the newest messages without a body from
// the cache in batches and fetches each one with a delay before it. A message
// that fails although Bridge is reachable is counted in BodiesFailed and
// retried later, up to store.MaxBodyAttempts times. A connection that cannot
// be restored aborts the run, and the next run resumes it.
func (s *Syncer) syncBodies(ctx context.Context, res *Result) error {
	counts, err := s.store.Counts(ctx, s.opts.Account)
	if err != nil {
		return fmt.Errorf("count cached messages: %w", err)
	}
	done := make(map[string]int, len(counts))
	total := make(map[string]int, len(counts))
	for _, c := range counts {
		done[c.Mailbox] = c.Bodies
		total[c.Mailbox] = c.Messages
	}
	s.emit(Progress{Account: s.opts.Account, State: StateBodies})

	fetchedAny := false
	for {
		keys, err := s.store.PendingBodies(ctx, s.opts.Account, s.opts.BodyBatchSize)
		if err != nil {
			return fmt.Errorf("list messages without body: %w", err)
		}
		if len(keys) == 0 {
			return nil
		}
		for _, key := range keys {
			if fetchedAny {
				if err := s.sleep(ctx, s.opts.BodyDelay); err != nil {
					return err
				}
			}
			fetchedAny = true

			var msg bodyResult
			err := s.call(ctx, "fetch body of "+key.Mailbox, func(ctx context.Context) error {
				m, err := s.client.FetchMessage(ctx, key.Mailbox, key.UID)
				if err != nil {
					return err
				}
				msg = bodyResult{uidValidity: m.UIDValidity, text: m.TextBody, html: m.HTMLBody, attachments: m.Attachments}
				return nil
			})
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				var ce *connError
				if errors.As(err, &ce) {
					return err
				}
				if merr := s.store.MarkBodyFailed(ctx, key); merr != nil && !errors.Is(merr, store.ErrNotFound) {
					return fmt.Errorf("record failed body fetch: %w", merr)
				}
				res.BodiesFailed++
				continue
			}
			if msg.uidValidity != key.UIDValidity {
				return fmt.Errorf("mailbox %s changed UIDVALIDITY during body sync; the next run will resync it", key.Mailbox)
			}
			if err := s.store.SetBody(ctx, key, store.Body{Text: msg.text, HTML: msg.html, Attachments: msg.attachments}); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue // header removed while syncing; reconcile handles it next run
				}
				return fmt.Errorf("cache body of UID %d in %s: %w", key.UID, key.Mailbox, err)
			}
			res.BodiesFetched++
			done[key.Mailbox]++
			s.emitMailbox(StateBodies, key.Mailbox, 0, 0, done[key.Mailbox], total[key.Mailbox])
		}
	}
}

// bodyResult carries one fetched message out of the retry closure.
type bodyResult struct {
	uidValidity uint32
	text        string
	html        string
	attachments []model.Attachment
}
