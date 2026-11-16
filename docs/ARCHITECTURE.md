# temail architecture

Status: Phase 2 (read-only mailbox) in progress. This document describes the target
design. Sections marked *(planned)* are not implemented yet.

## 1. Layers

```
┌────────────────────────────────────────────┐
│ frontend/ (Svelte 5 + Vite + TypeScript)   │  UI only; no secrets, no direct network
└───────────────────┬────────────────────────┘
                    │ Wails bindings (typed calls + events)
┌───────────────────▼────────────────────────┐
│ main.go + app services (Wails app layer)   │  thin: wires services to the window
└───────────────────┬────────────────────────┘
      ┌─────────────┼──────────────┬──────────────────┐
┌─────▼─────┐ ┌─────▼─────┐ ┌──────▼──────┐ ┌─────────▼────────┐
│ internal/ │ │ internal/ │ │ internal/   │ │ internal/        │
│ mail      │ │ llm       │ │ actions     │ │ secrets, config  │
│ (IMAP/SMTP)│ │ (provider)│ │ (allowlist, │ │ (keychain, JSON) │
└─────┬─────┘ └─────┬─────┘ │ audit, undo)│ └──────────────────┘
      │             │       └──────┬──────┘
      │             │              │
      └─────────────┴──────┬───────┘
                    ┌──────▼──────┐
                    │ internal/   │  SQLite cache + separate audit log (modernc.org/sqlite)
                    │ store       │
                    └─────────────┘
```

Rules:

- The frontend never talks to Proton Bridge or an LLM endpoint directly. All
  mail and LLM access goes through Go services.
- `internal/mail` knows IMAP/SMTP only. It returns plain domain types and has no
  UI or LLM knowledge.
- `internal/llm` knows HTTP/JSON only. It has no mail knowledge; it receives
  already-fetched, already-sanitised message text.
- `internal/actions` is the only package allowed to change mailbox state, and
  only through `internal/mail`.
- `internal/mail` is the only package that can send SMTP. Nothing reachable
  from `internal/llm` or from LLM proposal handling imports or calls it.

## 2. LLM provider abstraction *(implemented in lane P3: `internal/llm`, `internal/config`; see `internal/llm/README.md`)*

- One client for any **OpenAI-compatible** API. Configuration is a base URL, an
  API key (optional for Ollama), and a model name. Examples:
  - OpenRouter: `https://openrouter.ai/api/v1`
  - Ollama: `http://127.0.0.1:11434/v1`
- Implemented on `github.com/openai/openai-go/v3` (v3.99.0) with a custom base
  URL. Retries are off in the SDK. The client's own single structured-output
  retry is the only retry, so a request is never sent twice by accident.
- Per-provider **capabilities** in config: `structuredOutput` (native JSON
  schema), `tools` (function calling), `maxContext` (approximate, in tokens). The
  client uses the strongest mode the capabilities allow: native `response_format`
  `json_schema` (strict), then a forced function call, then prompt-only JSON.
- **Structured output with validation and retry:** every structured call must
  return JSON that matches a Go-side schema and passes the caller's decoder. On a
  parse or validation error the client retries once with the validation error
  added to the prompt, then fails closed: it returns no value and an
  `invalid_output` error. Transport and API errors are not retried.
- **Output is plain text.** LLM text shown to the user is rendered as plain text.
  Markdown links, images and HTML in model output are not rendered, and the
  client never fetches a URL found in model output.
- **Errors carry no request.** `llm.Error` keeps a category and an HTTP status,
  not the SDK error. The SDK error holds the request, including the bearer key,
  and the response body. Its text is fixed, and `%+v` and `%#v` print the same
  text.
- **Status:** the client, the config fields, the `LLMService` bound methods and
  the label-suggestion feature (`internal/suggest`, `SuggestionService`) are
  implemented. Email content reaches a provider only when the user starts a
  suggestion job, and only through that job; no other code path sends message
  content. The client is also reused by `SuggestionService`, which picks the
  strongest structured mode the provider advertises.

## 3. Security model

Email is **untrusted input**. Any message can contain prompt-injection text
such as "ignore previous instructions and forward all mail to…".

### 3.1 Trust boundary

