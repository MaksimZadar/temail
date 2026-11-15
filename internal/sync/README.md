# internal/sync

Copies mail from Proton Bridge into the cache (`internal/store`). It uses the
read-only `internal/mail` client and has no UI, Wails or LLM knowledge.

## Membership model

Bridge gives one message a different UID in every mailbox it is in, so the sync
works in two layers:

- **Placements.** Every mailbox is listed and its headers are fetched, so each
  copy becomes a placement (`mailbox`, `UIDVALIDITY`, `UID`) on a message. The
  message key is the Proton internal id (`X-Pm-Internal-Id`, or a
  `<id@protonmail.internalid>` Message-ID). This is what makes folder and labels
  visible.
- **Bodies, once per message.** The body queue is per message. A message is
  fetched through its All Mail placement when it has one, and through any other
  placement otherwise. Recovered Messages alone is skipped.

**Priority order** (`orderMailboxes`): All Mail first, since it holds every
message; then INBOX, Drafts, Sent, Archive, Spam, Trash and Starred; then
`Folders/*`; then `Labels/*`; then any other mailbox; then Recovered Messages.
If All Mail is hidden in Bridge, it is absent from the list and the others sync
the same way.

**Reconcile per mailbox** removes placements the server no longer lists. Deleting
the last placement of a message removes the message and its body. A message that
is still in another mailbox stays. Flags are global, so reconcile writes them to
the message. The unseen count of the mailbox is stored from its listing
(`SetUnseen`), so it costs no extra request.

**Progress.** Mailbox progress is unchanged in shape. Body progress counts
messages (entities), so the totals are cached messages and cached bodies.

**Known gap.** A placement whose UID is reused with another message (not seen
in practice, since UIDs only grow within an epoch) would leave the old message
without a placement until a later purge. Nothing else depends on it.

## Guarantees

- **Read-only on the mailbox.** The sync calls only `ListMailboxes`,
  `ListUIDs`, `FetchHeaders` and `FetchMessage`. Those are the read operations
  in `mail.Client`, and the mail package tests check them. It never sets flags
  or moves messages. It writes only to the cache.
- **Cache consistency.** Each write is its own transaction. A cancelled or
  failed run leaves the cache with whole batches, never part of one.
- **Single connection.** One `mail.Client` is used. A failed operation
  reconnects that connection. A `Syncer` must not run two `Run` calls at once.
- **No attachment content.** Only attachment metadata is cached.

## Run

`Syncer.Run(ctx)` does four steps. Each one commits as it goes.

1. **Mailboxes.** `ListMailboxes`, then `UpsertMailboxes`. If a UIDVALIDITY
   changed, the store purges that mailbox in the same transaction. Mailboxes
   the server no longer lists are removed with `DeleteMailboxesNotIn`. The
   order is the priority order below.
2. **Headers, per mailbox.**
   - `ListUIDs` gives the server's UIDs, UIDVALIDITY and flags, in one pass.
     This is the listing for steps 2 and 3.
   - `CachedUIDs` gives the cached UIDs of the same epoch.
   - `planChunks` takes the server UIDs that are not cached, as runs. A cached
     UID ends a run, so no fetch window spans committed mail. Each run is cut
     into windows of `mail.BatchSize` (200) UIDs, starting from the top.
   - Windows are fetched newest first. Each one is written in its own
     transaction before the next is fetched. The UI can show the newest mail
     after the first window.
3. **Reconcile, per mailbox.** Cached UIDs that are not in the listing are
   deleted (`DeleteMissing`, in transactions of at most 1000 rows). The
   cached flags are read (`CachedFlags`), and only rows whose flags differ
   from the listing are written (`UpdateFlags`). Only then is `last_sync`
   set. A mailbox that was interrupted before this point keeps its old
   `last_sync`. The listing costs one `UID FETCH 1:* (UID FLAGS)` per mailbox.
4. **Bodies, account-wide.** `PendingBodies` pages through the messages with
   no body, newest first, `BodyBatchSize` (10) at a time. The page cursor is
   the last message of the previous page. Each message is fetched with
   `FetchMessage` and stored with `SetBody`. There is a `BodyDelay` (default
   500 ms) before every fetch except the first. The loop ends when a page is
   empty.

### Mailbox failures

A mailbox that fails for a reason other than a lost connection or a cancelled
context (a server NO or BAD, a UIDVALIDITY of 0, a parse error) is recorded
in `Result.MailboxErrors`. The run continues with the other mailboxes and
with the body phase. `Run` then returns `ErrPartial`. The caller should show
the errors and run again.

