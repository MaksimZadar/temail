package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/MaksimZadar/temail/internal/config"
)

// Response body limits. A chat reply is text; a model list is a few kilobytes
// per model. Anything larger fails closed rather than being buffered.
const (
	completionsMaxBody = 1 << 20 // 1 MiB
	modelsMaxBody      = 2 << 20 // 2 MiB
)

var (
	errBodyTooLarge = errors.New("response body exceeds the size limit")
	// errRedirectRefused means the server answered with a redirect. Redirects are
	// not followed, so the key is never sent to the target.
	errRedirectRefused = errors.New("redirects are not followed")
	errNotLoopback     = errors.New("local provider address is not loopback")
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
	dialTimeout := opts.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = DefaultDialTimeout
	}
	sdkOpts := []option.RequestOption{
		option.WithBaseURL(normalizeBase(p.BaseURL)),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(newHTTPClient(p.Kind, dialTimeout)),
		// Retries are the caller's decision (one structured-output retry), not
		// the SDK's: a retried request could be sent to the provider twice.
		option.WithMaxRetries(0),
	}
	if !opts.NoRequestTimeout {
		timeout := opts.Timeout
		if timeout <= 0 {
			timeout = DefaultTimeout
		}
		sdkOpts = append(sdkOpts, option.WithRequestTimeout(timeout))
	}
	sdk := openai.NewClient(sdkOpts...)
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

// wrapCall turns an SDK error into *Error with a category and status. The SDK
// error itself is dropped (see Error).
func wrapCall(err error) *Error {
	cat, status := classify(err)
	return &Error{Category: cat, Status: status, cause: keepCause(err)}
}

// newHTTPClient returns the HTTP client for one provider.
//
//   - Redirects are not followed. A 3xx reply is returned as is and is an
//     "other" error. Following it could send the bearer key to another host.
//   - Response bodies are capped (see completionsMaxBody, modelsMaxBody).
//   - A local provider dials loopback addresses only, and never through a
//     proxy. A cloud provider uses the default transport.
//   - Connect and TLS handshake are bounded independently of any request
//     timeout, so a dead endpoint fails quickly even when the reply itself has
//     no deadline.
func newHTTPClient(kind string, dialTimeout time.Duration) *http.Client {
	var base *http.Transport
	if kind == config.KindLocal {
		base = &http.Transport{
			Proxy:               nil,
			DialContext:         loopbackOnlyDial(&net.Dialer{Timeout: dialTimeout}),
			TLSHandshakeTimeout: dialTimeout,
		}
	} else {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &http.Client{
		Transport: &limitedTransport{base: base},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// loopbackOnlyDial resolves the host and refuses the connection if any address
// is not loopback. It then dials a loopback IP, not the name, so the address
// cannot change between the check and the connection (DNS rebinding).
func loopbackOnlyDial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
		}
		if len(ips) == 0 {
			return nil, errNotLoopback
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, errNotLoopback
			}
		}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

// limitedTransport applies the body limits and passes everything else through.
type limitedTransport struct {
	base http.RoundTripper
}

func (t *limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	limit := int64(completionsMaxBody)
	if strings.HasSuffix(req.URL.Path, "/models") {
		limit = modelsMaxBody
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errRedirectRefused
	}
	if resp.ContentLength > limit {
		_ = resp.Body.Close()
		return nil, errBodyTooLarge
	}
	resp.Body = &limitedBody{rc: resp.Body, left: limit}
	return resp, nil
}

// limitedBody returns errBodyTooLarge as soon as the body is longer than left.
// The caller then gets an error, never a truncated body.
type limitedBody struct {
	rc   io.ReadCloser
	left int64
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// Probe one byte: EOF means the body ended at the limit, data means it
		// is too large.
		var one [1]byte
		n, err := b.rc.Read(one[:])
		if n > 0 {
			return 0, errBodyTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }
