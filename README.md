# temail

A desktop email client for **Proton Mail**, built for Linux (CachyOS/Arch) and
Windows. It talks to your mailbox through **Proton Bridge** and can use an LLM
to help categorise, label, move and draft replies to mail.

> Status: Phase 1 (project scaffold). There is no mail functionality yet.
> See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the design and roadmap.

## Stack

- [Wails v3](https://v3.wails.io) (v3.1.0): Go backend, native window
  (GTK4/WebKitGTK 6.0 on Linux, WebView2 on Windows)
- Go 1.26
- Svelte 5 + Vite + TypeScript (in `frontend/`), package manager pnpm
- Mail: IMAP/SMTP via Proton Bridge (`go-imap/v2`, `go-smtp`, `enmime`)
- LLM: any OpenAI-compatible endpoint (OpenRouter, Ollama)
- Storage: SQLite cache (`modernc.org/sqlite`, pure Go)

## Prerequisites

### Both platforms

- [mise](https://mise.jdx.dev) to install Go, Node (LTS), pnpm and the Wails CLI
- Proton Bridge (see [Proton Bridge](#proton-bridge) below)

### Linux (CachyOS / Arch)

Install the GTK4 and WebKitGTK 6.0 development packages and a C toolchain:

```sh
sudo pacman -S --needed base-devel pkgconf gtk4 webkitgtk-6.0
```

`webkitgtk-6.0` pulls in libsoup3 and the other WebKit dependencies. Check with
`mise run doctor`.

### Windows

- WebView2 Runtime. It is preinstalled on most Windows 10/11 systems. Install
  it from Microsoft if `mise run doctor` reports it missing.
- Run commands in a shell that has `mise` on `PATH`.

## Quick start

```sh
mise install          # Go, Node LTS, pnpm
mise run setup        # go mod download, pnpm install, install the pinned wails3 CLI
mise run doctor       # report platform deps and Proton Bridge reachability (never fails)
mise run dev          # run the app in dev mode (hot reload)
mise run check        # lint + test + typecheck
mise run build        # production build for the host OS (Linux needs GTK4/WebKitGTK 6.0)
```

All tasks:

| Task | What it does |
|------|--------------|
| `setup` | Installs Go modules, frontend dependencies, and `wails3` |
| `dev` | `wails3 task dev` (Wails dev mode) |
| `build` | `wails3 task build` (host OS) |
| `test` | `go test ./...` and frontend unit tests (vitest) |
| `lint` | `go vet` and eslint |
| `fmt` | `gofmt` and prettier |
| `typecheck` | `svelte-check` |
| `check` | `lint` + `test` + `typecheck` |
| `doctor` | Report-only checks: GTK4/WebKitGTK pkg-config, wails3 doctor, Bridge ports 1143/1025 |

`wails3` is installed from the Go module proxy at a pinned version. It is built
with `CGO_ENABLED=0`, so it does not need the GTK dev packages. The `lint` and
`test` tasks also run with `CGO_ENABLED=0 -tags purego`. The app build itself
still needs cgo on Linux.

## Proton Bridge

temail does not talk to Proton's servers directly. It connects to Proton Bridge
on your machine, which decrypts and serves your mail over IMAP and SMTP.

- **Paid plan required.** Proton Bridge only works with a paid Proton plan.
- **Bridge must be running** and signed in. temail connects to `127.0.0.1` only.
  - IMAP: port `1143`
  - SMTP: port `1025`
- **Self-signed certificate.** Bridge uses a local self-signed TLS certificate.
  temail pins it on first use and asks you to confirm it. It does not disable
  certificate checks.
- **Use the Bridge password.** Log in with the username and the password shown in
  Bridge's settings, not your Proton account password.

## Secrets and settings

- **Secrets** (Bridge password, LLM API keys) go in the OS keychain through
  [go-keyring](https://github.com/zalando/go-keyring): Windows Credential Manager
  on Windows, Secret Service on Linux.
- **Fallback:** if no keychain is available, temail asks you to opt in to a file
  store (DPAPI-encrypted on Windows, 0600 on Linux). It is never used
  automatically, and the UI shows a warning while it is in use.
- **Environment override:** `TEMAIL_LLM_<NAME>_KEY` takes precedence over the
  stored key for that provider, e.g. `TEMAIL_LLM_OPENROUTER_KEY`.
- **Settings** (non-secret) go in `~/.config/temail/config.json` on Linux and
  `%APPDATA%\temail\config.json` on Windows.

## Project layout

```
main.go              Wails entry point (placeholder window)
internal/            Go packages: mail, llm, store, secrets, config, actions
frontend/            Svelte 5 + Vite + TypeScript UI
build/               Wails platform packaging (Windows, Linux)
docs/                Architecture and design notes
scripts/doctor.sh    Report-only environment check
mise.toml            Toolchain and task definitions
```

## License

MIT. See [LICENSE](LICENSE).
