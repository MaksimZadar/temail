# internal/services

The Wails layer between the webview and the Go packages. It is thin: it
validates arguments, calls the packages below, shapes JSON-friendly results and
emits events. The business logic stays in `internal/config`, `internal/secrets`,
`internal/bridge`, `internal/mail`, `internal/store`, `internal/sync` and
`internal/actions`.

**Trust boundary.** Every exported method of the services is bound, so any
JavaScript in the webview can call it. Read `docs/ARCHITECTURE.md` §3.3 before
adding a method. The rules are:

- Read-only against the mailbox, with one reviewed exception: `ActionService`
  is the intentional write boundary. Its four methods reach only the allowlisted,
  verified recipes in `internal/actions` (no permanent delete, no label
  create/rename, no drafts). Every action is audited and undoable. The other
  services never write flags, folders or messages.
- No secret goes out: no password or API key in a return value, an error, an
  event or a log line. The password comes in only through `SaveAccount`.
- Inputs are validated here (mailbox length, UID, part ID pattern, fingerprint
  format, loopback host, action type and batch size). No filesystem path comes
  from JS.
- The methods are checked by tests (`bindings_test.go`): the bound method set
  is exact, the names contain no write verbs outside `ActionService`, and no
  returned type has a secret-looking field. Changing the set fails the test on
  purpose.

## Services and bound methods

| Service | Bound methods |
|---------|---------------|
| `SetupService` | `GetStatus(ctx)`, `SaveAccount(ctx, host, imapPort, smtpPort, tlsMode, username, password)`, `ProbeCert(ctx)`, `TrustCert(ctx, fingerprint, replace)`, `TestConnection(ctx)` |
| `MailService` | `CacheInfo(ctx)`, `ListMailboxes(ctx)`, `ListMessages(ctx, mailbox, cursor, limit)`, `GetMessage(ctx, mailbox, uidValidity, uid)`, `GetAttachment(ctx, mailbox, uidValidity, uid, partID)` |
| `SyncService` | `Start()`, `Stop()`, `Status()` |
| `LLMService` | `ListProviders()`, `SaveProvider(input)`, `DeleteProvider(name)`, `SetDefaultProvider(name)`, `TestProvider(ctx, name)`, `ListModels(ctx, name)` |
| `ActionService` | `Apply(ctx, ActionDTO)`, `Undo(ctx, id)`, `UndoLast(ctx)`, `Recent(ctx, limit)` |

`ctx` is the Wails call context. It is not part of the JavaScript signature.

`ServiceStartup` and `ServiceShutdown` are lifecycle hooks, not bound.
`ServiceStartup` starts the autostart in the background, so it never waits for
the keychain. `ServiceShutdown` on `MailService` and `SyncService` is idempotent
and is also called by Wails after `services.Shutdown`. `services.Shutdown` is a
plain function (not bound) that does the ordered teardown described below.

### Lifecycle and launch generations

`SyncService` keeps a generation counter. `Stop` bumps it. A launch reads its
credentials, then checks the generation under the lock. If a stop or restart ran
in between, the launch drops itself. So `restart` starts exactly one run, with
credentials read after the restart. If the autostart's credential read takes
longer than 10 s (a locked keychain), the autostart bumps the generation, so the
late launch cannot start a run unless the user asks.

`services.Shutdown(sync, mail, store, actions...)`: stop the sync, then
`mail.shutdown()` (refuse new calls, cancel running ones, close the Bridge
session, wait up to 10 s for in-flight reads), then each action service's
shutdown (refuse new calls, wait for in-flight writes, close the Writer
connections and `audit.db`), then close the cache. If any step did not finish,
the cache stays open and the error says so.

### Configuration writes

`SetupService` holds `cfgMu` for each load-check-save sequence (`SaveAccount`,
`TrustCert`). Probes run outside the lock. `TrustCert` re-reads the config after
the probe. If the account ID, host, IMAP port or TLS mode changed, it returns
`ErrAccountChanged`.

`SaveAccount` writes the config first and the keyring second. If the keyring
write fails, the previous config is restored. An empty password keeps the stored
one. If no password is stored, the call fails with `no_password`.

