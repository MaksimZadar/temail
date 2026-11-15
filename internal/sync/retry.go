package sync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/MaksimZadar/temail/internal/mail"
)

// maxBackoff caps the delay between reconnect attempts.
const maxBackoff = 30 * time.Second

// connError marks a failure to (re)connect to Bridge. Callers treat it as a
// connection-level failure: it aborts the run once retries are exhausted.
type connError struct{ err error }

func (e *connError) Error() string { return fmt.Sprintf("reconnect to Bridge: %v", e.err) }
func (e *connError) Unwrap() error { return e.err }

// isTransport reports whether err means the connection is gone or unusable:
// a network or IO error, a closed connection, or no connection at all. A
// server reply (NO or BAD), a parse error or "not found" is not transport:
// reconnecting would not change the answer.
func isTransport(err error) bool {
	if err == nil {
		return false
	}
	var imapErr *imap.Error
	if errors.As(err, &imapErr) {
		return false
	}
	if errors.Is(err, mail.ErrAuthFailed) {
		return false
	}
	if errors.Is(err, mail.ErrNotConnected) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// permanent reports errors that retrying cannot fix, even if they look like
// transport errors: an untrusted or mismatched certificate and a non-loopback
// host need user action, and a bad password (ErrAuthFailed) would fail the
// same way on every run.
func permanent(err error) bool {
	return errors.Is(err, mail.ErrUntrustedCert) ||
		errors.Is(err, mail.ErrCertMismatch) ||
		errors.Is(err, mail.ErrNotLoopback) ||
		errors.Is(err, mail.ErrAuthFailed)
}

// retryable reports whether an error should trigger a reconnect and a retry.
func retryable(err error) bool {
	return isTransport(err) && !permanent(err)
}

// retries returns the configured retry count. An unset value takes the default.
func (s *Syncer) retries() int {
	if s.opts.Retries == nil {
		return DefaultRetries
	}
	return *s.opts.Retries
}

// backoff returns the delay before retry attempt n (1-based): Backoff, doubled
// each time, capped at maxBackoff.
func (s *Syncer) backoff(n int) time.Duration {
	d := s.opts.Backoff
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}

// reconnect makes one connection attempt. Connect closes any previous
// connection first, so a half-open socket is replaced. A failure is wrapped in
// connError.
func (s *Syncer) reconnect(ctx context.Context) error {
	if err := s.connect(ctx); err != nil {
		s.connected = false
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return &connError{err: err}
	}
	s.connected = true
	return nil
}

// connectWithRetry connects at the start of a run. It retries only transport
// failures, up to the retry count, with backoff.
func (s *Syncer) connectWithRetry(ctx context.Context) error {
	err := s.reconnect(ctx)
	for attempt := 1; err != nil && retryable(err) && attempt <= s.retries(); attempt++ {
		if ctx.Err() != nil {
			break
		}
		if serr := s.sleep(ctx, s.backoff(attempt)); serr != nil {
			return serr
		}
		err = s.reconnect(ctx)
	}
	return err
}

// call runs op. If op fails with a transport error, it waits, reconnects and
// runs op again, up to the retry count. Any other error (a server reply, a
// parse error, "not found") is returned at once, with no reconnect. The
// returned error is the last one: a *connError if reconnecting failed, a
// context error if the context ended, otherwise op's error wrapped with what.
func (s *Syncer) call(ctx context.Context, what string, op func(context.Context) error) error {
	err := op(ctx)
	for attempt := 1; err != nil && retryable(err) && attempt <= s.retries(); attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if serr := s.sleep(ctx, s.backoff(attempt)); serr != nil {
			return serr
		}
		if cerr := s.reconnect(ctx); cerr != nil {
			err = cerr
			if !retryable(cerr) || ctx.Err() != nil {
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
