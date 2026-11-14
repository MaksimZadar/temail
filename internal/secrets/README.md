# internal/secrets

Credential storage: OS keychain via go-keyring. The file fallback (DPAPI on
Windows, 0600 on Linux) is explicit user opt-in only, with a UI warning. Per-provider
env override: `TEMAIL_LLM_<NAME>_KEY` (e.g. `TEMAIL_LLM_OPENROUTER_KEY`). Phase 1 has no code here yet.
