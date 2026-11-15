# temail UI design (phase 2)

Status: accepted direction. PR 1 (shell and setup wizard) and PR 2 (mailbox view) implemented. Scope: first-run setup and read-only browsing.

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

## 2. Layout (implemented)

- A toolbar on top (folders toggle, a back button in single-pane mode, the folder title, a filter field, refresh, commands, shortcuts), three panes (folders and labels, message list, message view), and a slim footer. Every action has a visible button with a tooltip that shows its shortcut, for example "Refresh (g r)".
- **Footer.** One stable status button on the left, never flapping between folders: "Syncing folders · 3 of 14" with a thin overall bar, then "Downloading messages · 3 of 14 folders", then "Synced · just now" (relative time). If some folders failed, a "Some folders couldn't sync" link appears next to it. Clicking the status opens a sync panel (Esc, a click outside or Close dismisses it and focus returns to the button) that lists every folder: active first, then waiting, done, failed with the reason, each with its state, counts and a small progress bar. The same list is shown in the main placeholder. On the right, a quiet "Shortcuts ?" button. No mode name and no key-hint line.
- **Sync updates.** The store (`stores/sync.svelte.ts`) keeps one entry per folder from `sync:progress` (seeded from `ListMailboxes`, so folders show as waiting), plus an aggregate (folders done, messages cached, overall percent that never steps backwards). Events are coalesced and published at most every 250 ms. Screen readers hear only "Sync started" / "Sync finished" / "Sync failed" through a polite live region, not each step.
- **Key prefixes.** While a prefix such as `g` is pending, the which-key popover stays up with no timeout. A chip in the footer also shows the prefix. It closes when you press a valid follow-up, press Esc, press an invalid key (which only cancels, so a typo never triggers something else), click elsewhere, or click one of its entries (entries are buttons). A polite live region says "g pressed. Options: …" once.
- **Panes.** Drag handles (also focusable, arrow keys resize); widths and the folder-pane toggle are saved in `localStorage`. The folder pane starts hidden on windows under 1000 px (toggle with the toolbar button or `b`; an icon rail is not built). Below 800 px only one pane shows at a time, with a Back button (also `h` and `Esc`). Folders, rows and buttons are all clickable. Folders are ordered Inbox, Drafts, Sent, Archive, Spam, Trash, then folders, then labels.
- **Virtualised list.** Fixed row height, windowed rendering with 10 rows of overscan, pages of 50 using the `next` cursor, the cursor tracked by (mailbox, uidValidity, uid) so a sync refresh doesn't move it. `ErrStaleEpoch` reloads the list and shows a toast.
- Sync state comes from `sync:progress`, `sync:done` (may carry `mailboxErrors`, shown as "some folders failed") and `sync:error`. The list reads from the cache and never waits for sync.

## 3. Keyboard (optional accelerator)

Keys are ignored while typing in any input, during IME composition, and for Ctrl/Alt/Meta combinations the browser or OS owns (only Ctrl-d/u/f/b are ours). `Esc` leaves a field. Counts (`5j`) and sequences (`g i`) wait with no timeout; `Esc`, an invalid key or a click elsewhere cancels them. The single table in `frontend/src/lib/keys/keymap.ts` drives the handler, the `?` overlay and the which-key popover.

**Active** (read-only): `j`/`k` move, `gg`/`G` first/last (`12G` = line 12), `Ctrl-d`/`Ctrl-u` half page, `Ctrl-f`/`Ctrl-b` page, `h`/`l` pane focus (`Tab` keeps its normal meaning, so keyboard users are never trapped), `b` show or hide folders, `Enter`/`o` open, `[`/`]` previous/next message, `g i/s/d/t/a/j` go to Inbox/Sent/Drafts/Trash/Archive/Spam, `g l` pick any folder or label, `g r` refresh, `g h` plain text/HTML, `g x` links in this message, `y` copy sender, `/` filter the loaded messages of the folder by subject or sender (Esc clears), `:` command palette (includes "Go to <folder>"; `g l` opens it that way), `?` shortcuts. In the setup wizard: `Enter` continues, `Esc` goes back, `?` and `:` work.

**Reserved for later** (the keys stay free; `i` load remote images is also reserved, see section 5 and are listed greyed in `?`; pressing one shows "Not available yet"): `e` archive, `#` trash, `!` spam, `m` move, `;` label (not `l`, which is pane focus), `s` star, `U` mark unread, `x` select, `u` undo (phase 4); `a` accept suggestion, `S` suggestion panel (phase 3); `r`/`R` reply drafts (phase 5). Destructive actions will always ask for confirmation. Lowercase acts on the current item; uppercase is the broader variant. Remapping in a config file comes later.

**Discoverability, gently.** Tooltips with shortcuts on buttons, the `?` overlay (also the footer button), the which-key popover, and the command palette listing commands. No always-visible hint bar. The setup wizard shows one short line, "Enter continues. Esc goes back."

**Accessibility.** Panes are landmarks; the list is a listbox with `aria-activedescendant` and `aria-posinset`/`aria-setsize`; folder names include the unread count. Errors use `role="alert"`; sync and status use `aria-live="polite"`. Focus moves to the step title when the wizard changes step, overlays trap focus and restore it on close, and a visible focus ring is always shown. All text meets AA contrast in both themes. `prefers-reduced-motion` turns off animations (the sync pulse and transitions). Sizes use `rem`. Single-key shortcuts can later be turned off (WCAG 2.1.4); every function has a mouse path.

## 4. Setup wizard (implemented)

A centred card, one step per screen, "Step N of 4", plain short copy. Driven by `GetStatus`, so a restart resumes at the first unmet step. Enter continues where that is safe, Esc goes back. Enter never trusts a certificate; that needs a click or Enter on the focused button.

