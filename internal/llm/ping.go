package llm

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/openai/openai-go/v3"
)

// classify maps an SDK or transport error to a category and the HTTP status
// (0 when the server did not answer). The check order matters: cancellation
// first, then timeouts, then the server's status, then transport failures.
func classify(err error) (Category, int) {
	if errors.Is(err, context.Canceled) {
		return CategoryOther, 0
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		switch status {
		case 401, 403:
			return CategoryUnauthorized, status
		case 404:
			return CategoryModelNotFound, status
		case 408, 504:
			return CategoryTimeout, status
		default:
			return CategoryOther, status
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CategoryTimeout, 0
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return CategoryTimeout, 0
	}
	// Our own refusals (an oversized body, a non-loopback local address) are
	// "other", not "unreachable": the server was reached or never dialled.
	if errors.Is(err, errBodyTooLarge) || errors.Is(err, errNotLoopback) || errors.Is(err, errRedirectRefused) {
		return CategoryOther, 0
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return CategoryUnreachable, 0
	}
	return CategoryOther, 0
}

// pingReply is the tiny reply Ping asks for.
type pingReply struct {
	OK bool `json:"ok"`
}

var pingSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required":             []string{"ok"},
	"additionalProperties": false,
}

// Ping makes one tiny structured call to check the provider, the key, the model
// and the structured-output mode. It returns nil on success. On failure it
// returns an *Error whose category is one of: unreachable, unauthorized,
// model_not_found, structured_output_unsupported, timeout or other. Use
// CategoryOf to read it. Ping never returns the key or the request body.
func (c *Client) Ping(ctx context.Context) error {
	_, err := Complete(ctx, c,
		Request{System: "Health check. Answer in the requested format.", User: "Set ok to true."},
		Schema[pingReply]{
			Name:       "ping",
			JSONSchema: pingSchema,
			Decode: func(raw []byte) (pingReply, error) {
				v, err := DecodeStrict[pingReply](raw)
				if err == nil && !v.OK {
					return v, errors.New("ok must be true")
				}
				return v, err
			},
		})
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		return err // ErrCloudDisabled, ErrNoModel and similar: not a server answer
	}
	// The server answered, but not in the format the mode needs.
	if e.Category == CategoryInvalidOutput {
		return &Error{Category: CategoryStructuredUnsupported, cause: ErrInvalidOutput}
	}
	if (e.Status == 400 || e.Status == 422) && c.mode() != modePrompt {
		return &Error{Category: CategoryStructuredUnsupported, Status: e.Status}
	}
	return err
}
