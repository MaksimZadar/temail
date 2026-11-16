package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
)

// capabilitiesTimeout bounds the /api/show probe. Ollama answers from its
// local model metadata; ten seconds is generous and keeps a dead endpoint from
// hanging the settings UI.
const capabilitiesTimeout = 10 * time.Second

// ErrModelCapabilitiesUnsupported means the provider does not report model
// capabilities. Only a local (Ollama) provider does; a cloud provider and an
// endpoint without /api/show return it. No request content or key is included.
var ErrModelCapabilitiesUnsupported = errors.New("this endpoint does not report model capabilities; only a local Ollama provider does")

// ThinkingSupport is what a model reports about its thinking control.
//
// Supported is false when the model has no thinking mode. Values lists the
// config.Thinking values the model accepts; an empty string means "provider
// default" and is listed when the model's default is thinking on. Default is
// the value the model uses when none is chosen (an empty string means the
// provider default).
type ThinkingSupport struct {
	Supported bool
	Values    []string
	Default   string
}

// ModelThinking asks a local Ollama endpoint what thinking control the model
// supports. It POSTs {"model": ...} to {baseURL without /v1}/api/show and reads
// the capabilities and thinking fields. No API key is sent.
//
// A non-local provider and an endpoint without /api/show return
// ErrModelCapabilitiesUnsupported, so the UI can hide the control. Other
// failures return *Error with a category, never a body or a key.
func (c *Client) ModelThinking(ctx context.Context) (ThinkingSupport, error) {
	if c.p.Kind != config.KindLocal {
		return ThinkingSupport{}, ErrModelCapabilitiesUnsupported
	}
	if c.p.Model == "" {
		return ThinkingSupport{}, ErrNoModel
	}
	showURL, err := showEndpoint(c.p.BaseURL)
	if err != nil {
		return ThinkingSupport{}, ErrModelCapabilitiesUnsupported
	}
	body, err := json.Marshal(map[string]string{"model": c.p.Model})
	if err != nil {
		return ThinkingSupport{}, ErrModelCapabilitiesUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, capabilitiesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, showURL, bytes.NewReader(body))
	if err != nil {
		return ThinkingSupport{}, ErrModelCapabilitiesUnsupported
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// wrapCall keeps only the category, never the transport error text.
		return ThinkingSupport{}, wrapCall(err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return ThinkingSupport{}, ErrModelCapabilitiesUnsupported
	default:
		return ThinkingSupport{}, statusError(resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ThinkingSupport{}, wrapCall(err)
	}
	return parseThinking(raw)
}

// statusError classifies a non-200 answer without keeping the body.
func statusError(status int) *Error {
	cat := CategoryOther
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		cat = CategoryUnauthorized
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		cat = CategoryTimeout
	}
	return &Error{Category: cat, Status: status}
}

// showEndpoint turns the OpenAI-compatible base URL into Ollama's /api/show
// URL: the "/v1" suffix is dropped and "/api/show" is appended.
func showEndpoint(base string) (string, error) {
	u, err := url.Parse(normalizeBase(base))
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/v1")
	u.Path = path + "/api/show"
	u.RawPath = ""
	return u.String(), nil
}

// showReply is the part of Ollama's /api/show answer this client reads.
type showReply struct {
	Capabilities []string `json:"capabilities"`
	Thinking     *struct {
		Values  []json.RawMessage `json:"values"`
		Default json.RawMessage   `json:"default"`
	} `json:"thinking"`
}

// parseThinking converts the /api/show answer into ThinkingSupport. A reply
// without a thinking object means the model does not think, which is not an
// error. Unknown values (for example "max", which config does not accept) are
// dropped.
func parseThinking(raw []byte) (ThinkingSupport, error) {
	var reply showReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return ThinkingSupport{}, &Error{Category: CategoryOther}
	}
	out := ThinkingSupport{Values: []string{}}
	if reply.Thinking == nil {
		return out, nil
	}
	seen := map[string]bool{}
	for _, v := range reply.Thinking.Values {
		if value, ok := thinkingValue(v); ok && !seen[value] {
			seen[value] = true
			out.Values = append(out.Values, value)
		}
	}
	if def, ok := thinkingValue(reply.Thinking.Default); ok {
		out.Default = def
	}
	out.Supported = len(out.Values) > 0
	if !out.Supported {
		out.Values = []string{}
	}
	return out, nil
}

// thinkingValue maps one raw thinking value to a config.Thinking value. A
// boolean model reports false/true: false is "none", true is the provider
// default (an empty string). A level model reports strings; only the values
// config accepts are kept.
func thinkingValue(raw json.RawMessage) (string, bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		if b {
			return "", true
		}
		return config.ThinkingNone, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return "", true
		}
		if config.ValidThinking(s) {
			return s, true
		}
		return "", false
	}
	return "", false
}