1. **Welcome and checks.** Explains that mail is read through Bridge on this computer. Live checks: Bridge reachable, password storage ready. "Check again" and a disabled Continue until both pass.
2. **Bridge account.** Username and password (from Bridge's Mailbox details, not the Proton password). Advanced: host (`127.0.0.1`), IMAP 1143, SMTP 1025, connection type (STARTTLS default, or SSL/TLS). The host is loopback only. When an account already exists, the password may be left empty to keep the saved one. The password is cleared from the form as soon as `SaveAccount` returns, success or not; it is never kept anywhere else.
3. **Check this connection.** `ProbeCert` then `TrustCert(fingerprint, replace)`. Explains that Bridge makes its own certificate, shows the fingerprint (mono, copy button), what it is, and how to compare it with the one exported from Bridge. If a different certificate is already saved (`already_pinned`), the button becomes "Replace saved certificate" with a short warning. If it changed between probe and trust, or the settings changed, nothing is saved and the copy says so.
4. **Test and finish.** `TestConnection`, then "Connected. Your mail will start loading now." and "Open mailbox".

**Errors.** Each appears as a banner with `role="alert"` and a recovery button; no secret is ever shown. Keyring unavailable (unlock KWallet / check Credential Manager; back to checks), Bridge not running (open Bridge and sign in; retry), wrong password (use Mailbox details; back to the password), not trusted yet or certificate changed (review the certificate), invalid settings (edit them), no saved password, and a generic fallback. All `TestConnection` categories are mapped (`frontend/src/lib/setup/errors.ts`) and tested.

## 5. Safe email rendering (implemented)

- `HTMLBody` (already sanitised in Go) goes in `<iframe sandbox srcdoc>` with an empty sandbox list: no scripts, no same-origin, no forms, no popups; a meta CSP `default-src 'none'; img-src data: cid:; style-src 'unsafe-inline'` (`https:` is added to `img-src` only after the user loads images for that message).
- **Scrolling (decided).** A sandboxed frame without `allow-same-origin` cannot be scrolled or watched by the host, so the host does not try. The message header, image banner and attachments stay fixed above; the frame fills the remaining height and scrolls itself (mouse wheel, touchpad, scrollbar). To scroll by keyboard, focus the frame (`Enter` or `l` on the message view, or click it), then the native keys work (arrows, Space, PgUp/PgDn). Keys typed inside the frame never reach temail, so `Tab` or a click leaves it; this is the cost of the sandbox. The frame gets an accessible title ("Message body"). No scripts or `allow-same-origin` are added.
- **Remote images.** Go removes remote images before the message reaches the UI, so the pane shows a quiet note, "Remote images are blocked.", with no button. *Future work:* a per-message "Load images" flow needs Go support (a fetch path that returns the images as `data:` URLs or an allow-listed proxy); the frame CSP would then add those `data:` images only. `cid:` images are not resolved yet and show their alt text.
- **Plain text.** A Formatted / Plain text switch (`g h`) appears when both parts exist; messages with only text, or only HTML, show what they have (plain text from HTML is a simple extract). Plain text is wrapped, proportional, URLs not linked.
- **Links (decided).** Nothing inside the frame can navigate: before rendering, the host (in an inert DOM) removes every `href`, drops scripts, frames, forms, base tags and `on*` attributes, and collects http, https and mailto links. They appear in a "Links (n)" list (`g x`) that shows the text and the real address; clicking one opens it in the system browser through the Wails runtime (`Browser.OpenURL`). A confirm appears only when the link text shows an address (for example `paypal.com`) that is not where the link goes; plain words like "Read more" open directly because the real address was already on screen.
- Attachments are listed with name, type and size; Download calls `GetAttachment`, turns the bytes into a Blob and saves it through the browser's download handling; no path is ever chosen by the page. Large files show "Too large to download here".

## 6. Svelte 5 structure and tests

```
frontend/src/
  App.svelte                 route (loading | setup | main), key handler, overlays
  lib/api.ts                 typed wrappers over the generated Wails bindings and events (mocked in tests)
  lib/keys/keymap.ts         the one key table
  lib/keys/dispatcher.ts     window key handler: counts, sequences, input/IME rules
  lib/stores/*.svelte.ts     app (route), ui (overlay, pending keys, theme), sync (events), mail (folders, list, open message), layout (panes)
  lib/setup/                 wizard.svelte.ts (step logic), errors.ts (copy)
  lib/mail/                  folders, format, virtual (list window math), render (frame HTML), links
  lib/components/            Wizard, MainView, Toolbar, FolderPane, MessageList, MessagePane, Resizer, Footer, SyncPanel, HelpOverlay, Palette, WhichKey, ErrorBanner
  styles/                    tokens.css, base.css
```

Stores are classes with `$state` fields. Tests (vitest, jsdom, Testing Library) mock `lib/api`: wizard step logic and every error category, the key handler (counts, g-prefix, timeout, inputs, IME, contexts, overlays), keymap table sanity, theme tokens and contrast, and app routing and wizard flows. Real Wails calls are not tested here.

## 7. Decided answers

`l` is pane focus (labels use `;` later). Three panes by default, single pane on small windows. Flat list (no threads). Follow the system theme. `/` is a client-side subject filter. Bundled Plex Sans and JetBrains Mono. Confirm link opening only when the text and the target differ. Remappable keys later. Violet accent.

## 8. Known gaps

- **Unread counts.** `MailService.ListMailboxes` returns `messages` and `bodies` but no unread count, so the folder pane shows total messages. Unread counts need a Go field (`unseen`).
- **Remote images** cannot be loaded per message yet (section 5).
- **List refresh** after sync merges new mail at the top and updates flags; messages deleted on the server disappear on the next folder load.