`TrustCert(fingerprint, replace)`: if the account already has a different pin
and `replace` is false, it returns `ErrAlreadyPinned` (text starts with
`already_pinned`) before probing. The UI then shows the re-pin flow. Pinning the
stored value again is a no-op.

### Keyring probe

`GetStatus` runs `Probe` in a goroutine and waits at most 2 s. On timeout it
reports a "keychain did not respond; it may be locked" message. The probe keeps
running, and its result is cached for 30 s.

### SetupService

- `GetStatus()` returns `configured`, `keyringOk` with `keyringMessage`,
  `bridgeReachable`, `certPinned` and `accountId`. Reachability is a TCP dial
  with a 1 s timeout. A non-loopback host is never dialled.
- `SaveAccount(...)` validates the input (loopback host, non-empty username,
  length limits, and `config.Validate`). It saves `config.json` first and then
  stores the password in the keyring under `main` (or the existing account's
  ID). Nothing is written when validation fails. If the host or IMAP port
  changes, the pin is cleared. Sync restarts afterwards.
- `ProbeCert()` returns the presented fingerprint and subject. It connects with
  the pin cleared, so the handshake stops at the certificate check and no
  password is sent. Nothing is pinned.
- `TrustCert(fingerprint, replace)` probes again. It pins the value only if the
  fresh fingerprint equals the one given (case-insensitive) and no different pin
  is in place (or `replace` is set). Otherwise it returns `ErrFingerprintChanged`,
  `ErrAlreadyPinned` or `ErrAccountChanged` and writes nothing.
- `TestConnection()` logs in and closes at once. It returns a category instead of
  an error, so the UI can show a clear message. The categories are listed in
  `ARCHITECTURE.md` §3.3. Dial failures come from a `*net.OpError` with
  `Op == "dial"`.

### MailService

- **Watching (phase P4 PR B).** After the first sync, `SyncService` starts the
  change watcher on the same run (see `internal/sync/README.md`). The run keeps
  its slot, so `Stop` and shutdown stop the watcher through the same context.
  - `Start()` while the watcher runs asks it for a check now and returns nil.
    The old "already running" answer is gone.
  - Login refusals are latched across the sync, the poll and the IDLE connection
    (`mailsync.AuthLatch`). After a refused login, a restart of sync returns the
    latched error without logging in. A changed password clears it.
  - `SyncStatus` now has `running` (only during the first sync), `watching` (the
    watcher is up), `idle` (the IDLE connection is up on INBOX) and `lastChecked`.
    `running` used to mean "a run exists"; a UI that showed a spinner for
    `running` now sees the spinner stop once the first sync is done. `watching`
    shows the live state.
  - `sync:changed` `{mailboxes: [...]}` is emitted after a check found changes
    and they were synced. Bursts are coalesced.
  - A permanent watch error (auth failure) emits `sync:error` and stops the
    watch. A stop emits nothing.
  - Tests inject the IDLE connection through `newWatcher`, so they need no
    server.
- `CacheInfo(ctx)` returns `CacheInfoDTO{bytes, messages, bodies}` (camelCase
  JSON). It reads through `store.CacheStats`: `bytes` is `os.Stat` of the
  database, `-wal` and `-shm` (a missing file counts 0), and the counts are
  `COUNT(*)` with an equality filter on `account`. Both `messages` and `bodies`
  have a primary key that starts with `account`, so SQLite counts from that index
  without reading rows. Measured on this machine at 50k messages and 25k bodies:
  4.3 ms best case. Nothing is cached, so no counter can drift. It never connects
  to Bridge. With no configured account, the counts are 0 and the bytes are still
  reported.
