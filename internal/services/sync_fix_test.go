package services

import (
	"errors"
	"testing"

	"github.com/MaksimZadar/temail/internal/mail"
)

// G: the end of the initial sync and the start of watching happen under one
// lock. A sync:done listener never sees the run as neither syncing nor watching.
func TestHandoffNeverLooksIdle(t *testing.T) {
	w := newFakeWatcher()
	fx := watchingFixture(t, w)
	var seen []SyncStatus
	fx.svc.emitter = &statusEmitter{inner: fx.ev, svc: fx.svc, onDone: func(st SyncStatus) { seen = append(seen, st) }}
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "watching", func() bool { return fx.svc.Status().Watching })
	fx.svc.Stop()
	if len(seen) != 1 {
		t.Fatalf("sync:done seen %d times", len(seen))
	}
	if seen[0].Running || !seen[0].Watching {
		t.Fatalf("at sync:done the status was %+v, want watching and not running", seen[0])
	}
}

// statusEmitter forwards to the test emitter and records the status at sync:done.
type statusEmitter struct {
	inner  *fakeEmitter
	svc    *SyncService
	onDone func(SyncStatus)
}

func (e *statusEmitter) Emit(name string, data ...any) bool {
	if name == EventSyncDone {
		e.onDone(e.svc.Status())
	}
	return e.inner.Emit(name, data...)
}

// G: Start while the watcher runs asks it for a check, and returns nil. It does
// not report "already running".
func TestStartWhileWatchingKicksCheck(t *testing.T) {
	w := newFakeWatcher()
	fx := watchingFixture(t, w)
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "watching", func() bool { return fx.svc.Status().Watching })

	fx.setStatus("INBOX", mail.MailboxStatus{Name: "INBOX", UIDValidity: 7, UIDNext: 4, Messages: 3})
	if err := fx.svc.Start(); err != nil {
		t.Fatalf("Start while watching = %v, want nil", err)
	}
	waitUntil(t, "sync:changed from the kick", func() bool { return fx.ev.has(EventSyncChanged) })
	if err := fx.svc.Stop(); err != nil {
		t.Fatal(err)
	}
}

// H: a refused login is latched for the app session. The second start with the
// same credentials returns the latched error and does not log in again.
func TestServiceAuthLatchOneStrike(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.mu.Lock()
	fx.client.connectErr = mail.ErrAuthFailed
	fx.client.mu.Unlock()

	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "sync:error", func() bool { return fx.ev.has(EventSyncError) })

	fx.client.mu.Lock()
	before := fx.client.connects
	fx.client.mu.Unlock()
	err := fx.svc.Start()
	if !errors.Is(err, mail.ErrAuthFailed) {
		t.Fatalf("second start = %v, want the latched ErrAuthFailed", err)
	}
	fx.client.mu.Lock()
	after := fx.client.connects
	fx.client.mu.Unlock()
	if after != before {
		t.Fatalf("a refused login was retried: connects %d -> %d", before, after)
	}
}

// H: a changed password clears the latch, and the next start logs in.
func TestServiceAuthLatchClearsOnNewPassword(t *testing.T) {
	fx := newSyncFixture(t, readyConfig())
	fx.client.mu.Lock()
	fx.client.connectErr = mail.ErrAuthFailed
	fx.client.mu.Unlock()
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "sync:error", func() bool { return fx.ev.has(EventSyncError) })

	fx.client.mu.Lock()
	fx.client.connectErr = nil
	before := fx.client.connects
	fx.client.mu.Unlock()
	fx.sec.pw["main"] = "a-new-password"
	if err := fx.svc.Start(); err != nil {
		t.Fatalf("start with a new password = %v", err)
	}
	waitUntil(t, "a new login", func() bool {
		fx.client.mu.Lock()
		defer fx.client.mu.Unlock()
		return fx.client.connects > before
	})
	fx.svc.Stop()
}

// H: one login serves the sync and the watcher's checks. The connection is not
// closed when the sync ends.
func TestServiceWatcherReusesSyncConnection(t *testing.T) {
	w := newFakeWatcher()
	fx := watchingFixture(t, w)
	if err := fx.svc.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "watching", func() bool { return fx.svc.Status().Watching })
	fx.setStatus("INBOX", mail.MailboxStatus{Name: "INBOX", UIDValidity: 7, UIDNext: 4, Messages: 3})
	fx.svc.Start() // a kick: a check runs on the same connection

	waitUntil(t, "the kicked check to run", func() bool { return fx.ev.has(EventSyncChanged) })
	fx.client.mu.Lock()
	logins := fx.client.connects
	fx.client.mu.Unlock()
	if logins != 1 {
		t.Fatalf("the sync and the watcher logged in %d times, want 1", logins)
	}
	_ = fx.svc.Stop()
}
