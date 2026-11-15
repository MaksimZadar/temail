package llm

import (
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/MaksimZadar/temail/internal/config"
)

// approxCharsPerToken is the rough ratio used to compare a request with
// Capabilities.MaxContext. It is an estimate, not a tokenizer.
const approxCharsPerToken = 4

// Client talks to one provider. It is safe for concurrent use. A Client holds
// the API key only inside the SDK; it never returns or formats it.
type Client struct {
	p   Provider
	sdk openai.Client
}

// NewClient returns a client for p. apiKey may be empty (Ollama needs none).
// The key is always passed to the SDK, even when empty, so an OPENAI_API_KEY
// in the environment is never sent to a provider's host.
func NewClient(p Provider, apiKey string, opts Options) (*Client, error) {
	if !validBase(p.BaseURL) {
		return nil, fmt.Errorf("llm: provider %q: baseURL must be an http(s) URL with a host", p.Name)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	sdk := openai.NewClient(
		option.WithBaseURL(normalizeBase(p.BaseURL)),
		option.WithAPIKey(apiKey),
		// Retries are the caller's decision (one structured-output retry), not
		// the SDK's: a retried request could be sent to the provider twice.
		option.WithMaxRetries(0),
		option.WithRequestTimeout(timeout),
	)
	return &Client{p: p, sdk: sdk}, nil
}

// checkEnabled refuses a call to a disabled cloud provider before anything is
// sent. Anything that is not explicitly local counts as cloud.
func (c *Client) checkEnabled() error {
	if c.p.Kind != config.KindLocal && !c.p.Enabled {
		return ErrCloudDisabled
	}
	return nil
}

// gate is checkEnabled plus a model name, for calls that need one.
func (c *Client) gate() error {
	if err := c.checkEnabled(); err != nil {
		return err
	}
	if c.p.Model == "" {
		return ErrNoModel
	}
	return nil
}

// wrapCall turns an SDK error into *Error with a category. It keeps the cause
// for errors.Is, and the message stays fixed.
func wrapCall(err error) *Error {
	cat, status := classify(err)
	return &Error{Category: cat, Status: status, cause: err}
}
