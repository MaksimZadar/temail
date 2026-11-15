# internal/services

The Wails layer between the webview and the Go packages. It is thin: it
validates arguments, calls the packages below, shapes JSON-friendly results and
emits events. The business logic stays in `internal/config`, `internal/secrets`,
`internal/bridge`, `internal/mail`, `internal/store` and `internal/sync`.

**Trust boundary.** Every exported method of the three services is bound, so any
JavaScript in the webview can call it. Read `docs/ARCHITECTURE.md` §3.3 before
adding a method. The rules are:

- Read-only against the mailbox. No SMTP, no LLM, no flag, move or delete.
- No secret goes out: no password or API key in a return value, an error, an
  event or a log line. The password comes in only through `SaveAccount`.
- Inputs are validated here (mailbox length, UID, part ID pattern, fingerprint
  format, loopback host). No filesystem path comes from JS.
- The methods are checked by tests (`bindings_test.go`): the bound method set
  is exact, the names contain no write verbs, and no returned type has a
  secret-looking field. Changing the set fails the test on purpose.

## Services and bound methods

| Service | Bound methods |
|---------|---------------|
| `SetupService` | `GetStatus`, `SaveAccount`, `ProbeCert`, `TrustCert`, `TestConnection` |
| `MailService` | `ListMailboxes`, `ListMessages`, `GetMessage`, `GetAttachment` |
| `SyncService` | `Start`, `Stop`, `Status` |

`ServiceStartup` and `ServiceShutdown` are lifecycle hooks, not bound.
`MailService.ServiceShutdown` closes its Bridge connection. `SyncService`'s hooks
start the sync at app start (if ready) and stop it at shutdown.

### SetupService

- `GetStatus()` returns `configured`, `keyringOk` with `keyringMessage`,
  `bridgeReachable`, `certPinned` and `accountId`. Reachability is a TCP dial
  with a 1 s timeout. A non-loopback host is never dialled.
- `SaveAccount(host, imapPort, smtpPort, tlsMode, username, password)` validates
  the input (loopback host, non-empty username and password, length limits, and
  `config.Validate`). It stores the password in the keyring under `main` (or the
  existing account's ID) and then saves `config.json`. Nothing is written when
  validation fails. If the host or IMAP port changes, the pin is cleared. Sync
  restarts afterwards.
- `ProbeCert()` returns the presented fingerprint and subject. It connects with
  the pin cleared, so the handshake stops at the certificate check and no
  password is sent. Nothing is pinned.
- `TrustCert(fingerprint)` probes again. It pins the value only if the fresh
  fingerprint equals the one given (case-insensitive). Otherwise it returns
  `ErrFingerprintChanged` and writes nothing.
- `TestConnection()` logs in and closes at once. It returns a category instead of
  an error, so the UI can show a clear message. The categories are listed in
  `ARCHITECTURE.md` §3.3. Dial failures come from a `*net.OpError` with
  `Op == "dial"`.

### MailService

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
  Declared sizes over 25 MiB are refused before the fetch, and the fetched bytes
  are checked again. The bytes go back in the response (base64 in JSON). Nothing
  is written to disk, so the save-dialog option from the plan was not used.

### SyncService

- `Start()` runs `internal/sync` in a goroutine with a context. The context is
  the app's startup context, so it is cancelled at shutdown. If a run is already
  active, `Start` does nothing. It refuses (without creating a client) when the
  account is missing, unpinned or has no password.
- `Stop()` cancels the run and waits up to 15 s. It is a no-op when nothing is
  running.
- `Status()` returns `running`, `lastResult` and `lastError`.
- Events: `sync:progress` on every progress step, then one of `sync:done`
  (also for partial runs, with `mailboxErrors`) or `sync:error`. A stopped run
  emits no done or error event.

## Injection

Each service takes interfaces, so tests use fakes:

- `ConfigStore` (`Load`, `Save`): `*config.Manager`.
- `SecretStore` (the `secrets.Store` methods plus `Probe`): `*secrets.KeyringStore`.
- `ClientFactory` (`func() mail.Client`): `mail.NewClient`.
- `Emitter` (`Emit`): `app.Event`.
- `store.Store`: the SQLite cache. Tests use a fake for `MailService` and a real
  SQLite file in `t.TempDir()` for `SyncService`.

`main.go` builds the wiring and registers the services with `app.RegisterService`.
The shutdown hook stops the sync and closes the cache. The services shut down
after the hook runs.

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
- `bindings_test.go`: the bound method set, the read-only name check, and the
  secret-field check on every bound return type.

Run with `go test -race ./internal/services/`. The race detector needs cgo
(`CGO_ENABLED=1`). The `mise run test` task runs with cgo off and no `-race`.
