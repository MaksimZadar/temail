# temail UI design (phase 2)

Status: accepted direction, PR 1 (shell and setup wizard) implemented. Scope: first-run setup and read-only browsing.

**Principle.** temail is a modern desktop app first. Everything works with the mouse alone: visible buttons, clickable rows and folders, hover and focus states, tooltips. Vim-style keys are an optional accelerator layered on top, never required. It must not look like a terminal: no monospace UI, no mode line, no key-hint bar as the main interface.

## 1. Visual direction

Dense but friendly (think Linear, Thunderbird's compact view), quiet, content first.

- **Type.** IBM Plex Sans for all UI and message text (13 px UI, 14 px message body, 12 px small). JetBrains Mono only for fingerprints, code and key caps. Both are self-hosted through `@fontsource` (no CDN). Tabular numerals for dates and counts.
- **Density.** List rows 28 px, 4 px grid, 1 px hairlines, 6-12 px corner radius on cards and dialogs, soft shadows only on overlays. A roomier row size is a later setting.
- **Theme.** Follows the system (`prefers-color-scheme`); `:theme auto|light|dark` in the palette overrides it through `<html data-theme>`.
- **Tokens** (`frontend/src/styles/tokens.css`; the tests check that both themes define them and that text pairs meet WCAG AA 4.5:1):

| Token | Light | Dark |
|---|---|---|
| `--bg` | #fafaf8 | #14161a |
| `--bg-pane` | #f2f1ed | #191c21 |
| `--bg-raised` (cards, dialogs) | #ffffff | #1f2329 |
| `--fg` | #1d1f23 | #d8dbe0 |
| `--fg-dim` | #5c616a | #8d95a0 |
| `--line` | #dcdad3 | #2a2e36 |
| `--accent` (violet) | #6d41d8 | #a78bfa |
| `--accent-fg` (text on accent) | #ffffff | #14161a |
| `--sel-bg` | accent at 14% | accent at 16% |
| `--danger` / `--ok` | #b42f26 / #1d7a46 | #f0847b / #6fcf97 |

- **States.** Unread: bold sender and subject plus a small violet dot. Read: regular weight, dimmed subject. Selected row: `--sel-bg` background plus a 2 px violet bar on the left. Hover: a lighter tint of the same colour. Focus: 2 px violet outline, `:focus-visible` only. Never colour alone: unread is weight plus dot, selected is bar plus tint.

## 2. Layout (PR 2 builds the panes)

- A toolbar on top (search/filter field, refresh, theme menu), three panes (folders and labels, message list, message view), and a slim footer. Every action has a visible button with a tooltip that shows its shortcut, for example "Refresh (g r)".
- **Footer.** A small status dot and text for sync ("syncing Inbox 120/900", "synced"), a thin progress bar while syncing, and a quiet "Shortcuts ?" button on the right. No mode name and no key-hint line. While a key sequence is pending (for example `g`), a small chip shows it, and after 400 ms a small popover lists what can follow (which-key).
- **Panes.** Drag handles; widths are saved. Below 1100 px the folder pane becomes an icon rail; below 800 px the view becomes a single pane over the list with a Back button (also `h`/`Esc`). Folders, rows and buttons are all clickable.
- **Virtualised list.** Fixed row height, windowed rendering with 10 rows of overscan, pages of 50 using the `next` cursor, the cursor tracked by (mailbox, uidValidity, uid) so a sync refresh doesn't move it. `ErrStaleEpoch` reloads the list and shows a toast.
- Sync state comes from `sync:progress`, `sync:done` (may carry `mailboxErrors`, shown as "some folders failed") and `sync:error`. The list reads from the cache and never waits for sync.

## 3. Keyboard (optional accelerator)

Keys are ignored while typing in any input, during IME composition, and for Ctrl/Alt/Meta combinations the browser or OS owns (only Ctrl-d/u/f/b are ours). `Esc` leaves a field. Counts (`5j`) and sequences (`g i`) time out after 1 s; `Esc` cancels them. The single table in `frontend/src/lib/keys/keymap.ts` drives the handler, the `?` overlay and the which-key popover.

**Active now or in PR 2** (read-only): `j`/`k` move, `gg`/`G` first/last (`12G` = line 12), `Ctrl-d`/`Ctrl-u` half page, `Ctrl-f`/`Ctrl-b` page, `h`/`l` and `Tab` pane focus, `Enter`/`o` open, `[`/`]` previous/next message, `g i/s/d/t/a/j` go to Inbox/Sent/Drafts/Trash/Archive/Spam, `g l` pick any folder or label, `g r` refresh, `g h` plain text/HTML, `i` load images for this message, `y` copy sender, `/` filter the list by subject, `:` command palette, `?` shortcuts. In the setup wizard: `Enter` continues, `Esc` goes back, `?` and `:` work.

**Reserved for later** (the keys stay free and are listed greyed in `?`; pressing one shows "Not available yet"): `e` archive, `#` trash, `!` spam, `m` move, `;` label (not `l`, which is pane focus), `s` star, `U` mark unread, `x` select, `u` undo (phase 4); `a` accept suggestion, `S` suggestion panel (phase 3); `r`/`R` reply drafts (phase 5). Destructive actions will always ask for confirmation. Lowercase acts on the current item; uppercase is the broader variant. Remapping in a config file comes later.

**Discoverability, gently.** Tooltips with shortcuts on buttons, the `?` overlay (also the footer button), the which-key popover, and the command palette listing commands. No always-visible hint bar. The setup wizard shows one short line, "Enter continues. Esc goes back."

**Accessibility.** Panes are landmarks; the list is a listbox with `aria-activedescendant` and `aria-posinset`/`aria-setsize`; folder names include the unread count. Errors use `role="alert"`; sync and status use `aria-live="polite"`. Focus moves to the step title when the wizard changes step, overlays trap focus and restore it on close, and a visible focus ring is always shown. All text meets AA contrast in both themes. `prefers-reduced-motion` turns off animations (the sync pulse and transitions). Sizes use `rem`. Single-key shortcuts can later be turned off (WCAG 2.1.4); every function has a mouse path.

## 4. Setup wizard (implemented)

A centred card, one step per screen, "Step N of 4", plain short copy. Driven by `GetStatus`, so a restart resumes at the first unmet step. Enter continues where that is safe, Esc goes back. Enter never trusts a certificate; that needs a click or Enter on the focused button.

1. **Welcome and checks.** Explains that mail is read through Bridge on this computer. Live checks: Bridge reachable, password storage ready. "Check again" and a disabled Continue until both pass.
2. **Bridge account.** Username and password (from Bridge's Mailbox details, not the Proton password). Advanced: host (`127.0.0.1`), IMAP 1143, SMTP 1025, connection type (STARTTLS default, or SSL/TLS). The host is loopback only. When an account already exists, the password may be left empty to keep the saved one. The password is cleared from the form as soon as `SaveAccount` returns, success or not; it is never kept anywhere else.
3. **Check this connection.** `ProbeCert` then `TrustCert(fingerprint, replace)`. Explains that Bridge makes its own certificate, shows the fingerprint (mono, copy button), what it is, and how to compare it with the one exported from Bridge. If a different certificate is already saved (`already_pinned`), the button becomes "Replace saved certificate" with a short warning. If it changed between probe and trust, or the settings changed, nothing is saved and the copy says so.
4. **Test and finish.** `TestConnection`, then "Connected. Your mail will start loading now." and "Open mailbox".

**Errors.** Each appears as a banner with `role="alert"` and a recovery button; no secret is ever shown. Keyring unavailable (unlock KWallet / check Credential Manager; back to checks), Bridge not running (open Bridge and sign in; retry), wrong password (use Mailbox details; back to the password), not trusted yet or certificate changed (review the certificate), invalid settings (edit them), no saved password, and a generic fallback. All `TestConnection` categories are mapped (`frontend/src/lib/setup/errors.ts`) and tested.

## 5. Safe email rendering (PR 2)

- `HTMLBody` (already sanitised in Go) goes in `<iframe sandbox srcdoc>` with an empty sandbox list: no scripts, no same-origin, no forms, no popups; a meta CSP `default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'` (`https:` is added to `img-src` only after the user loads images for that message).
- **Scrolling (decided).** A sandboxed frame without `allow-same-origin` cannot be scrolled or watched by the host, so the host does not try. The message header, image banner and attachments stay fixed above; the frame fills the remaining height and scrolls itself (mouse wheel, touchpad, scrollbar). To scroll by keyboard, focus the frame (`Enter` or `l` on the message view, or click it), then the native keys work (arrows, Space, PgUp/PgDn). Keys typed inside the frame never reach temail, so `Tab` or a click leaves it; this is the cost of the sandbox. The frame gets an accessible title ("Message body"). No scripts or `allow-same-origin` are added.
- **Remote images.** A banner above the body when the message has any: "Remote images are blocked. They can tell the sender you opened this." with a "Load images" button (key `i`). Per message, never remembered in phase 2.
- **Plain text.** A "Plain text / HTML" toggle (`g h`). Plain text is wrapped, proportional, and URLs are not auto-linked.
- **Links.** Clicking a link opens a small confirm showing the real address only when the link text and the target differ; otherwise it opens in the system browser. Never inside the frame.
- Attachments are listed with name, type and size; saving uses `GetAttachment` (limit 10 MiB, not written to disk by Go).

## 6. Svelte 5 structure and tests

```
frontend/src/
  App.svelte                 route (loading | setup | main), key handler, overlays
  lib/api.ts                 typed wrappers over the generated Wails bindings and events (mocked in tests)
  lib/keys/keymap.ts         the one key table
  lib/keys/dispatcher.ts     window key handler: counts, sequences, input/IME rules
  lib/stores/*.svelte.ts     app (route), ui (overlay, pending keys, theme), sync (events)
  lib/setup/                 wizard.svelte.ts (step logic), errors.ts (copy)
  lib/components/            Wizard, Footer, HelpOverlay, Palette, WhichKey, ErrorBanner, MainPlaceholder
  styles/                    tokens.css, base.css
```

Stores are classes with `$state` fields. Tests (vitest, jsdom, Testing Library) mock `lib/api`: wizard step logic and every error category, the key handler (counts, g-prefix, timeout, inputs, IME, contexts, overlays), keymap table sanity, theme tokens and contrast, and app routing and wizard flows. Real Wails calls are not tested here.

## 7. Decided answers

`l` is pane focus (labels use `;` later). Three panes by default, single pane on small windows. Flat list (no threads). Follow the system theme. `/` is a client-side subject filter. Bundled Plex Sans and JetBrains Mono. Confirm link opening only when the text and the target differ. Remappable keys later. Violet accent.
