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
current UID of every message is resolved from the cache at apply time; a UID or
epoch from the frontend is never trusted. There is no permanent delete, no label
create/rename and no draft handling in this lane.

`Result{Applied []mid, Failed []Failure{MID, Mailbox, Message}, BatchID,
Entries []ResultEntry{MID, AuditID, Undoable}}`. Failures are visible per
message, and every write, partial write and verification failure is also logged.
The error return is reserved for a refused action, a cancelled call, or a
failure to reach Bridge before any write.

## Epochs, identity and plans

Every piece of work is planned from cache placements, and a placement carries
the UIDVALIDITY epoch its UID belongs to. `store.MembershipOfMessages` joins
`placements.uidvalidity = mailboxes.uidvalidity`, so a stale placement from an
earlier epoch is never even considered.

Every writer call takes that epoch. `mail.Writer` compares it with the
SELECT/EXAMINE answer and refuses with `mail.ErrEpochChanged` before writing.
That is a per-message failure and marks the mailbox for a resync (the service
resets `last_sync` and kicks the watcher).

A move (MOVE or COPY) and a label expunge additionally prove identity first: one
`BODY.PEEK[HEADER.FIELDS (X-Pm-Internal-Id Message-ID)]` fetch per group must
return the cached mid, with the Message-ID form as fallback. A mismatch is a
per-message failure and nothing is written for it. A synthetic mid (a message
with no Proton id) has no identity header; it relies on the epoch check and on
COPYUID pairing for its destination.

Move-family actions and label copies refuse messages whose source or target
mailbox has never completed a sync (`LastSync` zero): the UID/epoch would be a
guess.

## Recipes

| Action | Recipe |
|---|---|
| mark_read / mark_unread | One `STORE ±\Seen` on a UID set. Runs in All Mail when the message is there, otherwise in one placement. `\Seen` is global per message. A message already in the desired state is a no-op: no write, no inverse. |
| star / unstar | One `STORE ±\Flagged`, same mailbox choice. `\Flagged` is global. A no-op follows the same rule. |
| archive | `MOVE` to `Archive` from the message's system/folder placement. Bridge's Archive behaviour is unverified: a `NO` answer falls back to `COPY`, and the source placement stays. From All Mail (no folder placement) it uses `COPY` directly. |
| move{folder} | `MOVE` to `Folders/X`. The target must be a cached folder; labels go through add_label. From All Mail it uses `COPY` (Bridge refuses MOVE from All Mail). |
| trash / spam / not_spam | `MOVE` to `Trash` / `Spam` / `INBOX`. From All Mail it uses `COPY`. |
| add_label{label} | `COPY` into `Labels/<label>`; the label must already exist. |
| remove_label{label} | Identity check, then `STORE +\Deleted` and `UID EXPUNGE` **inside `Labels/<label>` only**. This is the only expunge path. |

**A COPY fallback is a partial outcome, never a success.** The copy is
confirmed, so its target placement is cached, but the source copy remains. The
message appears in `Failed` with, for example, "archived copy created; original
still in INBOX — try again after the folders finish syncing", the audit row says
`unknown outcome`, and the source is marked for a resync.

Never expunged: archive, move, trash and spam. The static test
`TestNoExpungeOutsideLabelPath` fails if an expunge call appears anywhere except
`applyRemoveLabel`. `mail.Writer.ExpungeDeleted` additionally refuses when the
server lacks UIDPLUS/IMAP4rev2, because a plain `EXPUNGE` would remove every
`\Deleted` message in the mailbox.

## Verification and destination UIDs

- After a COPY or MOVE the destination is found by `UID SEARCH HEADER
  X-Pm-Internal-Id <mid>` in the target and must return **exactly one** UID.
  There is no zip-by-index mapping of COPYUID. Zero hits is a failure; more than
  one is a failure (never a guess). A synthetic mid uses the COPYUID
  source-to-destination pairing instead, because no header search can find it.
- When the server reports COPYUID, its `UIDValidity` must match the cached
  target epoch. A mismatch means the cache's target is from another epoch:
  nothing is written, the outcome is `unknown outcome`, and the target is marked
  for a resync.
- Flag writes re-read the flag set of the written UIDs.
- After a MOVE, the source removal is eventual: the source is rechecked after
  ~1 s, up to 2 retries. A message still in the source is a **partial** write:
  the confirmed target placement is cached, the message is reported failed, and
  both mailboxes are marked for a resync.
- remove_label rechecks that the UID left the label and that the identity search
  finds nothing.

## Cache update

