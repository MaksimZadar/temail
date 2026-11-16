# internal/actions

User actions against Proton Bridge, with verification, a cache update and an
audit log with undo. This is the only package that calls the write-capable
`mail.Writer`. It has no UI or Wails knowledge. See `docs/ARCHITECTURE.md` §3.3
and §7.

## Action types (v1)

`mark_read`, `mark_unread`, `star`, `unstar`, `archive`, `move{folder}`, `trash`,
`spam`, `not_spam`, `add_label{label}`, `remove_label{label}`.

One `Action` is one batch: one type, typed params, and up to `MaxBatch` (100)
stable message ids (`mid`, the Proton internal id from `X-Pm-Internal-Id`). The
current UID of every message is resolved from the cache at apply time; a UID
from the frontend is never trusted. There is no permanent delete, no label
create/rename and no draft handling in this lane.

`Result{Applied []mid, Failed []Failure{MID, Mailbox, Message}}`: failures are
visible per message, and every failed write or verification is also logged. The
error return is reserved for a refused action, a cancelled call, or a failure to
reach Bridge before any write.

## Recipes

The researched Bridge behaviour, implemented in `apply.go`:

| Action | Recipe |
|---|---|
| mark_read / mark_unread | One `STORE ±\Seen` on a UID set. Runs in All Mail when the message is there, otherwise in one placement. `\Seen` is global per message. |
| star / unstar | One `STORE ±\Flagged`, same mailbox choice. `\Flagged` is global. |
| archive | `MOVE` to `Archive` from the message's system/folder placement. Bridge's Archive behaviour is unverified: a `NO` answer falls back to `COPY`, and the source placement stays. From All Mail (no folder placement) it uses `COPY` directly. |
| move{folder} | `MOVE` to `Folders/X`. The target must be a cached folder; labels go through add_label. From All Mail it uses `COPY` (Bridge refuses MOVE from All Mail). |
| trash / spam / not_spam | `MOVE` to `Trash` / `Spam` / `INBOX`. From All Mail it uses `COPY`. |
| add_label{label} | `COPY` into `Labels/<label>`; the label must already exist. The copy's COPYUID is checked. |
| remove_label{label} | `STORE +\Deleted` then `UID EXPUNGE` **inside `Labels/<label>` only**. This is the only expunge path. It unlabels the message; other placements stay. |

Never expunged: archive, move, trash and spam. The static test
`TestNoExpungeOutsideLabelPath` fails if an expunge call appears anywhere except
`applyRemoveLabel`. `mail.Writer.ExpungeDeleted` additionally refuses when the
server lacks UIDPLUS/IMAP4rev2, because a plain `EXPUNGE` would remove every
`\Deleted` message in the mailbox.

## Verification

Every write is verified before it counts as applied:

- A COPY or MOVE COPYUID (UIDPLUS) is the server's own confirmation. When it is
  absent, `UID SEARCH HEADER X-Pm-Internal-Id <mid>` in the target mailbox is the
  fallback. A synthetic mid (a message with no Proton id) can only be verified
  through COPYUID; without it the write is reported as unverified, never assumed.
- Flag writes re-read the flag set of the written UIDs.
- After a MOVE, the source removal is eventual: the source is rechecked after
  ~1 s, up to 2 retries. A message still in the source is a failure.
- A write that cannot be verified is a `Failure` in the result and an audit row
  with `verified = 0` and the error text.

## Cache update

After verification, placements and flags are updated in one store transaction
(`store.ApplyActionUpdates`), so the membership counters that the schema triggers
maintain stay consistent. `mark_read`/`star` update the message's global flags;
moves remove the source placement and add the target placement with the COPYUID
destination UID; add_label adds a placement; remove_label removes one. The
watcher's reconcile remains the backstop. A trash/spam move may also drop the
message's labels on the Proton side; this layer removes only the source placement
it moved from and leaves the rest to the watcher, so the cache never claims a
label was removed before the server says so.

## Audit and undo

`audit.db` is a separate SQLite file in the same data directory as `cache.db`
(`store.DefaultAuditPath`). Deleting the cache never removes it. The `actions`
table holds metadata only — timestamp, account, type, params JSON, inverse JSON,
mid, before/after mailbox-and-flag JSON, verified, error, undone. It never holds
subjects, bodies or secrets.

Inverses: `mark_read ↔ mark_unread`, `star ↔ unstar`,
`move/archive/trash/spam/not_spam → move` back to the before-folder,
`add_label ↔ remove_label`.

Undo resolves the message's **current** UIDs from the cache by mid (UIDs change
across moves and resyncs) and runs the inverse through the same verified write
path. The original entry is marked undone only when the inverse is verified.
`Recent(limit)` lists the newest entries; `Undo(id)` and `UndoLast()` apply an
inverse. An archive or trash that had to use COPY from All Mail has no inverse
(undoing it would need an expunge outside the label/trash paths); `Undo` then
returns `ErrNotUndoable` and the audit DTO marks the row `undone: false`.

The audit log keeps one row per message, so `Recent`'s `count` is 1 per row;
grouping a batch is the frontend's presentation choice.

## Batching, politeness and auth

- At most 100 ids per action; duplicates are removed.
- Writes are grouped by placement mailbox: one UID STORE/MOVE/COPY per source
  mailbox. The executor serialises all writes on one connection.
- A 100 ms pause after each mailbox write is kind to Bridge; it honours the
  context.
- The writer shares the app's `AuthLatch` (`sync.CredsKey`), so a refused login
  trips every login path. An auth failure stops the batch and is returned.
- Cancelling the context stops the batch; successful writes are still audited
  (audit and cache writes use a non-cancelled context).

## Tests

`actions_test.go` uses a stateful fake writer that models the researched traps:
MOVE from All Mail refused, MOVE to Archive refused (COPY fallback), EXPUNGE
unlabels only, optional missing COPYUID, and delayed source removal. With a real
SQLite cache, the tests cover every recipe, verification (including header-search
fallback), a failed verification, cache updates and counters, audit rows, undo
with changed UIDs, batch cap, coalescing of duplicates, wildcard moves, and
concurrent applies under `-race`. The static guard checks the expunge call site.

## What only a real Bridge can confirm

The fake models the researched semantics, not Bridge itself. A real Bridge is
needed to confirm: Archive accepts MOVE (or only COPY), MOVE from All Mail is
refused as documented, COPYUID is reported for every COPY/MOVE, UID EXPUNGE
inside a label removes only that label placement, flag STOREs from All Mail
stick globally, and how quickly a MOVE's source UID disappears.

`cmd/actionprobe` exercises exactly these recipes on one chosen message; see its
README.
