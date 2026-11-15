package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/bridge"
	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/store"
)

// fetchTimeout bounds one on-demand read from Bridge.
const fetchTimeout = 60 * time.Second

// partIDPattern matches IMAP section numbers such as "2" or "2.1".
var partIDPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

// MailService serves mail from the local cache. It connects to Bridge only
// when a body or an attachment is not cached yet, and it never writes to the
// mailbox.
type MailService struct {
	st   store.Store
	cfg  ConfigStore
	sess *bridge.Session

	// drainWait bounds shutdown's wait for in-flight calls. Zero means shutdownWait.
	drainWait time.Duration

	mu         sync.Mutex // guards closed; begin adds to wg under it
	closed     bool
	base       context.Context // cancelled by shutdown: stops in-flight calls
	cancelBase context.CancelFunc
	wg         sync.WaitGroup // in-flight bound calls
}

// NewMailService returns a MailService. sec supplies the password for the
// on-demand fetches.
func NewMailService(st store.Store, cfg ConfigStore, sec PasswordSource, newClient ClientFactory) *MailService {
	resolve := func(context.Context) (config.BridgeAccount, string, error) {
		c, err := cfg.Load()
		if err != nil {
			return config.BridgeAccount{}, "", fmt.Errorf("load config: %w", err)
		}
		idx, err := bridge.PickAccount(c, "")
		if err != nil {
			return config.BridgeAccount{}, "", err
		}
		pw, err := bridge.Password(sec, c.Bridge[idx].ID)
		if err != nil {
			return config.BridgeAccount{}, "", err
		}
		return c.Bridge[idx], pw, nil
	}
	base, cancel := context.WithCancel(context.Background())
	return &MailService{st: st, cfg: cfg, sess: bridge.NewSession(resolve, newClient), base: base, cancelBase: cancel}
}

// PasswordSource reads the Bridge password. secrets.Store satisfies it.
type PasswordSource = bridge.PasswordSource

// MailboxDTO is one mailbox as shown in the folder list.
type MailboxDTO struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	Kind        model.MailboxKind `json:"kind"`
	UIDValidity uint32            `json:"uidValidity"`
	Messages    int               `json:"messages"`
	Bodies      int               `json:"bodies"`
	// Unseen is the number of messages in this mailbox without \Seen, from its
	// last sync. 0 until the mailbox has synced.
	Unseen   uint32     `json:"unseen"`
	LastSync *time.Time `json:"lastSync"`
}

// PageCursor marks the position after the last message of a page. Send it
// back unchanged to get the next page.
type PageCursor struct {
	Date time.Time `json:"date"`
	UID  uint32    `json:"uid"`
}

// MessagePage is one page of message headers, newest first.
type MessagePage struct {
	Messages []model.MessageHeader `json:"messages"`
	Next     *PageCursor           `json:"next"`
}

// AttachmentDTO is the content of one attachment. Data is base64 in JSON.
type AttachmentDTO struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Data        []byte `json:"data"`
}

// begin registers one bound call. The call's context ends when the Wails
// context ends, when fetchTimeout passes, or when the service shuts down. end
// must be called when the call returns. After shutdown, begin fails with
// ErrClosed.
func (m *MailService) begin(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, ErrClosed
	}
	m.wg.Add(1)
	cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	stop := context.AfterFunc(m.base, cancel)
	return cctx, func() {
		stop()
		cancel()
		m.wg.Done()
	}, nil
}

// shutdown refuses new calls, cancels running ones, closes the Bridge session
// and waits up to shutdownWait for in-flight calls. It is idempotent.
func (m *MailService) shutdown() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.mu.Unlock()

	m.cancelBase()
	sessErr := m.sess.Close()
	drained := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(drained)
	}()
	wait := m.drainWait
	if wait <= 0 {
		wait = shutdownWait
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-drained:
		return sessErr
	case <-timer.C:
		return errors.Join(sessErr, ErrInFlightTimeout)
	}
}

// CacheInfoDTO is the local cache size, for the footer. Bytes is the size on
// disk of the database and its -wal and -shm files. Messages and Bodies count
// the rows of the configured account (0 when no account is configured).
type CacheInfoDTO struct {
	Bytes    int64 `json:"bytes"`
	Messages int64 `json:"messages"`
	Bodies   int64 `json:"bodies"`
}

// CacheInfo returns the local cache size and row counts. It only reads the
// cache and the file sizes. It never connects to Bridge and never touches the
// mailbox. Cheap enough to poll during a sync.
func (m *MailService) CacheInfo(ctx context.Context) (CacheInfoDTO, error) {
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return CacheInfoDTO{}, err
	}
	defer end()
	acct, err := m.accountID()
	if errors.Is(err, bridge.ErrNotConfigured) {
		// No account: the file sizes still count, and no rows belong to it.
		acct = ""
	} else if err != nil {
		return CacheInfoDTO{}, err
	}
	st, err := m.st.CacheStats(ctx, acct)
	if err != nil {
		return CacheInfoDTO{}, fmt.Errorf("read cache stats: %w", err)
	}
	return CacheInfoDTO{Bytes: st.Bytes, Messages: st.Messages, Bodies: st.Bodies}, nil
}

