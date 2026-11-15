package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MaksimZadar/temail/internal/mail"
)

// maxBackoff caps the delay between reconnect attempts.
const maxBackoff = 30 * time.Second

// connError marks a failure to (re)connect to Bridge, as opposed to an error
// from a single mail operation. Body fetches use it to tell a dead Bridge
// (abort the run) from one bad message (skip it).
type connError struct{ err error }

func (e *connError) Error() string { return fmt.Sprintf("reconnect to Bridge: %v", e.err) }
func (e *connError) Unwrap() error { return e.err }

// permanent reports connection errors that retrying cannot fix: an untrusted
// or mismatched certificate needs user action, and a non-loopback host is a
// configuration error.
func permanent(err error) bool {
	return errors.Is(err, mail.ErrUntrustedCert) ||
		errors.Is(err, mail.ErrCertMismatch) ||
		errors.Is(err, mail.ErrNotLoopback)
}

// backoff returns the delay before reconnect attempt n (1-based): Backoff,
// doubled each time, capped at maxBackoff.
func (s *Syncer) backoff(n int) time.Duration {
	d := s.opts.Backoff
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// reconnect makes one connection attempt. Connect closes any previous
// connection first, so a half-open socket is replaced.
func (s *Syncer) reconnect(ctx context.Context) error {
	if err := s.connect(ctx); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return &connError{err: err}
	}
	return nil
}

// connectWithRetry connects at the start of a run, retrying up to
// Options.Retries times with backoff.
func (s *Syncer) connectWithRetry(ctx context.Context) error {
	err := s.reconnect(ctx)
	for attempt := 1; err != nil && attempt <= s.opts.Retries; attempt++ {
		if ctx.Err() != nil || permanent(err) {
			break
		}
		if serr := s.sleep(ctx, s.backoff(attempt)); serr != nil {
			return serr
		}
		err = s.reconnect(ctx)
	}
	return err
}

// call runs op. If op fails it reconnects, then runs op again, up to
// Options.Retries times with backoff. The returned error is the last one:
// a *connError if reconnecting failed, otherwise op's own error. Context
// errors are returned as they are.
func (s *Syncer) call(ctx context.Context, what string, op func(context.Context) error) error {
	err := op(ctx)
	for attempt := 1; err != nil && attempt <= s.opts.Retries; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if serr := s.sleep(ctx, s.backoff(attempt)); serr != nil {
			return serr
		}
		if cerr := s.reconnect(ctx); cerr != nil {
			err = cerr
			if permanent(cerr) || ctx.Err() != nil {
				break
			}
			continue
		}
		err = op(ctx)
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ce *connError
	if errors.As(err, &ce) {
		return err
	}
	return fmt.Errorf("%s: %w", what, err)
}

// defaultSleep waits d or until ctx is done.
func defaultSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