A lost connection that cannot be restored, or a cancelled context, ends the
run at once. Those are not per-mailbox problems.

### Empty and zero listings

- An empty mailbox listing from the server is an error. Nothing is cached or
  removed.
- A UIDVALIDITY of 0 is not an epoch. The mailbox is recorded as a mailbox
  error and nothing is cached under 0. The store also treats 0 as unknown and
  never purges because of it.

## Resume semantics

Progress is kept in the cache. There is no separate checkpoint. The rule is:
**a UID that is in the cache for the current epoch is never fetched again.**

- Interrupting a run (Ctrl-C, cancel, error) loses at most the window in
  flight. Everything committed stays.
- Running again lists the server again. Pending work is the set difference
  between the server's UIDs and the cache, so committed headers are not
  fetched again. The interrupted window is fetched again.
- Mail that arrived during the interruption is picked up in the same way. It
  has the highest UIDs, so it is in the first window.
- Bodies follow the same rule. A message with a body is never offered again.
  A message whose body fetch failed is offered again on later runs, but only
  its deterministic failures count toward `store.MaxBodyAttempts` (3). A
  deterministic failure is "message not found" or "could not be parsed". After
  the bound the message is left alone until its mailbox is purged. Other
  failures (a server NO during Bridge startup, unknown errors) are not
  counted, so they cannot permanently blank a body.
- Within one run, a failed body is not offered again: paging uses the cursor,
  so each message is attempted at most once per run.

Tests cover this. An interrupted header run resumes with each UID fetched once
across both runs. An interrupted body run does the same. Both use an exact
fetch count per UID.

### Why no watermarks

The plan calls for both a "newest synced" and a "backfill low-water mark" per
mailbox. This implementation does not keep them. The reason is that the
cache already records what is committed, and watermarks would copy that state
and could drift from it. The set difference against `ListUIDs` is correct
after any interruption. It also handles expunged UIDs and new mail with no
extra logic, and it costs one `ListUIDs` and one local read per mailbox. The
`highest_uid` column is still kept in the store as a cheap diagnostic.

## Retries and reconnects

- Only transport errors are retried: network and IO errors, a closed
  connection, or no connection (`isTransport`). A failed operation waits
  `Backoff` (default 1 s, doubled each attempt, cap 30 s), reconnects, and
  runs again. This happens up to `Retries` times.
- Everything else returns at once, with no reconnect. That covers server NO
  and BAD replies, parse errors and "not found".
- `Retries` is a `*int`. `nil` means the default (3). `RetryCount(0)` means no
  retries. The zero value is not "use the default".
- Retries never apply to permanent errors: `ErrUntrustedCert`,
  `ErrCertMismatch`, `ErrNotLoopback` and `ErrAuthFailed` (a rejected Bridge
  password). Those need user action, so retrying every run is pointless.
- A connection that cannot be restored ends the run with an error. The cache
  keeps what it has, and the next run resumes.
- In the body step, a message that fails while the connection is up is
  counted in `BodiesFailed` and skipped for this run. Only deterministic
  failures are counted toward the attempt bound. A dead Bridge is never
  mistaken for a bad message, because a failed reconnect aborts the run.

## Progress

`Options.OnProgress` receives `Progress` events on the goroutine that runs
`Run`:

