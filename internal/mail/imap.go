package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/model"
)

// Timeouts for connection setup. Once the session is up, commands are bounded
// by the caller's context only.
const (
	dialTimeout  = 30 * time.Second
	setupTimeout = 30 * time.Second
)

// wordDecoder decodes RFC 2047 encoded words (subjects, names, file names)
// including non-UTF-8 charsets.
var wordDecoder = &mime.WordDecoder{CharsetReader: charset.Reader}

// imapClient implements Client on top of go-imap/v2. Only read commands are
// issued: see the package comment and the read-only tests.
type imapClient struct {
	// opMu serialises operations so that SELECT and FETCH from one call are
	// not interleaved with another call's SELECT.
	opMu sync.Mutex
	// mu guards cl. Close takes only mu, so it can interrupt a running
	// operation.
	mu sync.Mutex
	cl *imapclient.Client
}

// NewClient returns an unconnected read-only Client.
func NewClient() Client {
	return &imapClient{}
}

// Connect implements Client.
func (c *imapClient) Connect(ctx context.Context, acct config.BridgeAccount, password string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()

	if err := c.Close(); err != nil {
		return fmt.Errorf("close previous connection: %w", err)
	}
	if err := validateAccount(acct); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	cl, err := dialBridge(ctx, acct)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.cl = cl
	c.mu.Unlock()

	stop := context.AfterFunc(ctx, func() { _ = cl.Close() })
	err = cl.Login(acct.Username, password).Wait()
	stop()
	if err != nil {
		_ = c.Close()
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// The error text is the server's; the password is never included.
		var imapErr *imap.Error
		if errors.As(err, &imapErr) {
			// The server answered NO or BAD: a credential problem, not a dropped connection.
			return fmt.Errorf("%w: log in to Bridge as %s: %w", ErrAuthFailed, acct.Username, err)
		}
		return fmt.Errorf("log in to Bridge as %s: %w", acct.Username, err)
	}
	return nil
}

// validateAccount checks the parts of an account that Connect depends on and
// that config.Validate does not cover (the username and loopback host).
func validateAccount(acct config.BridgeAccount) error {
	if !isLoopbackHost(acct.Host) {
		return fmt.Errorf("%w: got %q", ErrNotLoopback, acct.Host)
	}
	if acct.IMAPPort < 1 || acct.IMAPPort > 65535 {
		return fmt.Errorf("imap port %d out of range", acct.IMAPPort)
	}
	if acct.TLSMode != config.TLSModeSTARTTLS && acct.TLSMode != config.TLSModeSSL {
		return fmt.Errorf("unsupported tlsMode %q", acct.TLSMode)
	}
	if strings.TrimSpace(acct.Username) == "" {
		return errors.New("bridge username is required")
	}
	if acct.CertFingerprint != "" && !config.ValidFingerprint(acct.CertFingerprint) {
		return errors.New("certFingerprint must be 64 hex characters (SHA-256)")
	}
	return nil
}

// dialBridge opens the TCP connection, performs TLS (implicit or STARTTLS) with
// the pin check, and returns an IMAP client that is not yet logged in.
func dialBridge(ctx context.Context, acct config.BridgeAccount) (*imapclient.Client, error) {
	addr := net.JoinHostPort(acct.Host, strconv.Itoa(acct.IMAPPort))
	pin := &pinCheck{}
	tlsCfg := tlsConfigFor(strings.ToLower(acct.CertFingerprint), pin)
	opts := &imapclient.Options{
		TLSConfig:   tlsCfg,
		WordDecoder: wordDecoder,
	}

	dialer := &net.Dialer{Timeout: dialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("connect to Bridge at %s: %w", addr, err)
	}
	_ = raw.SetDeadline(time.Now().Add(setupTimeout))
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()

	var cl *imapclient.Client
	switch acct.TLSMode {
	case config.TLSModeSSL:
		conn := tls.Client(raw, tlsCfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, handshakeErr(ctx, pin, "TLS handshake with Bridge", err)
		}
		cl = imapclient.New(conn, opts)
	default: // STARTTLS; tlsMode is validated before dialing
		cl, err = imapclient.NewStartTLS(raw, opts)
		if err != nil {
			return nil, handshakeErr(ctx, pin, "STARTTLS with Bridge", err)
		}
	}
	_ = raw.SetDeadline(time.Time{})
	return cl, nil
}