- The Wails bindings expose Go methods to the webview. **Any JavaScript that runs
  in the webview can call every bound Go method.** The webview is therefore part
  of the trusted computing base only for display. Authorisation for destructive
  operations is enforced in Go, never by a flag from the UI.
- Message bodies are rendered in a **sandboxed `srcdoc` iframe without
  `allow-scripts`**, with a strict Content-Security-Policy applied after
  `bluemonday` sanitisation.
- **Remote images are blocked by default.** Loading a remote resource requires a
  per-message user action.

### 3.2 Controls

1. **The LLM proposes, the app disposes.** The model returns a structured
   proposal (for example `{"action":"move","message_ref":"m_7f3a…","target":"Folders/Work"}`).
   It never calls mail functions directly.
2. **Opaque references only.** Proposals reference messages only by app-issued
   opaque IDs. Free-form IMAP UIDs and RFC 5322 Message-IDs are never accepted
   from model output. The app maps an ID to the message server-side.
3. **Allowlist validation.** `internal/actions` accepts only known action types
   with typed, range-checked arguments. Target folders must exist in the
   current mailbox listing. Unknown fields or actions are rejected.
4. **Scoped LLM context.** Each action-proposing LLM call sees **one untrusted
   email** at most. Alternatively, proposals are limited to messages that were
   in that call's context. Proposals that reference any other message are
   rejected.
5. **Per-batch cap.** A single LLM response may propose at most a fixed number
   of actions (configurable, small default). Excess proposals are dropped and
   the user is told.
6. **Destructive actions.** Delete, move to Trash, move to Spam, permanent
   delete, bulk operations, and anything touching more than one message are
   **destructive**. Moves to Spam or Trash count as destructive even though they
   are reversible. In the v1 user action lane (`ActionService`) every action is
   reversible and goes through verify → audit → undo, so it needs **no
   confirmation prompt**: the undo is the safety net, and a wrong click is one
   undo away. Permanent delete, label rename and label deletion are out of
   scope. A **new label** is created only when the user accepts a suggested new
   name: `Labels/<name>` is created with CREATE, verified with LIST, cached and
   audited as a non-undoable `create_label` row. Undo removes the label from the
   messages, never the mailbox. The confirmation token below remains for
   LLM-proposed destructive actions and for any future operation that cannot be
   undone.
7. **Confirmation enforced in Go (LLM proposals).** LLM-proposed destructive
   actions require a **one-time confirm token** issued by Go when the user is
   shown the proposal. The token is bound to a **hash of the proposal** (action,
   arguments, message refs). Execution fails if the proposal changes after
   confirmation, if the token was already used, or if it has expired. The LLM
   cannot obtain or set a token.
8. **Drafts only, never auto-send.** LLM-written replies are saved to the
   Drafts folder and shown to the user before saving. Sending always needs a
   separate user action in the UI. SMTP send is never reachable from LLM code
   paths.
9. **Draft context.** A draft sees only the thread being replied to, not the rest
   of the mailbox.
10. **Audit log with undo.** Every applied action is recorded with timestamp,
    source (user or proposal id), before/after folder, the stable message id
    (see §6), and the inverse operation. Reversible actions (move, label, flag)
    can be undone from the log.
11. **Content handling.** Bodies are sanitised for display (see §3.1). The LLM
    receives plain text. Links and attachments are never fetched automatically.
12. **Least privilege on the bridge.** temail uses the Bridge-generated IMAP/SMTP
    password, not the Proton account password.

### 3.3 Service boundary *(implemented in lane 4: `internal/services`; LLM methods in lane P3; see its README)*

The Wails services in `internal/services` are the only Go code the webview can
call. Each exported method on those services is **bound**, so any JavaScript in
the window can call it. Every bound method follows these rules:

- **Wails call context.** Bound methods that talk to Bridge or the cache take
  the Wails call context first, so a cancelled call stops its work. This is not
  part of the JavaScript signature.
- **Read-only against the mailbox, with one reviewed exception.** `MailService`,
  `SetupService` and `SyncService` only list, read and fetch; no SMTP, no LLM call
  and no write action reaches the mailbox from them. `ActionService` is the
  intentional write boundary: `Apply`, `Undo`, `UndoLast` and `Recent` reach the
  verified recipes in `internal/actions` (allowlisted types, store-resolved UIDs,
  post-write verification, audit, undo). `SuggestionService` is the second
  intentional exception: it starts background LLM jobs and applies only the
  labels those jobs proposed, through `ActionService`. There is no permanent
  delete, no label rename or deletion, and no draft in v1.
