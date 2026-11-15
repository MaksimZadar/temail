# internal/config

Non-secret settings in `config.json`, under `os.UserConfigDir()/temail`
(`~/.config/temail/config.json` on Linux, `%APPDATA%\temail\config.json` on
Windows). Phase 2.

- **Content:** schema `version`, `bridge` (accounts: `id`, `host`, `imapPort`,
  `smtpPort`, `tlsMode` `starttls`|`ssl`, `username`, optional
  `certFingerprint`) and `llm`
  (`defaultProvider`, `providers[]`: `name`, `baseURL`, `model`, and the optional
  fields below).
- **LLM provider fields (phase P3).** Added without a schema bump, so older files
  load unchanged (`TestOldConfigFileLoadsUnchanged`):
  - `kind`: `cloud` or `local`. Empty means derived with `EffectiveKind`: a
    loopback host (`127.0.0.1`, `::1`, `localhost`) is `local`, anything else is
    `cloud`. A `local` kind on a non-loopback URL fails validation.
  - `enabled`: optional bool. `IsEnabled` treats nil as the default for the kind:
    cloud off, local on. A cloud provider receives no content until it is enabled.
  - `capabilities`: `structuredOutput`, `tools` (both bool, default false) and
    `maxContext` (tokens, 0 means unknown, negative fails validation).
  - Provider names are not validated beyond non-empty and unique here. The
    services layer limits them to `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`, so they fit a
    keyring user key and an environment variable name.
- **Defaults:** host `127.0.0.1`, IMAP port `1143`, SMTP port `1025`, TLS mode
  `starttls`. A missing file loads as an empty config; omitted Bridge fields take
  these defaults on load.
- **Validation:** `Validate` runs on load and before every save. It checks unique
  account IDs and provider names, port ranges, `tlsMode`, that
  `defaultProvider` exists when set, and that each `baseURL` is an http(s) URL.
  All problems are reported together and wrap `ErrInvalid`.
- **Writes:** `Save` writes a temp file in the same directory and renames it over
  `config.json`. The directory is 0700 and the file is 0600. Unknown JSON fields
  are rejected on load.
- **Certificate pin.** `certFingerprint` is the SHA-256 of the Bridge leaf
  certificate as 64 hex characters (either case); it is omitted when unset. It is
  written by the trust-on-first-use flow in `internal/mail` and is not a secret.
  Empty means "not pinned yet".
- **No secrets.** The file never holds passwords or API keys; see `internal/secrets`.
  LLM keys are in the keyring under `llm:<name>` (or `TEMAIL_LLM_<NAME>_KEY`).

## What lives where

| Data | Location | Notes |
|------|----------|-------|
| Accounts, ports, TLS mode, provider URLs and models | `config.json` | Non-secret |
| Bridge password, LLM API keys | OS keyring (see `internal/secrets`) | Env vars override |
| Environment overrides | `TEMAIL_BRIDGE_PASSWORD`, `TEMAIL_LLM_<NAME>_KEY` | Not persisted |
