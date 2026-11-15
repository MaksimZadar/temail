package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	return &MailService{st: st, cfg: cfg, sess: bridge.NewSession(resolve, newClient)}
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
	LastSync    *time.Time        `json:"lastSync"`
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

// ListMailboxes returns the cached mailbox list, INBOX first.
func (m *MailService) ListMailboxes() ([]MailboxDTO, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
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
func (m *MailService) ListMessages(mailbox string, cursor *PageCursor, limit int) (MessagePage, error) {
	if err := checkMailbox(mailbox); err != nil {
		return MessagePage{}, err
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	headers, err := m.st.ListMessages(ctx, acct, mailbox, opts)
	if err != nil {
		return MessagePage{}, fmt.Errorf("read messages: %w", err)
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
func (m *MailService) GetMessage(mailbox string, uidValidity, uid uint32) (model.Message, error) {
	if err := checkMailbox(mailbox); err != nil {
		return model.Message{}, err
	}
	if uid == 0 {
		return model.Message{}, fmt.Errorf("%w: uid", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	return m.loadMessage(ctx, mailbox, uidValidity, uid)
}

// GetAttachment returns the bytes of one attachment. partID must be one of the
// parts listed for the message. The part is fetched from Bridge, and nothing is
// written to disk. Content over MaxAttachmentBytes is refused.
func (m *MailService) GetAttachment(mailbox string, uidValidity, uid uint32, partID string) (AttachmentDTO, error) {
	if err := checkMailbox(mailbox); err != nil {
		return AttachmentDTO{}, err
	}
	if uid == 0 || len(partID) > maxPartIDLen || !partIDPattern.MatchString(partID) {
		return AttachmentDTO{}, fmt.Errorf("%w: attachment", ErrInvalidInput)
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
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

// ServiceShutdown drops the on-demand Bridge connection. It is a lifecycle
// hook, not a bound method. It does not wait for a running fetch.
func (m *MailService) ServiceShutdown() error {
	return m.sess.Close()
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
