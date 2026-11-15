package services

import (
	"context"
	"sync"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
	"github.com/MaksimZadar/temail/internal/secrets"
	"github.com/MaksimZadar/temail/internal/store"
)

// testPassword is a canary: no test output may contain it.
const testPassword = "CANARY-bridge-password-7f3a"

// fakeConfig is an in-memory ConfigStore that counts saves.
type fakeConfig struct {
	mu    sync.Mutex
	cfg   config.Config
	loadE error
	saves int
	saveE error
}

func newFakeConfig(cfg config.Config) *fakeConfig { return &fakeConfig{cfg: cloneConfig(cfg)} }

func (f *fakeConfig) Load() (config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadE != nil {
		return config.Config{}, f.loadE
	}
	return cloneConfig(f.cfg), nil
}

func (f *fakeConfig) Save(c config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveE != nil {
		return f.saveE
	}
	if err := config.Validate(c); err != nil {
		return err
	}
	f.saves++
	f.cfg = cloneConfig(c)
	return nil
}

func (f *fakeConfig) saveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves
}

func (f *fakeConfig) current() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneConfig(f.cfg)
}

func cloneConfig(c config.Config) config.Config {
	out := c
	out.Bridge = append([]config.BridgeAccount(nil), c.Bridge...)
	return out
}

// fakeSecrets is an in-memory SecretStore.
type fakeSecrets struct {
	mu       sync.Mutex
	pw       map[string]string
	probeErr error
	getErr   error
	setErr   error
	sets     int
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{pw: map[string]string{}} }

func (f *fakeSecrets) GetBridgePassword(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.pw[id]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeSecrets) SetBridgePassword(id, pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.sets++
	f.pw[id] = pw
	return nil
}

func (f *fakeSecrets) DeleteBridgePassword(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pw, id)
	return nil
}

func (f *fakeSecrets) GetLLMKey(string) (string, error) { return "", secrets.ErrNotFound }
func (f *fakeSecrets) SetLLMKey(string, string) error   { return nil }
func (f *fakeSecrets) DeleteLLMKey(string) error        { return nil }
func (f *fakeSecrets) Probe() error                     { return f.probeErr }

// fakeClient is a scriptable mail.Client. Fields are read under mu.
type fakeClient struct {
	mu sync.Mutex

	// connectErr, if set, is returned from Connect. gate, if set, blocks Connect
	// until it is closed or the context ends.
	connectErr error
	gate       chan struct{}
	// passwords records every password passed to Connect.
	passwords []string
	connects  int
	closes    int

	boxes   []model.Mailbox
	listing mail.UIDListing
	listErr error
	headers []model.MessageHeader
	info    mail.MailboxInfo

	message    model.Message
	messageErr error
	attachment []byte
	attachErr  error

	fetchMessages    int
	fetchAttachments int
}

func (c *fakeClient) Connect(ctx context.Context, _ config.BridgeAccount, password string) error {
	c.mu.Lock()
	c.connects++
	c.passwords = append(c.passwords, password)
	gate, err := c.gate, c.connectErr
	c.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (c *fakeClient) ListMailboxes(context.Context) ([]model.Mailbox, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.Mailbox(nil), c.boxes...), nil
}

func (c *fakeClient) MailboxInfo(context.Context, string) (mail.MailboxInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info, nil
}

func (c *fakeClient) FetchHeaders(_ context.Context, _ string, rng mail.UIDRange) ([]model.MessageHeader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []model.MessageHeader
	for _, h := range c.headers {
		if h.UID >= rng.First && (rng.Last == 0 || h.UID <= rng.Last) {
			out = append(out, h)
		}
	}
	return out, nil
}

func (c *fakeClient) ListUIDs(context.Context, string) (mail.UIDListing, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listing, c.listErr
}

func (c *fakeClient) FetchMessage(_ context.Context, mailbox string, uid uint32) (model.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchMessages++
	m := c.message
	m.Mailbox, m.UID = mailbox, uid
	return m, c.messageErr
}

func (c *fakeClient) FetchAttachment(context.Context, string, uint32, string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchAttachments++
	return c.attachment, c.attachErr
}

func (c *fakeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}

func (c *fakeClient) fetches() (messages, attachments int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetchMessages, c.fetchAttachments
}

// clientFactory returns a factory that hands out c and counts the calls.
type clientFactory struct {
	mu    sync.Mutex
	c     *fakeClient
	calls int
}

func (f *clientFactory) new() mail.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.c
}

func (f *clientFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeEmitter records events.
type fakeEmitter struct {
	mu     sync.Mutex
	events []emitted
}

type emitted struct {
	name string
	data any
}

func (e *fakeEmitter) Emit(name string, data ...any) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	var d any
	if len(data) == 1 {
		d = data[0]
	}
	e.events = append(e.events, emitted{name: name, data: d})
	return false
}

func (e *fakeEmitter) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.events))
	for _, ev := range e.events {
		out = append(out, ev.name)
	}
	return out
}

// fakeStore implements only the store methods the MailService uses. Any other
// call panics, because the embedded interface is nil.
type fakeStore struct {
	store.Store

	mu        sync.Mutex
	states    []store.MailboxState
	counts    []store.MailboxCounts
	headers   map[store.MessageKey]model.MessageHeader
	bodies    map[store.MessageKey]store.Body
	listed    []model.MessageHeader
	setBodies int
	lastOpts  store.ListOptions
}

func newFakeStore() *fakeStore {
	return &fakeStore{headers: map[store.MessageKey]model.MessageHeader{}, bodies: map[store.MessageKey]store.Body{}}
}

func (f *fakeStore) GetSyncState(context.Context, string) ([]store.MailboxState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.MailboxState(nil), f.states...), nil
}

func (f *fakeStore) Counts(context.Context, string) ([]store.MailboxCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.MailboxCounts(nil), f.counts...), nil
}

func (f *fakeStore) ListMessages(_ context.Context, _, _ string, opts store.ListOptions) ([]model.MessageHeader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastOpts = opts
	return append([]model.MessageHeader(nil), f.listed...), nil
}

func (f *fakeStore) GetMessage(_ context.Context, key store.MessageKey) (model.MessageHeader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.headers[key]
	if !ok {
		return model.MessageHeader{}, store.ErrNotFound
	}
	return h, nil
}

func (f *fakeStore) GetBody(_ context.Context, key store.MessageKey) (store.Body, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bodies[key]
	if !ok {
		return store.Body{}, store.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) SetBody(_ context.Context, key store.MessageKey, body store.Body) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.headers[key]; !ok {
		return store.ErrNotFound
	}
	f.setBodies++
	f.bodies[key] = body
	return nil
}

func (f *fakeStore) bodyWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setBodies
}

// readyConfig is a config with one pinned account "main" and its password.
func readyConfig() config.Config {
	c := config.Default()
	acct := config.DefaultBridgeAccount("main", "me@proton.test")
	acct.CertFingerprint = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	c.Bridge = []config.BridgeAccount{acct}
	return c
}