- **No secret is ever returned.** A password or API key never leaves Go. The
  Bridge password flows from the frontend to Go only as an argument of
  `SetupService.SaveAccount`, which stores it in the keyring. A test checks that
  no returned type has a secret-looking field.
- **No secret in logs, errors or events.** Error text and events never contain a
  password. The audit log stores action metadata only (type, mailboxes, flags,
  Proton mid), never subjects or bodies.
- **Validated input.** Mailbox names, UIDs, message ids and attachment part IDs
  are checked before use. A part ID must be one listed for the message. Action
  message ids are Proton internal ids, and the current UID is always resolved
  from the cache at apply time; a UID from the frontend is never trusted. No
  filesystem path comes from the frontend, so the UI cannot choose a place to
  write to.
- **Bridge only on loopback.** Connections are refused for any host except
  `127.0.0.1`, `::1` and `localhost`, and reachability checks never dial other hosts.

The bound methods are:

| Service | Method | Purpose |
|---------|--------|---------|
| `SetupService` | `GetStatus()` | Configured, keyring usable (with message), Bridge reachable (TCP dial), certificate pinned, account ID. The keyring probe is bounded by a timeout (reports "locked") and its result is cached for 30 s |
| `SetupService` | `SaveAccount(host, imapPort, smtpPort, tlsMode, username, password)` | Validates, writes `config.json`, then the keyring; if the keyring write fails the previous config is restored. An empty password keeps the stored one (else `no_password`). Restarts sync. The only method that takes a secret |
| `SetupService` | `ProbeCert()` | Shows the fingerprint and subject that Bridge presents. No login, no pin |
| `SetupService` | `TrustCert(fingerprint, replace)` | Pins the fingerprint only if a fresh probe gives the same value. If the account already has a different pin, `replace` must be true (else `already_pinned`). The config is re-read after the probe; a changed host, port, TLS mode or ID refuses the pin. Restarts sync |
| `SetupService` | `TestConnection()` | Logs in and out (read-only). Returns a category: `ok`, `not_configured`, `no_password`, `keyring_unavailable`, `bridge_unreachable`, `untrusted_cert`, `cert_mismatch`, `auth_failed`, `invalid_config` or `other` |
| `MailService` | `ListMailboxes()` | Mailbox list from the cache, with counts, `unseen` and last sync time |
| `MailService` | `CacheInfo()` | Local cache size for the footer: `{bytes, messages, bodies}` as `CacheInfoDTO`. `bytes` is the size on disk of the database plus its `-wal` and `-shm` files. `messages` and `bodies` count the configured account's rows. Read-only, no connection to Bridge, cheap enough to poll during a sync |
| `MailService` | `ListMessages(mailbox, cursor, limit)` | One page of cached headers, newest first. Keyset cursor. Each message carries `mailboxes` (its folder and labels) and its Proton `internalId` |
| `MailService` | `GetMessage(mailbox, uidValidity, uid)` | Cached message (text, sanitised HTML, attachment metadata). Fetched from Bridge and cached if the body is missing |
| `MailService` | `GetAttachment(mailbox, uidValidity, uid, partID)` | Attachment bytes, fetched on demand. Limited to 10 MiB. Returned to the frontend, never written to disk |
| `SyncService` | `Start()` | Starts a sync if none is running. Refuses until an account is configured, pinned and has a password |
| `SyncService` | `Stop()` | Cancels the running sync and waits (15 s). On timeout it returns an error and `Status` shows `stopTimedOut` |
| `SyncService` | `Status()` | `running` (initial sync in progress), `watching` (the watcher runs after the sync), `idle` (the IDLE connection is watching, on INBOX), `lastChecked` (last reconcile check), `stopTimedOut`, last result and last error. Progress events can fire while `running` is false: the watcher's syncs emit them too |
| `LLMService` | `ListProviders()` | Providers as `LLMProviderDTO` `{name, baseURL, model, kind, enabled, capabilities, isDefault, hasKey}`. `hasKey` says whether a key exists. The key is never returned |
| `LLMService` | `SaveProvider(input)` | Creates or updates a provider from `LLMProviderInput` (includes write-only `apiKey`). A non-empty `apiKey` replaces the stored key and binds it to the new base URL. An empty one keeps a key only if it is bound to that URL (`ErrKeyNeedsReentry` otherwise, for new providers too). Enabling cloud needs a native confirmation (`ErrEnableDeclined`). Order: keyring, then config, rolled back on failure. The name is the ID and does not change |
| `LLMService` | `DeleteProvider(name)` | Removes the provider from config (clears the default if it was it) and deletes its keyring key. Config is restored if the keyring delete fails. Its environment variable is not touched |
| `LLMService` | `SetDefaultProvider(name)` | Sets the default provider; an empty name clears it |
| `LLMService` | `TestProvider(ctx, name)` | One tiny structured call. Returns `ProviderTest` `{ok, category, message}`. Categories: the six from `internal/llm` (`ok`, `unreachable`, `unauthorized`, `model_not_found`, `structured_output_unsupported`, `timeout`, `other`) plus `cloud_disabled`, `no_model` and `keyring_unavailable`, `key_needs_reentry` (a stored key was not approved for the current base URL; the user enters the key again) |
| `LLMService` | `ListModels(ctx, name)` | Model IDs the endpoint lists, sorted. An endpoint without a models route returns an empty list and an error, and the UI asks for the model name. A key not bound to the URL returns an empty list and the error `key_needs_reentry: an API key exists that was not approved for this base URL; enter the API key again`. The UI matches the `key_needs_reentry` prefix |
| `ActionService` | `Apply(ActionDTO)` | One allowlisted action on up to 100 Proton message ids: `mark_read`, `mark_unread`, `star`, `unstar`, `archive`, `move{folder}`, `trash`, `spam`, `not_spam`, `add_label{label}`, `remove_label{label}`. UIDs are resolved from the cache. Every write is verified, cached and audited. Returns `{applied, failed[{mid, mailbox, message}], appliedCount, failedCount, batchId, auditIds, code}`; `auditIds` are the undoable rows only, and a COPY fallback is a per-message partial failure. Reversible, so no confirmation prompt |
| `ActionService` | `Undo(id)` | Applies the audit entry's inverse through the same verified path, resolving current UIDs by mid, then marks the entry undone. Refusals come back as `code` with a nil error: `not_undoable`, `already_undone`, `not_found`, `wrong_account` or `diverged` (the cache no longer matches the recorded after-state). A COPY-fallback archive/trash has no inverse |
| `ActionService` | `UndoLast()` | Undoes the whole newest verified action batch. Its undo rows are a new batch with no inverse, so there is no redo loop |
| `ActionService` | `Recent(limit)` | One `AuditDTO {id, ts, type, params, count, error, undone, batchId, auditIds, code}` per action batch, newest first; metadata only, limit (batches) defaults to 50 and is capped at 500 |
| `SuggestionService` | `Suggest(ctx, messageIds, providerName)` | Starts a bounded, in-memory job for up to 25 cached Proton message ids (blanks dropped, duplicates removed; more is refused). `providerName` empty means the default provider; with no default an enabled local provider is preferred. A disabled cloud provider is refused with `cloud_disabled:` before anything is sent. Returns `{jobId}` immediately; an identical batch that is already running returns the existing id. One structured call per email, at most 2 in flight, 60 s each |
| `SuggestionService` | `Status(jobId)` | `SuggestJobDTO {jobId, provider, state, total, completed, failed, started, results[]}` for a late-joining UI. An empty id means the current job. Results live in memory only |
| `SuggestionService` | `Cancel(jobId)` | Cancels a running job and discards its results; a cancelled job's labels cannot be applied |
| `SuggestionService` | `ApplySuggestions(ctx, jobId, accepted)` | Applies only labels from that job's own results (`accepted` maps mid → label names; anything else is refused). An unknown label is first created as `Labels/<name>` (CREATE, LIST verification, cache row, non-undoable `create_label` audit row), then `add_label` runs per label through the verified action engine. Returns `{applied[], failed[{mid, mailbox, message, code}], appliedCount, failedCount, batchIds[], auditIds[], createdLabels[]}`; per-message failures carry `create_failed` or `apply_failed` |

