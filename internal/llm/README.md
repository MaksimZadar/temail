# internal/llm

OpenAI-compatible LLM client for temail (phase P3). One client works for
OpenRouter, Ollama and any endpoint that speaks chat-completions. It knows HTTP
and JSON only: no mail, no UI. Callers pass prepared text.

Built on `github.com/openai/openai-go/v3` (v3.99.0). The base URL is set per
provider, and the SDK's own retries are off (see below).

## Privacy rules

- **Cloud gate.** A provider whose kind is `cloud` and that is not enabled is
  refused before anything is sent: `ErrCloudDisabled`. This covers `Complete`,
  `ListModels` and `Ping`. Anything that is not explicitly `local` counts as
  cloud. The gate is tested by counting requests on a fake server (zero).
- **Keys.** The key goes to the SDK as the bearer token. It is never stored
  elsewhere in this package, and it never appears in an error. `NewClient`
  always passes the key option, even when it is empty. So an `OPENAI_API_KEY` in
  the environment is not sent to a configured host, and a local endpoint gets no
  `Authorization` header at all.
- **Errors.** `*Error` has a fixed message per category. Its `Unwrap` returns the
  SDK error for `errors.Is`/`errors.As`. Do not print or log the wrapped cause:
  transport errors carry URLs, and API errors can echo server text.
- **No logging.** The package has no logger. Prompts, replies and keys are never
  written anywhere.
- **Plain text out.** Model output is data. Nothing here renders it or fetches a
  URL found in it.

## Modes (`Complete`)

`Capabilities` picks the mode, strongest first:

| Capability | Mode | Request | Reply read from |
|------------|------|---------|-----------------|
| `structuredOutput` | native | `response_format: json_schema`, `strict: true` | message content |
| `tools` | tools | one function `name = Schema.Name`, `tool_choice` forced to it | the matching tool call's arguments |
| neither | prompt | the schema JSON is added to the system prompt | message content |

Prompt mode and any mode whose reply is empty go through the same validation.

## Validation and retry

`Complete[T](ctx, client, Request, Schema[T])` returns the value that
`Schema.Decode` produced. `Decode` must reject anything that does not match.
`DecodeStrict[T]` does that: unknown fields, trailing data and wrong types fail.

- A failed decode gets **one** retry. The retry adds the decoder's error text to
  the prompt, so the decoder must not echo its input.
- A second failure returns the zero value and `*Error` with category
  `invalid_output`. There is no partial result.
- Transport and API errors are not retried by this package.
- `maxContext` is an estimate (4 characters per token). A request over it is
  refused with `ErrTooLarge` before it is sent.

## Errors and categories

`CategoryOf(err)` gives the category. `Ping` maps failures to:

| Category | When |
|----------|------|
| `ok` | nil |
| `unreachable` | a transport failure with no server answer |
| `unauthorized` | HTTP 401 or 403 |
| `model_not_found` | HTTP 404 (wrong model or wrong base path) |
| `structured_output_unsupported` | the reply is not the requested format, or HTTP 400/422 for `json_schema` or tools |
| `timeout` | the deadline passed, the server answered 408/504, or a net timeout |
| `other` | anything else, including cancellation by the caller |

Sentinel errors (`errors.Is`): `ErrCloudDisabled`, `ErrNoModel`,
`ErrModelListUnsupported`, `ErrInvalidOutput`, `ErrTooLarge`, `ErrBadSchema`.

## Timeouts and cancellation

`Options.Timeout` bounds each HTTP request (default 60 s). The caller's context
ends the call sooner, and a context deadline is reported as `timeout`. A
cancelled context returns `context.Canceled` in the chain, and its category is
`other`.

## ListModels

`GET {base}/models`, paged with the SDK's auto-pager. The result is sorted and
capped at 500 IDs. An endpoint with no models route (HTTP 404, 405 or 501) gives
an empty list and `ErrModelListUnsupported`. The caller then asks for the model
name.

## Tests

`llm_test.go` runs against an `httptest` OpenAI-compatible fake. It covers: each
structured mode (request shape, and the decoded value); retry then success; retry
then fail (exactly two requests, no partial value); no retry on API errors; the
models list, sorted, and the 404 degrade; every `Ping` category; unreachable (a
closed port); timeout; caller cancel; the cloud gate (zero requests); an enabled
cloud provider; the key sent but not in errors (also with a hostile echo); no
environment key leaking; the context limit; bad schema, model and base URL; and
`DecodeStrict`. Run with `-race`.
