# internal/bridge

The connect, pin and password flow for Proton Bridge. It is shared by
`internal/services` (the app) and by the manual tools `cmd/mailsync` and
`cmd/mailprobe`, so the flow is written once. It has no UI and no Wails code.

## What it provides

| Function | Purpose |
|----------|---------|
| `PickAccount(cfg, id)` | Index of an account by ID, or the first account when `id` is empty. Errors: `ErrNotConfigured`, `ErrUnknownAccount` |
| `Password(sec, id)` | Bridge password from the environment or the keyring. Missing gives `ErrNoPassword`. Other keyring read failures give `secrets.ErrKeyringUnavailable`. The password is never in an error |
| `Credentials(cfg, sec)` | Loads the config, picks the first account and reads its password. It does not check the pin |
| `ProbeCert(ctx, client, acct)` | Returns the fingerprint and subject that Bridge presents. It connects with the pin cleared, so the handshake always stops at the certificate check and no password is sent |
| `EnsurePinned(ctx, mgr, cfg, idx, pw, trust, report)` | The `cmd/mailsync` flow: if the certificate is not pinned, reports it, and with `trust` saves it to `config.json` |
| `Session` | One lazily connected `mail.Client` for on-demand reads. Calls are serialised. A failed call closes the connection, so the next call reconnects. `Close` can run during a call |

## Rules

- A password is never part of an error, a log line or a return value other than
  the one that feeds `mail.Client.Connect`.
- `ProbeCert` never logs in. It is the only way to see an unpinned certificate
  without a password.
- Loopback-only rules stay in `internal/mail`. This package adds no new dial path.

## Tests

`bridge_test.go` uses a fake `mail.Client` and fake secret and config sources.
It covers account picking, the password error mapping (including the check that
the password is not in the error), credentials, the probe (no password sent,
client closed), `Session` reconnect after a failure, and `EnsurePinned` with an
unreachable port (nothing is written). The full pin and login path against a
real IMAP server stays in `internal/mail` tests.
