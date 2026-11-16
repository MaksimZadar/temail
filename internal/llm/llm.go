// Package llm is the OpenAI-compatible LLM client for temail: one client for
// OpenRouter, Ollama and any endpoint that speaks the chat-completions API. It
// has no mail knowledge. Callers pass it already-prepared text. See README.md.
//
// Privacy rules enforced here:
//   - A cloud provider refuses every call until it is enabled (ErrCloudDisabled).
//   - API keys are passed to the SDK and never stored in an error, a log line or
//     a return value. Error text is fixed per category, so it cannot carry a
//     request body or a key.
//   - Model output is plain text. The package never renders it, and it never
//     fetches URLs found in it.
package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
)

// DefaultTimeout bounds one request when Options.Timeout is zero and the
// request timeout is enabled.
const DefaultTimeout = 60 * time.Second

// DefaultDialTimeout bounds the connect and TLS handshake steps even when the
// overall request timeout is disabled. A local model may take minutes to reply,
// but a dead endpoint should still fail quickly.
const DefaultDialTimeout = 10 * time.Second

// Sentinel errors. Callers match them with errors.Is.
var (
	// ErrCloudDisabled means a cloud provider is not enabled. Nothing was sent.
	ErrCloudDisabled = errors.New("cloud LLM provider is disabled; enable it before any content is sent to it")
	// ErrNoModel means the provider has no model name.
	ErrNoModel = errors.New("no model is configured for this provider")
	// ErrModelListUnsupported means the endpoint does not list models. ListModels
	// returns an empty list with this error, so the caller can degrade.
	ErrModelListUnsupported = errors.New("this endpoint does not list models")
	// ErrInvalidOutput means the model's reply failed the schema check, even
	// after one retry. No partial result is returned.
	ErrInvalidOutput = errors.New("model output failed validation after one retry")
	// ErrTooLarge means the request exceeds the provider's maxContext estimate.
	ErrTooLarge = errors.New("request is larger than the provider's context limit")
	// ErrBadSchema means the caller's Schema is incomplete or has a bad name.
	ErrBadSchema = errors.New("invalid structured-output schema")
)

// Category classifies a failed call for the UI.
type Category string

const (
	CategoryOK                    Category = "ok"
	CategoryUnreachable           Category = "unreachable"
	CategoryUnauthorized          Category = "unauthorized"
	CategoryModelNotFound         Category = "model_not_found"
	CategoryStructuredUnsupported Category = "structured_output_unsupported"
	CategoryTimeout               Category = "timeout"
	CategoryInvalidOutput         Category = "invalid_output"
	CategoryOther                 Category = "other"
)

// Error is a failed call. Its text is fixed per category, so it never contains a
// key, a prompt or a response. Format and GoString print the same text for every
// verb, so %+v and %#v do not expand fields.
//
// The SDK error is not kept. It carries the request (headers included, so the
// bearer key) and the response body. Unwrap returns only the context sentinels
// (context.Canceled, context.DeadlineExceeded) or ErrInvalidOutput, so
// errors.Is works for those and nothing else leaks.
type Error struct {
	Category Category
	// Status is the HTTP status when the server answered, else 0.
	Status int
	cause  error
}

func (e *Error) Error() string {
	return "llm " + string(e.Category) + ": " + categoryText(e.Category)
}

// Format writes the fixed text for every verb.
func (e *Error) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, e.Error())
}

// GoString is the fixed text, so %#v cannot show the cause.
func (e *Error) GoString() string {
	return fmt.Sprintf("llm.Error{Category: %q, Status: %d}", string(e.Category), e.Status)
}

// Unwrap returns the sentinel cause, if any. It never returns an SDK or
// transport error.
func (e *Error) Unwrap() error { return e.cause }

func categoryText(c Category) string {
	switch c {
	case CategoryUnreachable:
		return "the endpoint could not be reached"
	case CategoryUnauthorized:
		return "the endpoint rejected the API key"
	case CategoryModelNotFound:
		return "the model or the endpoint path was not found"
	case CategoryStructuredUnsupported:
		return "the endpoint does not return structured output; turn off structuredOutput for this provider"
	case CategoryTimeout:
		return "the request timed out"
	case CategoryInvalidOutput:
		return "the model output failed validation after one retry"
	case CategoryOK:
		return "ok"
	default:
		return "the request failed"
	}
}

// CategoryOf returns the category of err: CategoryOK for nil, the *Error
// category, or CategoryOther.
func CategoryOf(err error) Category {
	if err == nil {
		return CategoryOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Category
	}
	return CategoryOther
}

// Provider is what the client needs to know about one endpoint. Build it with
// ProviderFrom so that kind and enabled follow the config rules.
type Provider struct {
	Name         string
	BaseURL      string
	Model        string
	Kind         string
	Enabled      bool
	Capabilities config.LLMCapabilities
}

// ProviderFrom returns the Provider for a config entry.
func ProviderFrom(p config.LLMProvider) Provider {
	return Provider{
		Name:         p.Name,
		BaseURL:      p.BaseURL,
		Model:        p.Model,
		Kind:         p.EffectiveKind(),
		Enabled:      p.IsEnabled(),
		Capabilities: p.Capabilities,
	}
}

// Options configures a Client. The zero value uses DefaultTimeout.
type Options struct {
	// Timeout bounds one HTTP request. The caller's context can end sooner.
	// It is ignored when NoRequestTimeout is set.
	Timeout time.Duration
	// NoRequestTimeout disables the per-request timeout. The caller's context
	// is then the only bound on a reply. Local models can be very slow, so
	// suggestion jobs use this for local providers and rely on the Cancel
	// button. Connect and TLS handshake stay bounded by DialTimeout.
	NoRequestTimeout bool
	// DialTimeout bounds connect and TLS handshake. Zero uses
	// DefaultDialTimeout. It applies even when NoRequestTimeout is set.
	DialTimeout time.Duration
}

// normalizeBase makes sure the base URL ends with "/" so that paths join right.
func normalizeBase(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.HasSuffix(raw, "/") {
		raw += "/"
	}
	return raw
}

// validBase applies the same rules as config.ValidBaseURL: http(s), a host, and
// no userinfo, query or fragment.
func validBase(raw string) bool {
	return config.ValidBaseURL(raw) == nil
}

// keepCause returns the only causes that may leave this package with an *Error.
func keepCause(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}
