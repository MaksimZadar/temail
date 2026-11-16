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
- **Errors carry no request.** `*Error` holds a category and an HTTP status. It
  does not keep the SDK error, because that error holds the request (the bearer
  key in the headers) and the response body. `Unwrap` returns only
  `context.Canceled`, `context.DeadlineExceeded` or `ErrInvalidOutput`. `Format`
  and `GoString` print the same fixed text for every verb, so `%+v` and `%#v`
  cannot expand fields. The tests walk the whole chain under all these verbs and
  check for the key, the body and the SDK type.
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

## HTTP transport

Built by `newHTTPClient`:

- **No redirects.** `CheckRedirect` returns `http.ErrUseLastResponse`, and the
  transport refuses any 3xx reply (`errRedirectRefused`, category `other`). So the
  bearer key is never sent to a redirect target.
- **Body limits.** Replies are capped at 1 MiB and `/models` at 2 MiB. A longer
  body fails with `errBodyTooLarge` (category `other`), and no partial body is
  returned. The limit applies both when `Content-Length` says so and while the
  body is read.
- **Loopback only for `local`.** `loopbackOnlyDial` resolves the host. If any
  address is not loopback, it refuses. It then dials a loopback IP, not the name,
  so DNS cannot change the address between check and connect. A local provider
  uses no proxy. A `cloud` provider uses the default transport.
- **Base URLs** must be http(s) with a host and no userinfo, query or fragment
  (`config.ValidBaseURL`, the same check that config uses).

## Validation and retry

`Complete[T](ctx, client, Request, Schema[T])` returns the value that
`Schema.Decode` produced. `Decode` must reject anything that does not match.
`DecodeStrict[T]` does that: unknown fields, trailing data and wrong types fail.

- A failed decode gets **one** retry. The retry adds the decoder's error text to
  the prompt, so the decoder must not echo its input.
- **Custom `Decode` callbacks must not echo attacker-controlled text.** The
  correction message carries the decoder's error. A decoder that puts the reply,
  or any email text, into its error sends that text back to the model, and
  possibly into a second prompt. `DecodeStrict` keeps only the category of the
  JSON error. Anything written by hand must do the same. The error text is cut to
  300 characters before it goes into the prompt.
- **The correction counts against the context limit.** `maxContext` is checked
  again with the correction message included. A retry that would go over it is
  refused with `ErrTooLarge` and is not sent.
- A second failure returns the zero value and `*Error` with category
  `invalid_output`. There is no partial result.
- Transport and API errors are not retried by this package.
- `maxContext` is an estimate (4 characters per token). A request over it is
  refused with `ErrTooLarge` before it is sent.

## Thinking control

`Provider.Thinking` (or `Options.Thinking`, which overrides it) is sent as
`reasoning_effort` on every chat-completions call. The accepted values are
`none`, `low`, `medium` and `high` (see `config.ThinkingValues`); an empty value
leaves the field out of the request, so the provider's default applies and
providers that ignore the field see no change.

- On Ollama's OpenAI-compatible endpoint, `"none"` turns thinking off for a
  thinking-capable model, and `low`/`medium`/`high` select a level. Models
  without thinking support ignore the field.
- Measured on Ollama 0.40.16 with qwen3:8b: default thinking took 11.3 s and 306
  completion tokens, `"none"` took 0.27 s and 7 tokens and returned no
  `reasoning` field. `SuggestionService` uses `none` for local suggestion jobs
  by default (see `internal/suggest/README.md`).

`(*Client).ModelThinking` probes a **local** Ollama model: `POST
{baseURL without /v1}/api/show` with `{"model": ...}`, no API key, 10 s bound.
It reads `capabilities` and `thinking`, and maps boolean values to
`none`/`""` (off/on) and level strings to the config values, dropping values
config does not accept. A non-local provider or a 404/405/501 answer returns
`ErrModelCapabilitiesUnsupported`; a 200 without a thinking object reports
`Supported=false` without an error.

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
`ErrModelListUnsupported`, `ErrModelCapabilitiesUnsupported`,
`ErrInvalidOutput`, `ErrTooLarge`, `ErrBadSchema`.

## Timeouts and cancellation

`Options.Timeout` bounds each HTTP request (default 60 s). The caller's context
ends the call sooner, and a context deadline is reported as `timeout`. A
cancelled context returns `context.Canceled` in the chain, and its category is
`other`.

`Options.NoRequestTimeout` removes the per-request bound, so only the caller's
context limits the reply. Suggestion jobs use it for `local` providers: an
Ollama model can take minutes, and the job's Cancel button is the bound. The
connect and TLS handshake steps are still bounded by `Options.DialTimeout`
(default 10 s), so a dead endpoint fails fast even with no request timeout.
Response body caps are unchanged in both modes.

## ListModels

`GET {base}/models`, paged with the SDK's auto-pager. The result is sorted and
capped at 500 IDs. An endpoint with no models route (HTTP 404, 405 or 501) gives
an empty list and `ErrModelListUnsupported`. The caller then asks for the model
name.

## Tests

`hardening_test.go` covers the errors carrying no request (every verb, the whole
chain), the bearer key still being sent, the base-URL rules, redirects not
followed, oversized replies failing closed (chat and models), the loopback dialer,
the correction counted against the limit, and the correction text being cut.

`thinking_test.go` covers `reasoning_effort` being sent for `none`/`low`/`high`
and omitted by default, `Provider.Thinking` applying to calls, an unknown value
being refused, and the `/api/show` probe: path, no `Authorization`, the model in
the body, boolean and level values, a missing thinking object, a non-local
provider, a 404 endpoint and the `/v1` suffix removal.

`llm_test.go` runs against an `httptest` OpenAI-compatible fake. It covers: each
structured mode (request shape, and the decoded value); retry then success; retry
then fail (exactly two requests, no partial value); no retry on API errors; the
models list, sorted, and the 404 degrade; every `Ping` category; unreachable (a
closed port); timeout; caller cancel; the cloud gate (zero requests); an enabled
cloud provider; the key sent but not in errors (also with a hostile echo); no
environment key leaking; the context limit; bad schema, model and base URL; and
`DecodeStrict`. Run with `-race`.
