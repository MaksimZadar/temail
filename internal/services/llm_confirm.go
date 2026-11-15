package services

import (
	"context"
	"fmt"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// confirmTimeout is how long the native dialog waits for an answer. No answer
// means "declined".
const confirmTimeout = 60 * time.Second

// CloudConfirmer asks the user, in a native dialog that the webview cannot
// drive, before a cloud provider is enabled. It returns true only on an explicit
// "Enable". Any other outcome (cancel, close, timeout) is false.
type CloudConfirmer interface {
	ConfirmCloudEnable(ctx context.Context, provider, baseURL string) (bool, error)
}

// nativeConfirmer shows a Wails question dialog.
type nativeConfirmer struct {
	dialogs *application.DialogManager
}

// NewNativeConfirmer returns a CloudConfirmer backed by the Wails dialog
// manager (app.Dialog).
func NewNativeConfirmer(dialogs *application.DialogManager) CloudConfirmer {
	return &nativeConfirmer{dialogs: dialogs}
}

// ConfirmCloudEnable shows the dialog and waits for a button, the context, or
// confirmTimeout. Show runs in its own goroutine, because the dialog API does not
// document whether it returns before the user answers. The answer channel is
// buffered, so a late click after a timeout does nothing.
func (n *nativeConfirmer) ConfirmCloudEnable(ctx context.Context, provider, baseURL string) (bool, error) {
	answer := make(chan bool, 1)
	send := func(v bool) {
		select {
		case answer <- v:
		default:
		}
	}
	d := n.dialogs.Question().
		SetTitle("Enable a cloud LLM provider?").
		SetMessage(fmt.Sprintf(
			"Once \"%s\" is enabled, email content may be sent to %s. "+
				"Only enable a provider you trust with your mail.",
			provider, baseURL))
	d.AddButton("Enable").OnClick(func() { send(true) })
	d.AddButton("Cancel").SetAsDefault().SetAsCancel().OnClick(func() { send(false) })
	go d.Show()

	timer := time.NewTimer(confirmTimeout)
	defer timer.Stop()
	select {
	case v := <-answer:
		return v, nil
	case <-ctx.Done():
		return false, ctx.Err()
	case <-timer.C:
		return false, nil
	}
}