// handshakeErr prefers the pin error (typed, for errors.As) over the generic
// TLS error, and the context error over both.
func handshakeErr(ctx context.Context, pin *pinCheck, what string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if perr := pin.resolve(nil); perr != nil {
		return perr
	}
	return fmt.Errorf("%s: %w", what, err)
}

// ListMailboxes implements Client.
func (c *imapClient) ListMailboxes(ctx context.Context) ([]model.Mailbox, error) {
	cl, end, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()

	list, err := cl.List("", "*", nil).Collect()
	if err != nil {
		return nil, opErr(ctx, fmt.Errorf("list mailboxes: %w", err))
	}
	out := make([]model.Mailbox, 0, len(list))
	for _, ld := range list {
		if hasAttr(ld.Attrs, imap.MailboxAttrNoSelect) || hasAttr(ld.Attrs, imap.MailboxAttrNonExistent) {
			continue // namespace parents such as "Folders" cannot be opened
		}
		st, err := cl.Status(ld.Mailbox, &imap.StatusOptions{
			NumMessages: true,
			NumUnseen:   true,
			UIDValidity: true,
		}).Wait()
		if err != nil {
			return nil, opErr(ctx, fmt.Errorf("status of %s: %w", ld.Mailbox, err))
		}
		kind, display := classifyMailbox(ld.Mailbox, ld.Delim, ld.Attrs)
		mb := model.Mailbox{
			Name:        ld.Mailbox,
			DisplayName: display,
			Kind:        kind,
			UIDValidity: st.UIDValidity,
		}
		if st.NumMessages != nil {
			mb.Messages = *st.NumMessages
		}
		if st.NumUnseen != nil {
			mb.Unseen = *st.NumUnseen
		}
		out = append(out, mb)
	}
	return out, nil
}

// MailboxInfo implements Client.
func (c *imapClient) MailboxInfo(ctx context.Context, mailbox string) (MailboxInfo, error) {
	cl, end, err := c.begin(ctx)
	if err != nil {
		return MailboxInfo{}, err
	}
	defer end()

	data, err := selectReadOnly(cl, mailbox)
	if err != nil {
		return MailboxInfo{}, opErr(ctx, err)
	}
	info := MailboxInfo{
		Name:        mailbox,
		UIDValidity: data.UIDValidity,
		UIDNext:     uint32(data.UIDNext),
		Messages:    data.NumMessages,
	}
	if info.UIDNext > 1 {
		info.HighestUID = info.UIDNext - 1
	}
	return info, nil
}

// FetchHeaders implements Client.
func (c *imapClient) FetchHeaders(ctx context.Context, mailbox string, rng UIDRange) ([]model.MessageHeader, error) {
	cl, end, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()

	data, err := selectReadOnly(cl, mailbox)
	if err != nil {
		return nil, opErr(ctx, err)
	}
	uids, err := searchUIDs(cl, rng)
	if err != nil {
		return nil, opErr(ctx, fmt.Errorf("search UIDs in %s: %w", mailbox, err))
	}

	headers := make([]model.MessageHeader, 0, len(uids))
	for start := 0; start < len(uids); start += BatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(start+BatchSize, len(uids))
		bufs, err := cl.Fetch(imap.UIDSetNum(uids[start:end]...), headerFetchOptions()).Collect()
		if err != nil {
			return nil, opErr(ctx, fmt.Errorf("fetch headers from %s: %w", mailbox, err))
		}
		for _, buf := range bufs {
			headers = append(headers, headerFromBuffer(mailbox, data.UIDValidity, buf))
		}
	}
	return headers, nil
}

