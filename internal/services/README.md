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
| `SetupService` | `GetStatus(ctx)`, `SaveAccount(ctx, host, imapPort, smtpPort, tlsMode, username, password)`, `ProbeCert(ctx)`, `TrustCert(ctx, fingerprint, replace)`, `TestConnection(ctx)` |
| `MailService` | `CacheInfo(ctx)`, `ListMailboxes(ctx)`, `ListMessages(ctx, mailbox, cursor, limit)`, `GetMessage(ctx, mailbox, uidValidity, uid)`, `GetAttachment(ctx, mailbox, uidValidity, uid, partID)` |
| `SyncService` | `Start()`, `Stop()`, `Status()` |

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

`services.Shutdown(sync, mail, store)`: stop the sync, then `mail.shutdown()`
(refuse new calls, cancel running ones, close the Bridge session, wait up to
10 s for in-flight reads), then close the cache. If either step did not finish,
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

- `CacheInfo(ctx)` returns `CacheInfoDTO{bytes, messages, bodies}` (camelCase
  JSON). It reads through `store.CacheStats`: `bytes` is `os.Stat` of the
  database, `-wal` and `-shm` (a missing file counts 0), and the counts are
  `COUNT(*)` with an equality filter on `account`. Both `messages` and `bodies`
  have a primary key that starts with `account`, so SQLite counts from that index
  without reading rows. Measured on this machine at 50k messages and 25k bodies:
  4.3 ms best case. Nothing is cached, so no counter can drift. It never connects
  to Bridge. With no configured account, the counts are 0 and the bytes are still
  reported.
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
- `bindings_test.go`: the bound method set, the read-only name check, the
  secret-field check on every bound return type, and which methods take the
  Wails context first.
- `hardening_test.go`: a blocked keychain does not block startup; a late
  autostart is dropped; a stop during a credential read drops the launch; a
  restart starts one run with a fresh password; a stuck stop is reported and
  keeps the cache open; shutdown drains or refuses reads; a queued call honours
  its context; the keyring probe times out and is cached; config changes during
  a probe are refused; `replace` for pins; `SaveAccount` keeps the stored
  password, fails without one, and restores config when the keyring fails.

Run with `go test -race ./internal/services/`. The race detector needs cgo
(`CGO_ENABLED=1`). The `mise run test` task runs with cgo off and no `-race`.
