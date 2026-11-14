# internal/secrets

Secret storage for Bridge passwords and LLM API keys. Phase 2.

- **Keyring only.** Secrets go in the OS keyring via
  [go-keyring](https://github.com/zalando/go-keyring): Windows Credential Manager
  on Windows, Secret Service on Linux. Service name `temail`; user keys
  `bridge:<accountID>` and `llm:<provider>`.
- **No file fallback.** If the keyring is unusable, `KeyringStore.Probe()` returns
  `ErrKeyringUnavailable` with a message the UI can show. Nothing is written to
  disk instead.
- **Resolution order:** environment variable first, then keyring.
  - `TEMAIL_BRIDGE_PASSWORD`: Bridge password override. It applies to any account,
    so it is meant for single-account setups.
  - `TEMAIL_LLM_<NAME>_KEY`: per-provider override. `NAME` is the provider name
    uppercased, with every non-alphanumeric character replaced by `_`
    (`openrouter` gives `TEMAIL_LLM_OPENROUTER_KEY`).
  - Environment values are never written back to the keyring.
- **Missing secrets.** Get methods return `ErrNotFound` when neither source has a
  value. For LLM keys this is not a failure if the provider needs no key; check
  it with `errors.Is`. Deleting a missing entry is not an error.

## What lives where

| Data | Location | Notes |
|------|----------|-------|
| Bridge password | OS keyring (`bridge:<id>`) or `TEMAIL_BRIDGE_PASSWORD` | Never in config.json |
| LLM API key | OS keyring (`llm:<name>`) or `TEMAIL_LLM_<NAME>_KEY` | Optional per provider |
| Accounts, ports, TLS mode, provider URLs and models | `config.json` (see `internal/config`) | No secrets |