// ListMailboxes returns the cached mailbox list, INBOX first.
func (m *MailService) ListMailboxes(ctx context.Context) ([]MailboxDTO, error) {
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer end()
	acct, err := m.accountID()
	if err != nil {
		return nil, err
	}
	states, err := m.st.GetSyncState(ctx, acct)
	if err != nil {
		return nil, fmt.Errorf("read mailboxes: %w", err)
	}
	counts, err := m.st.Counts(ctx, acct)
	if err != nil {
		return nil, fmt.Errorf("read counts: %w", err)
	}
	byName := make(map[string]store.MailboxCounts, len(counts))
	for _, c := range counts {
		byName[c.Mailbox] = c
	}
	out := make([]MailboxDTO, 0, len(states))
	for _, s := range states {
		d := MailboxDTO{
			Name:        s.Name,
			DisplayName: s.DisplayName,
			Kind:        s.Kind,
			UIDValidity: s.UIDValidity,
			Messages:    byName[s.Name].Messages,
			Bodies:      byName[s.Name].Bodies,
			Unseen:      s.Unseen,
		}
		if !s.LastSync.IsZero() {
			t := s.LastSync
			d.LastSync = &t
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].Name == "INBOX", out[j].Name == "INBOX"
		if ai != aj {
			return ai
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListMessages returns one page of cached headers of a mailbox, newest first.
// Pass a nil cursor for the first page and the previous Next for later pages.
// limit defaults to DefaultPageSize and is capped at store.MaxListLimit.
func (m *MailService) ListMessages(ctx context.Context, mailbox string, cursor *PageCursor, limit int) (MessagePage, error) {
	if err := checkMailbox(mailbox); err != nil {
		return MessagePage{}, err
	}
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return MessagePage{}, err
	}
	defer end()
	if limit <= 0 {
		limit = DefaultPageSize
	}
	limit = min(limit, store.MaxListLimit)
	acct, err := m.accountID()
	if err != nil {
		return MessagePage{}, err
	}
	opts := store.ListOptions{Limit: limit}
	if cursor != nil {
		opts.Before = &store.Cursor{Date: cursor.Date, UID: cursor.UID}
	}
	headers, err := m.st.ListMessages(ctx, acct, mailbox, opts)
	if err != nil {
		return MessagePage{}, fmt.Errorf("read messages: %w", err)
	}
	for i := range headers {
		if err := m.fillMailboxes(ctx, acct, &headers[i]); err != nil {
			return MessagePage{}, err
		}
	}
	if headers == nil {
		headers = []model.MessageHeader{}
	}
	page := MessagePage{Messages: headers}
	if len(headers) == limit {
		last := headers[len(headers)-1]
		page.Next = &PageCursor{Date: last.Date, UID: last.UID}
	}
	return page, nil
}

// GetMessage returns one message: headers, sanitised text and HTML, and
// attachment metadata. The body comes from the cache. If it is not cached, the
// message is fetched from Bridge and stored. uidValidity must match the
// cached epoch.
func (m *MailService) GetMessage(ctx context.Context, mailbox string, uidValidity, uid uint32) (model.Message, error) {
	if err := checkMailbox(mailbox); err != nil {
		return model.Message{}, err
	}
	if uid == 0 {
		return model.Message{}, fmt.Errorf("%w: uid", ErrInvalidInput)
	}
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return model.Message{}, err
	}
	defer end()
	return m.loadMessage(ctx, mailbox, uidValidity, uid)
}

// GetAttachment returns the bytes of one attachment. partID must be one of the
// parts listed for the message. The part is fetched from Bridge, and nothing is
// written to disk. Content over MaxAttachmentBytes is refused.
func (m *MailService) GetAttachment(ctx context.Context, mailbox string, uidValidity, uid uint32, partID string) (AttachmentDTO, error) {
	if err := checkMailbox(mailbox); err != nil {
		return AttachmentDTO{}, err
	}
	if uid == 0 || len(partID) > maxPartIDLen || !partIDPattern.MatchString(partID) {
		return AttachmentDTO{}, fmt.Errorf("%w: attachment", ErrInvalidInput)
	}
	ctx, end, err := m.begin(ctx)
	if err != nil {
		return AttachmentDTO{}, err
	}
	defer end()
	msg, err := m.loadMessage(ctx, mailbox, uidValidity, uid)
	if err != nil {
		return AttachmentDTO{}, err
	}
	var meta *model.Attachment
	for i := range msg.Attachments {
		if msg.Attachments[i].PartID == partID {
			meta = &msg.Attachments[i]
			break
		}
	}
	if meta == nil {
		return AttachmentDTO{}, ErrNoSuchAttachment
	}
	if meta.Size > MaxAttachmentBytes {
		return AttachmentDTO{}, ErrAttachmentTooLarge
	}

	var data []byte
	err = m.sess.With(ctx, func(ctx context.Context, c mail.Client) error {
		if err := checkEpoch(ctx, c, mailbox, uidValidity); err != nil {
			return err
		}
		var ferr error
		data, ferr = c.FetchAttachment(ctx, mailbox, uid, partID)
		return ferr
	})
	if err != nil {
		return AttachmentDTO{}, fmt.Errorf("fetch attachment: %w", err)
	}
	if len(data) > MaxAttachmentBytes {
		return AttachmentDTO{}, ErrAttachmentTooLarge
	}
	return AttachmentDTO{
		Filename:    meta.Filename,
		ContentType: meta.ContentType,
		Size:        int64(len(data)),
		Data:        data,
	}, nil
}

// loadMessage returns the cached message, or fetches and caches its body when
// it is missing. A message whose header is not cached is ErrNotCached.
func (m *MailService) loadMessage(ctx context.Context, mailbox string, uidValidity, uid uint32) (model.Message, error) {
	acct, err := m.accountID()
	if err != nil {
		return model.Message{}, err
	}
	key := store.MessageKey{Account: acct, Mailbox: mailbox, UIDValidity: uidValidity, UID: uid}
	hdr, err := m.st.GetMessage(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return model.Message{}, ErrNotCached
	}
	if err != nil {
		return model.Message{}, fmt.Errorf("read message: %w", err)
	}

	if err := m.fillMailboxes(ctx, acct, &hdr); err != nil {
		return model.Message{}, err
	}
	body, err := m.st.GetBody(ctx, key)
	if err == nil {
		return model.Message{
			MessageHeader: hdr,
			TextBody:      body.Text,
			HTMLBody:      body.HTML,
			Attachments:   body.Attachments,
		}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return model.Message{}, fmt.Errorf("read body: %w", err)
	}

	// Body not cached yet: fetch it, keeping the epoch check so the UID cannot
	// resolve to a different message after a UIDVALIDITY reset.
	var fetched model.Message
	err = m.sess.With(ctx, func(ctx context.Context, c mail.Client) error {
		if err := checkEpoch(ctx, c, mailbox, uidValidity); err != nil {
			return err
		}
		var ferr error
		fetched, ferr = c.FetchMessage(ctx, mailbox, uid)
		return ferr
	})
	if err != nil {
		return model.Message{}, fmt.Errorf("fetch message: %w", err)
	}
	err = m.st.SetBody(ctx, key, store.Body{Text: fetched.TextBody, HTML: fetched.HTMLBody, Attachments: fetched.Attachments})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return model.Message{}, fmt.Errorf("cache body: %w", err)
	}
	fetched.MessageHeader = hdr
	return fetched, nil
}