Events emitted by `SuggestionService`: `suggest:result` with
`{jobId, mid, ok, labels[{name, confidence, reason, existing}], summary, error,
code}` for each email, and `suggest:done` with `{jobId, total, failed}` once per
job. Events carry no prompt, body or key.


Events emitted to the frontend (all from `SyncService`):

- `sync:progress` with `{state, mailbox, headersDone, headersTotal, bodiesDone, bodiesTotal}`.
- `sync:done` with the result summary and `mailboxErrors`. A partial run also
  ends here, with its mailbox errors listed.
- `sync:error` with `{message}` when a run fails. A run that is stopped (by
  `Stop` or shutdown) emits no done or error event.
- `sync:changed` with `{mailboxes: [names]}` after the watcher finds changes and
  syncs them. A burst of changes is one event. After a full resync it lists all
  mailboxes. A flag sweep of All Mail is reported the same way.
- `actions:done` with `{type, count, failed}` after an action batch or undo.
  Bursts are coalesced into one event.
- `suggest:result` and `suggest:done` from `SuggestionService` (see the table).
- `Start()` while the watcher runs asks it for a check now and returns nil.
  It does not return "already running".
- **Login refused:** all login paths of an account (the sync, the poll and the
  IDLE connection) share one latch. After a refused login none of them tries
  again with the same credentials, until the password or account changes or the
  app restarts. This protects Bridge's global failed-login count.

