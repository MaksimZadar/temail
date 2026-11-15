package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// Request is the text sent for one structured call. Email content belongs in
// User and is sent as plain text. It is never interpreted as instructions by
// this package, but the model may still be steered by it, so callers must treat
// the output as untrusted.
type Request struct {
	System string
	User   string
}

// Schema describes one structured reply.
//
// Decode parses and validates a candidate reply and returns the typed value.
// It must reject anything that does not match the schema. Its error is shown
// to the model on the single retry, so it must not echo the input back. Use
// DecodeStrict for the common case.
type Schema[T any] struct {
	// Name is the schema name: 1 to 64 characters of A-Z, a-z, 0-9, "_" or "-".
	Name string
	// Description is optional and helps the model.
	Description string
	// JSONSchema is the JSON Schema object (a map that encodes to JSON).
	JSONSchema map[string]any
	Decode     func(raw []byte) (T, error)
}

var schemaNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s Schema[T]) check() error {
	if !schemaNameRE.MatchString(s.Name) {
		return fmt.Errorf("%w: name must be 1 to 64 letters, digits, '_' or '-'", ErrBadSchema)
	}
	if s.JSONSchema == nil {
		return fmt.Errorf("%w: JSONSchema is required", ErrBadSchema)
	}
	if s.Decode == nil {
		return fmt.Errorf("%w: Decode is required", ErrBadSchema)
	}
	return nil
}

// DecodeStrict decodes one JSON object into T. Unknown fields and trailing data
// are errors. The error text never contains the input.
func DecodeStrict[T any](raw []byte) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("reply is not valid JSON for the schema: %s", decodeReason(err))
	}
	if dec.More() {
		return v, fmt.Errorf("reply has data after the JSON object")
	}
	return v, nil
}

// decodeReason keeps only the category of a JSON error, not its text.
func decodeReason(err error) string {
	switch err.(type) {
	case *json.SyntaxError:
		return "syntax error"
	case *json.UnmarshalTypeError:
		return "wrong field type"
	default:
		if errors.Is(err, io.EOF) {
			return "empty reply"
		}
		return "unknown field or wrong shape"
	}
}

// mode is how the structured reply is requested.
type mode int

const (
	// modeNative sends response_format json_schema (strict).
	modeNative mode = iota
	// modeTools forces one function call whose arguments are the reply.
	modeTools
	// modePrompt asks for JSON in the system prompt and parses the text.
	modePrompt
)

// mode picks the strongest mode the provider's capabilities allow.
func (c *Client) mode() mode {
	switch {
	case c.p.Capabilities.StructuredOutput:
		return modeNative
	case c.p.Capabilities.Tools:
		return modeTools
	default:
		return modePrompt
	}
}

// Complete sends one structured call and returns the decoded, validated value.
// If the reply fails Decode, it retries once with the validation error added,
// then fails closed: it returns the zero value and a *Error with category
// invalid_output. Transport and API errors are not retried here.
func Complete[T any](ctx context.Context, c *Client, req Request, s Schema[T]) (T, error) {
	var zero T
	if err := c.gate(); err != nil {
		return zero, err
	}
	if err := s.check(); err != nil {
		return zero, err
	}
	schemaJSON, err := json.Marshal(s.JSONSchema)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", ErrBadSchema, err)
	}
	m := c.mode()
	system := req.System
	if m == modePrompt {
		system = joinLines(system, "Reply with only one JSON object that matches this JSON Schema. Do not add any other text.\n"+string(schemaJSON))
	}
	if err := c.checkSize(system, req.User, string(schemaJSON)); err != nil {
		return zero, err
	}

	msgs := []openai.ChatCompletionMessageParamUnion{}
	if system != "" {
		msgs = append(msgs, openai.SystemMessage(system))
	}
	msgs = append(msgs, openai.UserMessage(req.User))

	var reason error
	for attempt := 0; attempt < 2; attempt++ {
		send := msgs
		if attempt == 1 {
			// The decoder's text goes into the prompt. Keep it short, and count it
			// against the context limit like the rest of the request.
			correction := fmt.Sprintf("Your previous reply was not valid: %s. Reply again with only one JSON object that matches the schema.", truncate(reason.Error(), maxReasonChars))
			if err := c.checkSize(system, req.User, string(schemaJSON), correction); err != nil {
				return zero, err
			}
			send = append(slices.Clone(msgs), openai.UserMessage(correction))
		}
		raw, err := c.send(ctx, m, s.Name, s.Description, s.JSONSchema, send)
		if err != nil {
			return zero, err
		}
		v, derr := s.Decode(raw)
		if derr == nil {
			return v, nil
		}
		reason = derr
	}
	// The decoder's text is not kept: it could echo the reply. The sentinel is.
	return zero, &Error{Category: CategoryInvalidOutput, cause: ErrInvalidOutput}
}

// send makes one chat-completions request and returns the candidate reply bytes.
// An empty reply (no choices, no matching tool call) returns nil, so Decode
// rejects it and the retry path runs.
func (c *Client) send(ctx context.Context, m mode, name, desc string, schema map[string]any,
	msgs []openai.ChatCompletionMessageParamUnion) ([]byte, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.p.Model),
		Messages: msgs,
	}
	switch m {
	case modeNative:
		js := shared.ResponseFormatJSONSchemaJSONSchemaParam{
			Name:   name,
			Schema: schema,
			Strict: param.NewOpt(true),
		}
		if desc != "" {
			js.Description = param.NewOpt(desc)
		}
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{JSONSchema: js},
		}
	case modeTools:
		fn := shared.FunctionDefinitionParam{
			Name:       name,
			Parameters: shared.FunctionParameters(schema),
			Strict:     param.NewOpt(true),
		}
		if desc != "" {
			fn.Description = param.NewOpt(desc)
		}
		params.Tools = []openai.ChatCompletionToolUnionParam{{
			OfFunction: &openai.ChatCompletionFunctionToolParam{Function: fn},
		}}
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{
			OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
				Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: name},
			},
		}
	}

	resp, err := c.sdk.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, wrapCall(err)
	}
	if len(resp.Choices) == 0 {
		return nil, nil
	}
	msg := resp.Choices[0].Message
	if m == modeTools {
		for _, tc := range msg.ToolCalls {
			if tc.Type == "function" && tc.Function.Name == name {
				return []byte(tc.Function.Arguments), nil
			}
		}
		return nil, nil
	}
	return []byte(msg.Content), nil
}

// maxReasonChars bounds the decoder error text that is shown to the model.
const maxReasonChars = 300

// checkSize refuses a request whose rough token estimate exceeds MaxContext.
// Every part that is sent counts: the system and user text, the schema, and on
// the retry, the correction message.
func (c *Client) checkSize(parts ...string) error {
	limit := c.p.Capabilities.MaxContext
	if limit <= 0 {
		return nil
	}
	chars := 0
	for _, p := range parts {
		chars += len(p)
	}
	if chars/approxCharsPerToken > limit {
		return ErrTooLarge
	}
	return nil
}

// truncate shortens s to at most n runes.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func joinLines(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}