// ServiceShutdown is the Wails lifecycle hook. Shutdown already did the work,
// so this only runs it again, which is a no-op.
func (m *MailService) ServiceShutdown() error {
	return m.shutdown()
}

// accountID returns the ID of the first account in config.json.
func (m *MailService) accountID() (string, error) {
	cfg, err := m.cfg.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	idx, err := bridge.PickAccount(cfg, "")
	if err != nil {
		return "", err
	}
	return cfg.Bridge[idx].ID, nil
}

// fillMailboxes sets h.Mailboxes to every mailbox the message is in (its folder
// and labels), from the cache. The placement key of h identifies the message.
func (m *MailService) fillMailboxes(ctx context.Context, acct string, h *model.MessageHeader) error {
	key := store.MessageKey{Account: acct, Mailbox: h.Mailbox, UIDValidity: h.UIDValidity, UID: h.UID}
	places, err := m.st.MembershipOf(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		h.Mailboxes = []model.MailboxRef{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read membership: %w", err)
	}
	refs := make([]model.MailboxRef, 0, len(places))
	for _, p := range places {
		refs = append(refs, model.MailboxRef{Name: p.Mailbox, DisplayName: p.DisplayName, Kind: p.Kind})
	}
	h.Mailboxes = refs
	return nil
}

// checkEpoch fails if the server's UIDVALIDITY for mailbox is not epoch.
func checkEpoch(ctx context.Context, c mail.Client, mailbox string, epoch uint32) error {
	info, err := c.MailboxInfo(ctx, mailbox)
	if err != nil {
		return err
	}
	if info.UIDValidity != epoch {
		return ErrStaleEpoch
	}
	return nil
}

func checkMailbox(name string) error {
	if name == "" || len(name) > maxMailboxName {
		return fmt.Errorf("%w: mailbox", ErrInvalidInput)
	}
	return nil
}