// FetchMessage implements Client.
func (c *imapClient) FetchMessage(ctx context.Context, mailbox string, uid uint32) (model.Message, error) {
	if uid == 0 {
		return model.Message{}, errors.New("invalid UID 0")
	}
	cl, end, err := c.begin(ctx)
	if err != nil {
		return model.Message{}, err
	}
	defer end()

	data, err := selectReadOnly(cl, mailbox)
	if err != nil {
		return model.Message{}, opErr(ctx, err)
	}
	bufs, err := cl.Fetch(imap.UIDSetNum(imap.UID(uid)), messageFetchOptions()).Collect()
	if err != nil {
		return model.Message{}, opErr(ctx, fmt.Errorf("fetch UID %d from %s: %w", uid, mailbox, err))
	}
	if len(bufs) != 1 {
		return model.Message{}, fmt.Errorf("%w: UID %d in %s", ErrMessageNotFound, uid, mailbox)
	}
	msg, err := messageFromBuffer(mailbox, data.UIDValidity, bufs[0])
	if err != nil {
		return model.Message{}, fmt.Errorf("%w: UID %d in %s: %v", ErrParse, uid, mailbox, err)
	}
	return msg, nil
}

// FetchAttachment implements Client.
func (c *imapClient) FetchAttachment(ctx context.Context, mailbox string, uid uint32, partID string) ([]byte, error) {
	if uid == 0 {
		return nil, errors.New("invalid UID 0")
	}
	path, ok := parsePartID(partID)
	if !ok {
		return nil, fmt.Errorf("invalid attachment part ID %q", partID)
	}
	cl, end, err := c.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()

	if _, err := selectReadOnly(cl, mailbox); err != nil {
		return nil, opErr(ctx, err)
	}
	opts := &imap.FetchOptions{
		UID:           true,
		BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		BodySection:   []*imap.FetchItemBodySection{{Part: path, Peek: true}},
	}
	bufs, err := cl.Fetch(imap.UIDSetNum(imap.UID(uid)), opts).Collect()
	if err != nil {
		return nil, opErr(ctx, fmt.Errorf("fetch part %s of UID %d: %w", partID, uid, err))
	}
	if len(bufs) != 1 || bufs[0].BodyStructure == nil {
		return nil, fmt.Errorf("message UID %d not found in %s", uid, mailbox)
	}
	ref, ok := findPart(indexParts(bufs[0].BodyStructure), partID)
	if !ok || !isAttachment(ref.part) {
		return nil, fmt.Errorf("part %s of UID %d is not an attachment", partID, uid)
	}
	raw := bufs[0].FindBodySection(&imap.FetchItemBodySection{Part: path})
	if raw == nil {
		return nil, fmt.Errorf("server returned no data for part %s", partID)
	}
	return decodeTransferEncoding(ref.part.Encoding, raw)
}

// Close implements Client. It closes the connection without waiting for a
// running operation, so it can be used to cancel one.
func (c *imapClient) Close() error {
	c.mu.Lock()
	cl := c.cl
	c.cl = nil
	c.mu.Unlock()
	if cl == nil {
		return nil
	}
	return cl.Close()
}

// begin takes the operation lock and returns the current connection. The
// returned func releases the lock. If ctx is cancelled while the operation
// runs, the connection is closed and the operation fails.
func (c *imapClient) begin(ctx context.Context) (*imapclient.Client, func(), error) {
	c.opMu.Lock()
	c.mu.Lock()
	cl := c.cl
	c.mu.Unlock()
	if cl == nil {
		c.opMu.Unlock()
		return nil, nil, ErrNotConnected
	}
	if err := ctx.Err(); err != nil {
		c.opMu.Unlock()
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = cl.Close() })
	return cl, func() {
		stop()
		c.opMu.Unlock()
	}, nil
}

// selectReadOnly opens a mailbox with EXAMINE. Nothing in the mailbox changes
// while it is open.
func selectReadOnly(cl *imapclient.Client, mailbox string) (*imap.SelectData, error) {
	data, err := cl.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", mailbox, err)
	}
	return data, nil
}

// opErr reports a cancelled context in place of the error it caused.
func opErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}