After verification, placements and flags are updated in one store transaction
(`store.ApplyActionUpdates`), so the membership counters that the schema triggers
maintain stay consistent. The target placement is written with the epoch the
write used (`ActionUpdate.AddUIDValidity`), not with whatever the cache row says
later. `mark_read`/`star` update the message's global flags; moves remove the
source placement and add the target placement; add_label adds a placement;
remove_label removes one. The watcher's reconcile remains the backstop. A
trash/spam move may also drop the message's labels on the Proton side; this layer
removes only the source placement it moved from and leaves the rest to the
watcher.

## Audit and undo

`audit.db` is a separate SQLite file in the same data directory as `cache.db`
(`store.DefaultAuditPath`). Deleting the cache never removes it. The `actions`
table holds metadata only — timestamp, account, type, params JSON, inverse JSON,
mid, before/after mailbox-and-flag JSON, verified, error, undone, `batch_id` and
`undo_of`. It never holds subjects, bodies or secrets.

Durability: for every group, rows are inserted **before** the write with
`verified = 0` and finalised after it with the real outcome. A crash, a cancelled
context or a lost connection therefore leaves evidence. Audit and cache writes
use a context without cancellation.

Batching: one action is one `batch_id`. `Recent` returns one row per batch with
`count` and the undoable `auditIds`; `UndoLast` undoes the whole newest verified
batch. The undo actions are recorded in a new batch with empty inverses, so they
are not undoable again — there is no redo loop. `Recent` and `UndoLast` are
scoped to the executor's account.

Inverses: `mark_read ↔ mark_unread`, `star ↔ unstar`,
`move/archive/trash/spam/not_spam → move` back to the before-folder,
`add_label ↔ remove_label`. An inverse is recorded only when the before-state
differed: a no-op has no inverse.

Undo resolves the message's **current** UIDs from the cache by mid and runs the
inverse through the same verified write path. Before writing it compares the
current cache state with the entry's recorded after-state. If the user changed
the message in between, undo refuses with `ErrDiverged` (a kind of
`ErrNotUndoable`) and changes nothing. The original entry is marked undone only
when the inverse is verified.

A COPY-fallback archive or trash has no inverse (undoing it would need an
expunge outside the label/trash paths); `Undo` returns `ErrNotUndoable`, and the
service maps the reason to a `code` (`not_undoable`, `already_undone`,
`not_found`, `wrong_account`, `diverged`).

## Batching, connections and politeness

- At most 100 ids per action; duplicates are removed.
- Writes are grouped by source mailbox **and epoch**: one UID STORE/MOVE/COPY
  per group. The executor serialises all writes on one connection.
- The connection is re-established before a group when needed. A protocol
  answer (`NO`/`BAD`, or a typed refusal such as `ErrEpochChanged` or
  `ErrExpungeUnsupported`) does **not** drop the connection; the group fails and
  the next group continues. A transport error, a verification/search/fetch
  error, or a cancelled context closes it, so the next group reconnects in a
  known state instead of failing with `ErrNotConnected`.
- `mail.Writer` remembers the selected mailbox (with its epoch and writable
  mode) and reuses it, so one action group does not SELECT/EXAMINE the same
  mailbox for every call. The recheck after a MOVE reuses the source selection
  when it is still current.
- A 100 ms pause after each mailbox write is kind to Bridge; it honours the
  context.
- The writer shares the app's `AuthLatch` (`sync.CredsKey`), so a refused login
  trips every login path; the latch still limits a refused login to one strike
  per app session. An auth failure stops the batch.
- Cancelling the context stops the batch; successful writes are still audited
  and cached (audit and cache writes use a non-cancelled context), and any
  uncertain source state marks its mailbox for a resync.

## Tests

`actions_test.go` and `regress_test.go` use a stateful fake writer that models
the researched traps and the regression cases: MOVE from All Mail refused, MOVE
to Archive refused (COPY fallback), EXPUNGE unlabels only, missing COPYUID,
missing UIDPLUS, delayed source removal, a UIDVALIDITY bump, a `NO` on one
group with a later group succeeding, a transport error with a reconnect, a
multi-hit identity search, an ambiguous destination search, a COPYUID epoch
mismatch, and partial writes. With a real SQLite cache, the tests cover every
recipe, verification, cache updates and counters, audit-before-write, batch
grouping, undo and divergence, account scoping, no-op inverses, the batch cap and
concurrent applies under `-race`. The store tests cover the stale-epoch join. The
static guard checks the expunge call site.

## What only a real Bridge can confirm

The fake models the researched semantics, not Bridge itself. A real Bridge is
needed to confirm: Archive accepts MOVE (or only COPY), MOVE from All Mail is
refused as documented, COPYUID is reported for every COPY/MOVE, UID EXPUNGE
inside a label removes only that label placement, flag STOREs from All Mail
stick globally, and how quickly a MOVE's source UID disappears.

`cmd/actionprobe` exercises exactly these recipes on one chosen message; see its
README.
