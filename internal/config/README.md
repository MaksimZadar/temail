# internal/config

Non-secret settings in `config.json`, under `os.UserConfigDir()/temail`
(`~/.config/temail/config.json` on Linux, `%APPDATA%\temail\config.json` on
Windows). Phase 2.

- **Content:** schema `version`, `bridge` (accounts: `id`, `host`, `imapPort`,
  `smtpPort`, `tlsMode` `starttls`|`ssl`, `username`) and `llm`
  (`defaultProvider`, `providers[]`: `name`, `baseURL`, `model`).
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
- **No secrets.** The file never holds passwords or API keys; see `internal/secrets`.

## What lives where

| Data | Location | Notes |
|------|----------|-------|
| Accounts, ports, TLS mode, provider URLs and models | `config.json` | Non-secret |
| Bridge password, LLM API keys | OS keyring (see `internal/secrets`) | Env vars override |
| Environment overrides | `TEMAIL_BRIDGE_PASSWORD`, `TEMAIL_LLM_<NAME>_KEY` | Not persisted |