- **Membership (additive DTO fields, phase P4).** `MailboxDTO` gains `unseen`
  (the mailbox's unseen count from its last sync). Each message in `ListMessages`
  and `GetMessage` gains `mailboxes`: every mailbox it is in (folder and labels),
  each as `{name, displayName, kind}`. `internalId` and `canonical` are the
  Proton identity. `GetMessage(mailbox, uidValidity, uid)` still resolves by
  placement. The body is shared by all placements of a message. A list page
  reads membership in one batched query (`MembershipOfMessages`), so a page
  costs a fixed number of queries regardless of its size.
- `ListMailboxes()` and `ListMessages(mailbox, cursor, limit)` read only the
  cache. They never connect. `limit` defaults to 50 and is capped at
  `store.MaxListLimit`. A full page returns a `next` cursor, and a short page
  returns `null`.
- `GetMessage(mailbox, uidValidity, uid)`: the cached body is returned if it
  exists. Otherwise the message is fetched through a `bridge.Session`, stored with
  `SetBody`, and returned. The fetch first checks the server's UIDVALIDITY. If it
  differs from the caller's, the call fails with `ErrStaleEpoch`, so a UID can
  never resolve to a message from another epoch.
- `GetAttachment(mailbox, uidValidity, uid, partID)`: `partID` must match
  `^[0-9]+(\.[0-9]+)*$` and must be one of the message's listed attachments.
  Declared sizes over 10 MiB are refused before the fetch, and the fetched bytes
  are checked again. The bytes go back in the response (base64 in JSON). Nothing
  is written to disk, so the save-dialog option from the plan was not used.

  **Future work:** the whole part is buffered in memory and sent over the bridge.
  Streaming it through the Wails asset handler (a URL the frontend downloads from)
  would remove the cap and the copy. That needs its own review of the handler's
  access checks before it ships.

### SyncService

- `Start()` runs `internal/sync` in a goroutine with a context. The context is
  the app's startup context, so it is cancelled at shutdown. If a run is already
  active, `Start` does nothing. It refuses (without creating a client) when the
  account is missing, unpinned or has no password.
- `Stop()` cancels the run and waits up to 15 s. It is a no-op when nothing is
  running. On timeout it returns `ErrSyncStopTimeout`, and `Status()` reports
  `stopTimedOut` until the run ends. The cache is then kept open at shutdown.
- `Status()` returns `running`, `stopTimedOut`, `lastResult` and `lastError`.
- Events: `sync:progress` on every progress step, then one of `sync:done`
  (also for partial runs, with `mailboxErrors`) or `sync:error`. A stopped run
  emits no done or error event.

### LLMService (phase P3)

Manages provider settings and keys. It never sends mail content. Its only network
calls are `TestProvider` (one tiny structured call) and `ListModels`, both made by
the user's explicit click.

- `ListProviders()` returns `LLMProviderDTO`: `name`, `baseURL`, `model`, `kind`,
  `enabled`, `capabilities` (`structuredOutput`, `tools`, `maxContext`),
  `isDefault` and `hasKey`. `hasKey` only says whether a key is available (from
  the keyring or `TEMAIL_LLM_<NAME>_KEY`). The key itself is never returned.
- `SaveProvider(LLMProviderInput)` validates the name (`[A-Za-z0-9][A-Za-z0-9_-]{0,63}`),
  the model length and the key length, then `config.Validate` (which also refuses
  two names that read the same environment variable). The steps are:
  1. **Confirmation.** Turning cloud on (a new cloud provider, a disabled one, or
     a local one changed to cloud while enabled) asks the `CloudConfirmer` first.
     `NewNativeConfirmer(app.Dialog)` shows a Wails question dialog with Enable and
     Cancel. The answer is waited for on a channel, with a 60 s limit. Cancel,
     close, a timeout, an error, or no confirmer (`nil`) all return
     `ErrEnableDeclined` and save nothing. Disabling never asks. The dialog runs
     without `cfgMu`, and the lock is taken afterwards. If the provider became an
     enabled cloud provider in the meantime, the call returns `ErrProviderChanged`.
  2. **Key check, for every save.** The effective key (environment or keyring) is
     read. A read error other than "no entry" returns `ErrKeyringUnavailable` and
     saves nothing (fail closed). With an empty `apiKey`, a key that is found
     must be bound to the new base URL, or the call returns `ErrKeyNeedsReentry`.
     This is also true for a new provider, so a key that already exists is never
     inherited silently.
  3. **Write order.** A non-empty `apiKey` replaces the keyring entry first and
     then saves `config.json`. If the save fails, the keyring goes back to its
     previous value (or is deleted if there was none). This is the reverse of the
     account save, which writes config first. The binding is set to the new URL
     whenever a key is written. An empty `apiKey` saves config only.
  The name is the ID and cannot be changed by an update. The whole sequence holds
  `cfgMu`.
- `DeleteProvider(name)` writes the config without the provider (clearing the
  default if it was that one), then deletes the keyring key. If the key delete
  fails, the config is restored. The environment variable is not touched.
- `SetDefaultProvider(name)`; an empty name clears the default.
- `TestProvider(ctx, name)` returns `ProviderTest` `{ok, category, message}`.
  Categories: the llm package's `ok`, `unreachable`, `unauthorized`,
  `model_not_found`, `structured_output_unsupported`, `timeout` and `other`, plus
  `cloud_disabled` (a disabled cloud provider, nothing sent), `no_model`,
  `keyring_unavailable` and `key_needs_reentry` (a stored key is not approved for
  the current base URL; the user enters it again. It is not a network failure). A disabled cloud provider is refused before any
  request. Errors other than category come back as Go errors only for an unknown
  name.
- `ListModels(ctx, name)` returns the sorted IDs. When the endpoint has no models
  route, it returns an empty list and `llm.ErrModelListUnsupported`, which the UI
  shows as "enter the model name".
  When a stored key is not approved for the base URL, it returns an empty list
  and `ErrKeyNeedsReentry`. Its text starts with `key_needs_reentry:`, so the UI
  can match it the same way.
- **`clientFor` never mixes a URL with another URL's key.** It reads the provider
  and the key under `cfgMu`, so a writer that is half done cannot be seen. It then
  releases the lock, and only then makes the client. Its key is used only if
  `KeyBoundTo` holds for the URL it reads. The test
  `TestReadersNeverPairKeyWithWrongURLDuringWrites` runs readers against a writer
  that keeps switching between two servers, and each server checks which key it
  received. Without the lock that test fails about 1 run in 5.

`NewLLMService(cfg, keys, confirm)` takes the confirmer. Pass
`NewNativeConfirmer(app.Dialog)` in `main.go`. Tests pass a fake.

The LLM bound methods are checked by `bindings_test.go`: the exact method set,
the read-only verb check (for this service only the mailbox-write verbs are
banned, since config writes are its job), and the secret-field check. The
`SaveProvider` input struct is allowed to carry `apiKey`, and nothing returned
may.

### ActionService (phase P5)

The only bound service with mailbox write methods. The writes live in
`internal/actions`; this layer validates the DTO, builds the executor lazily and
emits one coalesced event.

- `Apply(ActionDTO)` with `{type, params{folder|label}, messageIds}`. The type
  must be one of the v1 allowlist, the batch is capped at 100 ids, and the ids
  are Proton mids, never UIDs. `actions.Validate` runs here, so bad input fails
  with `ErrInvalidInput` before any cache read or connection. The result is
  `{applied, failed[{mid, mailbox, message}], appliedCount, failedCount}`.
  Per-message failures do not fail the call.
- `Undo(id)` resolves the audit entry's inverse and applies it through the same
  verified path. `UndoLast()` does the same for the newest verified entry.
  `ErrNotUndoable` covers an archive/trash that had to COPY from All Mail.
- `Recent(limit)` returns `AuditDTO {id, ts, type, params, count, error, undone}`.
  Metadata only; no subject or body is ever in the audit log or the DTO.
- The executor is built per configured account. The password is read lazily
  through `bridge.Password` on the first connect, the certificate must be pinned
  (`bridge.ErrNotPinned` otherwise), and the writer uses `mail.Writer`, whose
  `Connect` follows the same pin/TLS rules as the read client.
- The auth latch is the sync service's latch, keyed by `sync.CredsKey`, so a
  refused login on any path stops all of them for those credentials.
- `actions:done` `{type, count, failed}` is emitted after a burst of actions
  (100 ms coalescing window), not per message.

Tests (`actions_test.go`) use a fake engine: DTO mapping and validation, undo,
recent, the coalesced event, shutdown refusal, and camelCase/no-content JSON.
The executor itself is covered in `internal/actions`.

## Injection

Each service takes interfaces, so tests use fakes:

- `ConfigStore` (`Load`, `Save`): `*config.Manager`.
- `SecretStore` (the `secrets.Store` methods plus `Probe`): `*secrets.KeyringStore`.
- `ClientFactory` (`func() mail.Client`): `mail.NewClient`.
- `func() mail.Writer`: `mail.NewWriter` for `ActionService`.
- `Emitter` (`Emit`): `app.Event`.
- `store.Store`: the SQLite cache. Tests use a fake for `MailService` and a real
  SQLite file in `t.TempDir()` for `SyncService`.

`main.go` builds the wiring and registers the services with `app.RegisterService`.
The shutdown hook stops the sync, mail reads and action writes and closes the
cache. The services shut down after the hook runs.

## Tests

- `setup_test.go`: status combinations, `SaveAccount` validation (nothing is
  written on error), persistence with a real `config.Manager` (the password is
  never in `config.json`), account update and pin rules, `ProbeCert` sends no
  password, `TrustCert` pins only a matching fresh fingerprint, and the error
  categories with a check that the password is never in the output.
- `mail_test.go`: cache hit with no connection, fallback fetch and caching, the
  stale-epoch refusal, paging and limits, part ID validation, the size limits,
  and the wrapping of fetch errors.
- `sync_test.go`: refusal when not ready, progress then done, single run at a
  time, `Stop` and shutdown with no outcome event, a sync error event, a partial
  run, and restart. Run with `-race`.
- `bindings_test.go`: the bound method set, the read-only name check, the
  secret-field check on every bound return type, and which methods take the
  Wails context first.
- `llm_hardening_test.go`: a new provider does not inherit an existing key; an
  environment key not bound to the new URL is refused; the client refuses a
  stale binding; the env-name collision; keyring errors fail closed (both reads);
  the key is replaced before config is saved, and restored when the save fails;
  the concurrency test above; the confirmation (confirmed, declined, dialog error,
  no dialog, edit, disable, kind change, the transition table); no key in any
  error.
- `mail_test.go` (membership): a listed message carries its three mailboxes,
  `GetMessage` carries them with the body, and the JSON uses the new camelCase
  fields. `ListMailboxes` carries `unseen`.
- `sync_fix_test.go`: the handoff from sync to watching is seen by listeners as
  watching at sync:done; Start while watching kicks a check; the latch refuses a
  second start with the same credentials and clears on a new password; one login
  serves the sync and the watcher.
- `llm_test.go`: the DTO has no key and uses camelCase JSON; save validation
  writes nothing; create keeps the key out of `config.json`; an empty key keeps the
  stored one; a changed base URL needs a new key; keyring and config failures
  roll back; delete and default; `TestProvider` categories against an `httptest`
  endpoint; `ListModels` sort and 404 degrade; no key in any returned value.
- `hardening_test.go`: a blocked keychain does not block startup; a late
  autostart is dropped; a stop during a credential read drops the launch; a
  restart starts one run with a fresh password; a stuck stop is reported and
  keeps the cache open; shutdown drains or refuses reads; a queued call honours
  its context; the keyring probe times out and is cached; config changes during
  a probe are refused; `replace` for pins; `SaveAccount` keeps the stored
  password, fails without one, and restores config when the keyring fails.
- `actions_test.go`: the action DTO mapping, input refusal, undo id validation,
  recent conversion, coalesced `actions:done`, shutdown refusal, and camelCase
  JSON with no message content.

Run with `go test -race ./internal/services/`. The race detector needs cgo
(`CGO_ENABLED=1`). The `mise run test` task runs with cgo off and no `-race`.

## Notes for the frontend

- `running` is true only during the first sync. Use `watching` for "the app is
  following the mailbox", and `idle` for "new mail arrives instantly".
- Progress events (`sync:progress`) can fire while `running` is false, because a
  watcher check that syncs a mailbox emits them too. Do not treat the first
  progress event as the start of a run, or the first `sync:done` as its end.
  Use `sync:changed` for "these mailboxes changed".
- `start` while watching is a request for a check now. It is safe to call it from
  a "refresh" button.
- A `lastError` of a refused login stays until the password changes or the app
  restarts. Show it with the message from `SetupService.TestConnection`
  (category `auth_failed`), and do not offer a retry loop.
