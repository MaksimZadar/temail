package services

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/mail"
	"github.com/MaksimZadar/temail/internal/model"
)

// fakeWatcher is the IDLE connection used by the service. kick delivers an
// unsolicited update. failConnect, if set, is returned by Connect.
type fakeWatcher struct {
	caps        []string
	kick        chan struct{}
	failConnect error
	active      atomic.Int32
	closed      atomic.Int32
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{caps: []string{"IDLE", "IMAP4REV1"}, kick: make(chan struct{}, 8)}
}

func (w *fakeWatcher) Connect(ctx context.Context, _ config.BridgeAccount, _ string) error {
	if w.failConnect != nil {
		return w.failConnect
	}
	return ctx.Err()
}

func (w *fakeWatcher) Capabilities() []string { return w.caps }

func (w *fakeWatcher) Idle(ctx context.Context, _ string, onChange func()) error {
	w.active.Add(1)
	defer w.active.Add(-1)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.kick:
			onChange()
		}
	}
}

func (w *fakeWatcher) Close() error {
	w.closed.Add(1)
	return nil
}

// watchingFixture is a SyncService that keeps watching after its first sync.
// The fake client's STATUS answers are set in the test.
func watchingFixture(t *testing.T, w *fakeWatcher) *syncFixture {
	t.Helper()
	fx := newSyncFixture(t, watchableConfig())
	fx.svc.watchOff = false
	fx.svc.watchInterval = time.Hour
	fx.svc.watchPoll = time.Hour
	fx.svc.newWatcher = func() mail.Watcher { return w }
	fx.client.mu.Lock()
	fx.client.statuses = map[string]mail.MailboxStatus{}
	fx.client.mu.Unlock()
	return fx
}

// watchableConfig is a configured, pinned account.
func watchableConfig() config.Config {
	return readyConfig()
}

// has reports whether an event with this name was emitted.
func (e *fakeEmitter) has(name string) bool {
	for _, n := range e.names() {
		if n == name {
			return true
		}
	}
	return false
}

func (f *syncFixture) setStatus(mailbox string, st mail.MailboxStatus) {
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	f.client.statuses[mailbox] = st
}

// The initial sync finishes, the watcher starts, a change is found and synced,
// sync:changed is emitted, and Status reports watching and idle.
func TestServiceWatchesAfterSyncAndEmitsChanged(t *testing.T) {
	w := newFakeWatcher()
	fx := watchingFixture(t, w)
	fx.setStatus("INBOX", mail.MailboxStatus{Name: "INBOX", UIDValidity: 7, UIDNext: 4, Messages: 3})
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "watching", func() bool {
		st := fx.svc.Status()
		return st.Watching && st.Idle && !st.Running
	})
	if !fx.ev.has(EventSyncDone) {
		t.Fatal("sync:done was not emitted before watching")
	}

	// The server now has a fourth message.
	fx.client.mu.Lock()
	fx.client.headers = append(fx.client.headers, newInboxHeader(7, 4))
	fx.client.listing.Messages = append(fx.client.listing.Messages, mail.UIDState{UID: 4})
	fx.client.mu.Unlock()
	fx.setStatus("INBOX", mail.MailboxStatus{Name: "INBOX", UIDValidity: 7, UIDNext: 5, Messages: 4})
	w.kick <- struct{}{}

	waitUntil(t, "sync:changed", func() bool { return fx.ev.has(EventSyncChanged) })
	for _, e := range fx.ev.events {
		if e.name == EventSyncChanged {
			c, ok := e.data.(ChangedEvent)
			if !ok || len(c.Mailboxes) != 1 || c.Mailboxes[0] != "INBOX" {
				t.Fatalf("sync:changed payload = %#v", e.data)
			}
		}
	}
	if st := fx.svc.Status(); st.LastChecked == nil {
		t.Fatal("LastChecked not set after a check")
	}

	if err := fx.svc.Stop(); err != nil {
		t.Fatal(err)
	}
	st := fx.svc.Status()
	if st.Watching || st.Idle || st.Running {
		t.Fatalf("after Stop: %+v, want nothing running", st)
	}
	if w.active.Load() != 0 {
		t.Fatal("the IDLE goroutine is still running after Stop")
	}
}

// A permanent error from the IDLE connection (auth failure) ends the watch with
// a sync:error, and it is not retried.
func TestServiceWatchAuthFailureEmitsError(t *testing.T) {
	w := newFakeWatcher()
	w.failConnect = mail.ErrAuthFailed
	fx := watchingFixture(t, w)
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "sync:error from the watcher", func() bool {
		return fx.ev.has(EventSyncError) && !fx.svc.Status().Watching
	})
	if st := fx.svc.Status(); st.LastError == "" {
		t.Fatalf("LastError empty after auth failure: %+v", st)
	}
}

// Stop during watching returns promptly and reports no error event.
func TestServiceStopWhileWatchingIsSilent(t *testing.T) {
	w := newFakeWatcher()
	fx := watchingFixture(t, w)
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "watching", func() bool { return fx.svc.Status().Watching })
	if err := fx.svc.Stop(); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if fx.ev.has(EventSyncError) {
		t.Fatal("a stop must not emit sync:error")
	}
}

// Status reports Watching, Idle and LastChecked as separate fields, and Running
// only during the initial sync.
func TestStatusFieldsAreSeparate(t *testing.T) {
	fx := newSyncFixture(t, watchableConfig())
	st := fx.svc.Status()
	if st.Running || st.Watching || st.Idle || st.LastChecked != nil {
		t.Fatalf("idle service status = %+v", st)
	}
}

// newInboxHeader is a header for a message that arrived later.
func newInboxHeader(validity, uid uint32) model.MessageHeader {
	return model.MessageHeader{Mailbox: "INBOX", UIDValidity: validity, UID: uid, Subject: "new", Date: t0}
}
