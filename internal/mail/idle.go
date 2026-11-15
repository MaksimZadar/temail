package mail

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/MaksimZadar/temail/internal/config"
)

// ErrIdleUnsupported means the server does not advertise IDLE, or it has been
// turned off in Bridge. The caller must poll with STATUS instead.
var ErrIdleUnsupported = errors.New("server does not support IDLE")

// Watcher is a connection used only for IDLE. IDLE blocks, and it is only valid
// for the selected mailbox, so it gets its own connection and does not share
// the Client's operation lock. It is read-only: IDLE sends no change commands,
// and the mailbox is opened with EXAMINE.
type Watcher interface {
	// Connect dials and logs in, like Client.Connect (same pin and TLS rules).
	Connect(ctx context.Context, acct config.BridgeAccount, password string) error
	// Capabilities returns the server capabilities from the last login.
	Capabilities() []string
	// Idle watches mailbox until ctx ends or the connection fails. started is
	// called once IDLE has actually begun, so a caller never reports "watching"
	// for a session that failed to start. onChange is called for each
	// unsolicited update (new mail, expunge, flags). It runs on the read loop, so
	// it must not block. It returns ErrIdleUnsupported when IDLE is not offered.
	Idle(ctx context.Context, mailbox string, started func(), onChange func()) error
	// Close ends any running Idle and closes the connection.
	Close() error
}

// NewWatcher returns an unconnected IDLE watcher.
func NewWatcher() Watcher {
	return &imapClient{}
}

// unilateral returns the handlers for unsolicited server data. Each one only
// forwards a change signal.
func (c *imapClient) unilateral() *imapclient.UnilateralDataHandler {
	signal := func() { c.notifyChange() }
	return &imapclient.UnilateralDataHandler{
		Expunge: func(uint32) { signal() },
		Mailbox: func(*imapclient.UnilateralDataMailbox) { signal() },
		Fetch:   func(*imapclient.FetchMessageData) { signal() },
	}
}

// Idle implements Watcher. It selects mailbox read-only, starts IDLE, and calls
// onChange for each unsolicited update until ctx ends or the server ends IDLE
// with an error. go-imap re-issues IDLE by itself, before the server's timeout.
func (c *imapClient) Idle(ctx context.Context, mailbox string, started func(), onChange func()) error {
	if !slices.Contains(c.Capabilities(), "IDLE") {
		return ErrIdleUnsupported
	}
	cl, end, err := c.begin(ctx)
	if err != nil {
		return err
	}
	defer end()

	if _, err := selectReadOnly(cl, mailbox); err != nil {
		return opErr(ctx, fmt.Errorf("watch %s: %w", mailbox, err))
	}
	c.setOnChange(onChange)
	defer c.setOnChange(nil)

	cmd, err := cl.Idle()
	if err != nil {
		return opErr(ctx, fmt.Errorf("start IDLE on %s: %w", mailbox, err))
	}
	if started != nil {
		started()
	}
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Close()
		case <-finished:
		}
	}()
	err = cmd.Wait()
	close(finished)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if err != nil {
		return fmt.Errorf("IDLE on %s ended: %w", mailbox, err)
	}
	return nil
}
