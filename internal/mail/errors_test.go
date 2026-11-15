package mail

import (
	"context"
	"errors"
	"testing"

	"github.com/MaksimZadar/temail/internal/config"
)

func TestWrongPasswordIsAuthFailure(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	c := NewClient()
	t.Cleanup(func() { _ = c.Close() })
	err := c.Connect(context.Background(), fx.account(fx.fingerprint), "wrong")
	if !errors.Is(err, ErrAuthFailed) {
		t.Errorf("wrong password error = %v, want ErrAuthFailed", err)
	}
}

func TestFetchMessageUnknownUIDIsNotFound(t *testing.T) {
	fx := newBridgeFixture(t, config.TLSModeSTARTTLS)
	fx.appendMessage("INBOX", plainMessage(1))
	c := fx.mustConnect()
	_, err := c.FetchMessage(context.Background(), "INBOX", 99)
	if !errors.Is(err, ErrMessageNotFound) {
		t.Errorf("FetchMessage(99) = %v, want ErrMessageNotFound", err)
	}
}
