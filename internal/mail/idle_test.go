package mail

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
)

// newWatchFixture connects an IDLE watcher to the same test server as fx.
func newWatchFixture(t *testing.T, fx *bridgeFixture) Watcher {
	t.Helper()
	w := NewWatcher()
	t.Cleanup(func() { _ = w.Close() })
	if err := w.Connect(context.Background(), fx.account(fx.fingerprint), testPassword); err != nil {
		t.Fatalf("watcher connect: %v", err)
	}
	return w
}

func TestIdleReportsNewMail(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", plainMessage(1))
	w := newWatchFixture(t, fx)

	if !hasIdle(w.Capabilities()) {
		t.Skip("the test server does not advertise IDLE")
	}
	var changes atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- w.Idle(ctx, "INBOX", func() {}, func() { changes.Add(1) })
	}()

	time.Sleep(100 * time.Millisecond) // let IDLE start
	fx.appendMessage("INBOX", plainMessage(2))
	deadline := time.Now().Add(3 * time.Second)
	for changes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Idle returned %v, want context.Canceled", err)
	}
	if changes.Load() == 0 {
		t.Fatal("no change reported for a new message")
	}
}

func TestIdleUnsupportedWithoutCapability(t *testing.T) {
	w := NewWatcher()
	if err := w.Idle(context.Background(), "INBOX", nil, func() {}); !errors.Is(err, ErrIdleUnsupported) {
		t.Fatalf("unconnected watcher: err = %v, want ErrIdleUnsupported", err)
	}
	if hasIdle(nil) {
		t.Fatal("no capabilities must not report IDLE")
	}
}

func TestIdleContextEndsWithoutLeak(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	w := newWatchFixture(t, fx)
	if !hasIdle(w.Capabilities()) {
		t.Skip("the test server does not advertise IDLE")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Idle(ctx, "INBOX", nil, func() {}) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Idle did not return after cancel")
	}
}

func hasIdle(caps []string) bool {
	for _, c := range caps {
		if c == "IDLE" {
			return true
		}
	}
	return false
}