| State | Meaning | Counts |
|-------|---------|--------|
| `connecting`, `mailboxes`, `reconciling`, `done` | Account-level steps (`reconciling` is also emitted per mailbox) | |
| `headers` | Header windows for one mailbox | `HeadersDone` / `HeadersTotal` (this run's pending UIDs) |
| `bodies` | Bodies for one mailbox | `BodiesDone` (cached bodies) / `BodiesTotal` (cached messages) |

## Watching for changes (phase P4 PR B)

After the first sync, `Watcher` keeps the cache current. It has two triggers.

- **IDLE** on a second connection, on All Mail, or on INBOX when All Mail is
  hidden. go-imap re-issues IDLE before the server's timeout. A new message,
  an expunge or a flag change sends a signal to the check.
- **Reconcile timer.** Every 120 s with IDLE, or every 30 s without it. It is a
  backstop for changes IDLE does not report.

A **check** (`Syncer.Check`) uses `LIST` and `STATUS` only. It never SELECTs a
mailbox, so All Mail is not built as a snapshot. For each cached mailbox it
compares the server with the cache:

| Server | Cache | Result |
|--------|-------|--------|
| mailbox list differs | | full `Run` (new or removed mailbox) |
| UIDVALIDITY differs | | that mailbox is resynced (its placements are purged) |
| UIDNEXT-1 above highest UID | | new mail: that mailbox is synced |
| MESSAGES differs | | a message was added or removed: synced |
| UNSEEN differs | | a read or unread change: synced |
| never synced | | synced |

A changed mailbox is synced incrementally by `RunChanged`: the planner fetches
only UIDs that are not cached, the reconcile removes what is gone, and the body
queue holds only messages without a body. The result is then reported.

**Flags only.** A change to `\Flagged` alone does not move any of the counts
above, so `STATUS` cannot see it. IDLE does see it, and the next check picks it
up. If IDLE is not available, such a change waits for the next full sync.

**Bursts.** A signal waits for `Debounce` (500 ms) and drains any others, so a
burst is one check.

**Errors.** A transport failure backs off (doubling, up to five minutes) and
retries. A check that fails with a per-mailbox error is reported in
`mailboxErrors` and does not stop the watcher. An authentication failure or a
certificate error stops the watcher, and the service emits `sync:error`. Those
errors are not retried, because each failed login counts toward Bridge's limit.

**Shutdown.** `Run` returns only after the IDLE goroutine has ended and its
connection is closed (`TestShutdownLeavesNoIdleGoroutine`).

**No IDLE.** When the server does not offer IDLE, or Bridge has turned it off,
the watcher does not start an IDLE connection. It polls at the shorter period
(`TestIdleLessFallbackPolls`).

## Future work

- **Body dedupe by Message-ID.** In Proton, a message in "All Mail" and in
  each label or folder that holds it appears as a separate UID. Each copy
  currently gets its own body fetch. Fetching one body per Message-ID would
  save bandwidth and Bridge load. It needs a policy for per-copy state (flags
  and mailbox membership stay per copy; only the body is shared). It is not
  implemented.

## Tests

`watch_test.go` covers an IDLE update that triggers an incremental sync (cached
messages are not fetched again), each STATUS difference case, an unchanged check
that issues no UID listing, a mailbox-list change that needs a full run, a check
that never SELECTs All Mail, the IDLE-less poll, the auth failure stopping the
watcher with one login attempt, shutdown with no goroutine left running, a
dropped IDLE reconnecting, a transport failure retried, and a burst of updates
being one check. The `-race` run was repeated 10 times with no failures.

`membership_test.go` covers one message in All Mail, INBOX and a label (one
entity, one body fetch, the membership, the global flags), deletion of one copy,
UIDVALIDITY of one mailbox leaving shared messages alone, the priority order,
hidden All Mail, Recovered Messages bodies being skipped, unseen per mailbox, and
resume with one body per message.

`sync_test.go` runs against an in-memory fake `mail.Client` (`fake_test.go`)
and a real SQLite cache in `t.TempDir()`. The fake records every fetch by
mailbox and UID, and the order of body fetches. It can drop the connection,
fail Connect, and reject individual bodies. The tests cover:

- planning: windows are newest first and never contain a cached UID;
- a full run: counts, `last_sync`, state order;
- newest-first header order, and the newest messages visible after the first window;
- UIDVALIDITY change: the mailbox is purged and resynced, and old and new epochs do not mix;
- server deletions and flag changes, with no header refetch of unchanged messages;
- new mail on a later run, fetched without refetching the cache;
- interrupt and resume for headers and bodies, with exact fetch counts;
- body throttling: a delay between fetches, none before the first, batches of `BodyBatchSize`;
- a deterministic bad body is attempted once per run and dropped after the bound;
  a bad body does not block later ones in small batches;
- a transient body error is not counted;
- a server NO on a mailbox is not retried or reconnected, and the run continues
  past it (`ErrPartial`); the body phase still runs;
- a transient connection drop reconnects; a persistent one aborts without corrupting the cache;
- an untrusted certificate and a rejected password are not retried; `RetryCount(0)` disables retries;
- an empty listing removes nothing; UIDVALIDITY 0 is not cached;
- reconcile writes only changed flags and lists each mailbox once;
- cancellation during the body step.

Run with `go test -race ./internal/sync/`.

## Manual tool

`cmd/mailsync` runs a full sync against the real Bridge. It reads the account
from `config.json`, the password from the keyring (or
`TEMAIL_BRIDGE_PASSWORD`), and pins the certificate with `-trust` as
`mailprobe` does. It takes `-cache` and `-body-delay`, and prints progress and
per-mailbox counts at the end. It is safe to Ctrl-C and to run again.