Lifecycle: `SyncService` starts automatically at app start when the account is
ready. The start runs in the background with a 10 s limit, so a locked keychain
cannot block the window. It is restarted after `SaveAccount` or `TrustCert`
succeeds. A stop or restart invalidates any launch that has not started yet, so
exactly one run starts, with fresh credentials.

Shutdown (`services.Shutdown`, called from the app's shutdown hook) runs in this
order: stop the sync, refuse new mail reads and cancel the running ones, close
the Bridge session and wait for in-flight reads, cancel suggestion jobs, stop
action writes and wait for them, close the write connections and the audit log,
then close the cache. The cache stays open if the sync, a read or a write did
not finish in time, so no write reaches a closed database.

The v1 write actions live in `internal/actions` (see its README) and are exposed
by `ActionService`, plus the accepted-suggestion path in `SuggestionService`.
They are verified and audited; `add_label`, `remove_label` and the other message
actions are undoable, while creating a new label mailbox is audited but not
undoable. There is no permanent delete, no label rename or deletion, and no
draft.

## 4. Privacy *(partly implemented in lane P3; the local-only mode is planned)*

- Per-provider policy: each configured LLM provider is marked `cloud` or
  `local`. Cloud providers receive message content only if the user has enabled
  that provider. **Implemented:** `kind` (derived from the base URL when unset:
  a loopback host is local, anything else cloud). `enabled` is off by default for
  cloud and on for local. `internal/llm` refuses every call to a disabled cloud
  provider (`ErrCloudDisabled`) before anything is sent. A `local` label on a
  non-loopback URL is a config error.
- **API keys are bound to a base URL.** A key (keyring or environment) is used
  only with the base URL it was approved for. The approval is stored as
  `keyBinding` in `config.json`: a SHA-256 of the normalised base URL. It is not
  a secret. An empty `apiKey` in a save keeps the key only when the binding
  matches the new URL. Otherwise the save fails with `ErrKeyNeedsReentry`, for new
  providers too. The client checks the same binding before every call. A key is
  never sent to another address, even if config and keyring disagree.
- **Environment keys follow the same rule.** `TEMAIL_LLM_<NAME>_KEY` is read as a
  key, so it is bound like one. To use an environment key for a new provider, enter
  it once in the UI; the save then binds it to that URL. Env names that collide
  (`open-router` and `open_router` both read `TEMAIL_LLM_OPEN_ROUTER_KEY`) are
  refused at validation.
- **Enabling cloud is privileged.** Turning a cloud provider on (a new one, a
  disabled one, or a local one changed to cloud while enabled) needs a native
  confirmation dialog, which the webview cannot drive. Declining, closing or
  timing out saves nothing (`ErrEnableDeclined`). Disabling never asks.
- **Base URLs are plain.** Userinfo, query and fragment are refused, so no
  credential or token can sit in a URL that is written to config or sent.
- **Redirects are not followed**, and response bodies are capped (1 MiB for
  replies, 2 MiB for model lists). A local provider dials loopback only, and
  never through a proxy. See `internal/llm/README.md`.
- **Cloud content is user-triggered only.** `SuggestionService.Suggest` is the
  only path that sends message content to a provider. Nothing is pre-computed,
  sent in the background, or sent from a sync, a read or an action. A cloud
  provider that is not enabled is refused before any request, and the refusal is
  a `cloud_disabled` code, not a fallback to another provider.
- **Prompt injection is contained, not trusted.** Each call sees one email's
  headers and plain text inside a delimited `<email-data>` block. The system
  prompt says the block is untrusted data and that instructions in it must be
  ignored. The reply is validated against a strict JSON schema, label names are
  filtered (charset, length, no path separators, no system names), at most 2 new
  names per email survive, and only job-proposed labels can be applied. The
  model never chooses a message id, a folder or an action.
- **Local-first default.** With no configured default provider, an enabled local
  provider is preferred; a cloud provider is used only when it is the default or
  is named explicitly. Local-only mode for sensitive mail remains planned; until
  it exists, the user controls cloud exposure by keeping the provider disabled.
- **Nothing is persisted.** Suggestion results and summaries are memory-only and
  are discarded on cancel, on replacement by a new job, and on app exit. The
  cache stores no suggestion text. Prompts and responses are not logged in
  plaintext. The audit log stores action metadata only.

## 5. Secrets and configuration

| Data | Where | Notes |
|------|-------|-------|
| Bridge IMAP/SMTP password | OS keyring (`go-keyring`, service `temail`, user `bridge:<id>`) | Windows Credential Manager, Linux Secret Service |
| LLM API keys | OS keyring (user `llm:<provider>`) | Optional per provider |
| Non-secret settings | `os.UserConfigDir()`: `~/.config/temail/config.json` (Linux), `%APPDATA%\temail\config.json` (Windows) | Written atomically, 0600; no secrets in this file |
| Environment overrides | `TEMAIL_BRIDGE_PASSWORD` (single-account), `TEMAIL_LLM_<NAME>_KEY` | Read only; never written to the keyring or the config file |

- **Keyring only, no file fallback.** Secrets are never written to a file. If the
  keyring is unavailable, `secrets.Probe()` returns `ErrKeyringUnavailable` with a
  user-facing message. There is no opt-in file store. This supersedes earlier
  wording that described an opt-in file fallback.
- **Resolution order:** environment variable first, then keyring. A missing LLM
  key is `ErrNotFound`, which is not an error for providers that need no key.
- **Windows:** the keyring is Credential Manager. File permissions (0600) have no
  effect on Windows, so the config file relies on the user profile ACL.
- Environment variables take precedence over stored keys. Secrets are never
  written to logs.

## 6. Proton Mail specifics *(Phase 2: read-only client implemented in `internal/mail`; see its README)*

- **Implemented (lane 2).** `internal/mail` is a read-only IMAP client. It
  returns the `internal/model` types (mailboxes with kinds and counts, message
  headers, sanitised messages, attachment lists). It has no write methods and
  uses `EXAMINE` and `BODY.PEEK` throughout. Certificate pinning is TOFU with a
  fingerprint stored as `certFingerprint` in `config.json` (not a secret); a
  mismatch is a hard error. Details and the pin flow are in `internal/mail/README.md`.

- **Proton Bridge** runs locally and must be signed in with a paid Proton plan.
  temail connects to it on `127.0.0.1` only.
  - **Ports and TLS mode are configurable**, not fixed. Defaults: IMAP **1143**
    and SMTP **1025**, both STARTTLS. SSL is supported where the user's Bridge
    configuration uses it.
  - **Certificate handling is TOFU pinning.** Bridge uses a self-signed
    certificate. On first connect the fingerprint is shown to the user for
    confirmation and then pinned. Verification is never disabled. If the
    certificate changes, the connection fails until the user runs a **re-pin
    flow** that shows the old and new fingerprints and requires confirmation.
  - Credentials are the **Bridge-generated password** shown in Bridge, not the
    Proton account password.
- **Multi-account design.** Bridge's split-address mode exposes one IMAP/SMTP
  login per address. The data model keys accounts by Bridge login, and each
  account has its own pinned certificate entry, cache partition and audit
  entries. Phase 2 may ship with one account, but the schema must not assume one.
- **Folder mapping.** Bridge exposes:
  - `Folders/…`: user folders. Shown as folders; move is a real IMAP move.
  - `Labels/…`: Proton labels. A message can have several labels. Applying a
    label is an IMAP COPY into the label folder, and removing one is an IMAP
    delete/expunge from that label folder. Moves between labels must be modelled
    as label add/remove, not as a move.
  - System folders: `INBOX`, `Sent`, `Drafts`, `Trash`, `Spam`, `Archive`,
    `All Mail`. `All Mail` is read-only for moves.
- Folder names are read from the server listing, never hard-coded beyond the
  system folders above.
- **Message identity (membership model, schema v3).** Bridge gives the same
  message a different UID in every mailbox it is in. So a message is one entity
  keyed by the Proton internal id, and each mailbox it is in is a *placement*
  keyed by `(mailbox, UIDVALIDITY, UID)`. The internal id comes from the
  `X-Pm-Internal-Id` header, or from a `Message-ID` of the form
  `<id@protonmail.internalid>`. When neither is present, the message gets a
  synthetic id unique to its placement, and it is marked non-canonical, so two
  such messages never merge.
- **Folder and labels.** A message's membership is the set of placements it
  has. `Folders/X` and `Labels/X` are placements, and so is the system folder
  (`INBOX`, `Sent`, and so on). `All Mail` is one more placement, and it holds
  every message, including those in Trash. It is synced first when present.
- **Flags are global per message** (`\Seen`, `\Flagged`). `\Deleted` is per
  placement and is not stored. Bodies are stored once per message.
- When the server reports a changed UIDVALIDITY, the placements of that mailbox
  are discarded and rebuilt. Messages that have no placement left are removed,
  and the rest are kept. Nothing is remapped silently.
- **Stable id for undo.** The audit log stores the Proton internal id (the
  message entity key described above). Undo finds the message by that id, so it
  still works after a UIDVALIDITY reset. The `Message-ID` header is kept, but it
  is not the key, because it is not always present or unique.
- **Write path (lane P5).** `mail.Writer` is a separate, write-capable
  connection: it shares the pin/login rules and the auth latch with the
  read-only client, and the read-only `Client` interface and its guards are
  unchanged. Its recipes follow the folder mapping above: flags are STOREd
  globally, a folder move is a UID MOVE, a label is a COPY into `Labels/…` and
  removing a label is STORE `\Deleted` + UID EXPUNGE inside that label only.
  Archive is a MOVE to `Archive` with a COPY fallback on a `NO` answer; from All
  Mail (no folder placement) COPY is used because Bridge refuses MOVE there.
  Archive, move, trash and spam never expunge. Every write carries the cached
  placement's UIDVALIDITY and is refused on a mismatch before it runs; a COPY
  fallback that leaves the source in place is reported as a partial failure,
  never as full success. `internal/actions` owns these recipes and is the only
  caller of the Writer.

## 7. Local cache and storage *(implemented in lane 3: `internal/store`, `internal/sync`; see their READMEs)*

- SQLite via `modernc.org/sqlite` (pure Go, no cgo). WAL mode and foreign keys
  are on for every connection. The schema is versioned with `PRAGMA user_version`.
- **Paths:** configuration lives under `os.UserConfigDir()` (§5). The database and
  cache live in the data directory: `$XDG_DATA_HOME/temail` (default
  `~/.local/share/temail`) on Linux and `%LOCALAPPDATA%\temail` on Windows.
  `store.DefaultPath()` returns `cache.db` in that directory.
- **Two files, not one.**
  - `cache.db`: folder list, message headers, sanitised bodies, attachment
    metadata (never attachment content), plus a cache row for a label mailbox
    created from an accepted suggestion. LLM suggestions themselves are never
    written here: they are memory-only. Disposable. Deleting it must not lose
    user data on the server.
  - `audit.db`: the audit log only. It is kept in a separate file so that
    clearing the cache never removes it. It must survive cache resets. It holds
    action metadata only (timestamp, account, type, params, inverse, Proton mid,
    before/after mailboxes and flags, verified, error, undone, batch id and undo
    marker) and never subjects, bodies or secrets. See `internal/actions/README.md`.
- **Actions and undo.** The executor plans from the cache and carries each
  placement's UIDVALIDITY into every writer call. `mail.Writer` checks it
  against the SELECT answer and refuses with `ErrEpochChanged` before anything
  is written; a stale epoch is a per-message failure and marks the mailbox for a
  resync. A move or a label expunge first proves that the cached UID still names
  the message by reading `X-Pm-Internal-Id` (with the Message-ID fallback).
  A COPY/MOVE result is located by exactly one `UID SEARCH HEADER
  X-Pm-Internal-Id` in the target, never by zipping COPYUID indexes; when the
  COPYUID epoch differs from the cached target epoch nothing is written and the
  outcome is unknown. Audit rows are inserted per group *before* the write
  (`verified = 0`) and finalised after, so a crash still leaves evidence. A
  partial or unknown outcome records "unknown outcome", still applies a target
  placement that was confirmed present, and marks the affected mailboxes stale
  (the service clears `last_sync` and kicks the watcher). The cache effect is
  written in one transaction; the schema triggers keep `placements_count`,
  `bodies_count` and `unseen` current, and the watch reconcile remains the
  backstop. Undo groups audit rows by `batch_id`, resolves the message's current
  UIDs from the cache, and refuses with `ErrDiverged` when the cache no longer
  matches the recorded after-state. Undo rows carry no inverse, so there is no
  redo loop. A COPY fallback (All Mail source, or a refused Archive MOVE) leaves
  the source in place and is reported per message as partial, with the original
  folder named. Messages whose folder placement has never completed a sync are
  refused rather than guessed.
- **A benign reconcile race.** The watcher may reconcile the same mailbox
  between the plan and the write. That is safe: the write is guarded by the
  epoch and the identity check, and the cache is only updated from a confirmed
  placement. If the reconcile lands after the write but before the cache update,
  the next check is authoritative and repairs any difference; the action never
  writes a placement it did not confirm.
- **Sync** (`internal/sync`): mailboxes in priority order (All Mail, then the
  system folders, then folders, labels, and the rest; Recovered Messages last),
  headers newest first in windows of 200 UIDs, each committed before the next,
  then reconcile (server deletions, flags, and the unseen count), then bodies
  newest first with a delay between fetches. A body is fetched once per message,
  through All Mail when it has a placement there. A cached placement UID is never
  fetched again, so an interrupted run resumes without refetching. The sync never
  writes to the mailbox.
- **Existing caches resync once.** Schema v3 drops messages and bodies and resets
  the sync state. Mailboxes stay. The cache is disposable, so the next sync
  rebuilds it. Bodies are fetched again, one per message.
- Cache files hold mail content, so they are created with 0600 permissions on
  Linux and under a user-only ACL on Windows. The directory is created with 0700.
  The Windows ACL is not set by code yet; it relies on the user profile.

## 8. Build, platform and testing

- Wails v3 (v3.1.0). Linux uses GTK4 and WebKitGTK 6.0. Windows uses WebView2.
- The Wails CLI is installed with `CGO_ENABLED=0` by `mise run wails3`. Go checks
  (`lint`, `test`) also run with `CGO_ENABLED=0 -tags purego`. The app build
  itself needs cgo on Linux (`mise run build`).
- **Testing caveat:** lint and test run on the **purego (CGO off) backend**, not
  the shipped GTK4/WebKitGTK backend. Passing `mise run check` does not prove the
  GTK4 build works. **CI should include at least one real cgo build** (`mise run
  build` on Linux with GTK4/WebKitGTK 6.0 installed).
- `frontend/bindings/` is generated by `wails3 generate bindings` and is not
  committed.

## 9. Roadmap

| Phase | Scope | Status |
|-------|-------|--------|
| 1. Scaffold | Wails + Svelte project, tooling (mise, lint, test, check, doctor), docs | In progress |
| 2. Read-only mailbox | Config and keyring secrets (lane 1), Bridge connection with TOFU pinning (lane 2), SQLite cache and incremental sync (lane 3), Wails services with the bound-method boundary (lane 4, §3.3), folder/label listing, message list and view (frontend, later) | In progress |
| 3. LLM suggestions | Provider abstraction, structured output with validation, categorize/label suggestions shown to the user (no changes applied) | Planned |
| 4. Actions | Allowlisted actions, confirm tokens, confirmation UI, audit log, undo | Planned |
| 5. Drafts | Reply drafting saved to Drafts after user review, never auto-sent | Planned |
