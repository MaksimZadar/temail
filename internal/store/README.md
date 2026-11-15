# internal/store

SQLite cache for temail (`modernc.org/sqlite`, pure Go, no cgo). It holds the
mailbox list, message headers, sanitised bodies and attachment metadata. It is
disposable: deleting it loses nothing on the server, and the next sync rebuilds
it. The audit log will live in a separate file (`audit.db`) and is not part of
this package.

## Location

`DefaultPath()` returns the data directory, not the config directory:

| OS | Path |
|----|------|
| Linux | `$XDG_DATA_HOME/temail/cache.db`. If `XDG_DATA_HOME` is unset or relative, `~/.local/share/temail/cache.db` |
| Windows | `%LOCALAPPDATA%\temail\cache.db` |

`Open(path)` takes an explicit path, which the tests use. It creates the
directory with mode 0700 and the file with mode 0600. SQLite creates the WAL
and SHM files with the same mode.

## Connection settings

The DSN is a `file:` URI built with `net/url`, so a path with `?`, `#`, `%`
or spaces opens the file it names. Pragmas are set on every connection:

- `journal_mode(WAL)`: readers (the UI) do not block the sync writer.
- `foreign_keys(1)`: deleting a mailbox removes its messages, and deleting a
  message removes its body.
- `busy_timeout(5000)` and `synchronous(NORMAL)`.

### First open and SQLITE_BUSY

The first connection to a new file switches it to WAL. That switch can return
`SQLITE_BUSY` immediately, without using `busy_timeout`, while another
connection (the app and `cmd/mailsync` can both open the cache) is opening the
same file. Measured on this repo: 59 of 800 concurrent first opens failed with
the WAL pragma in the DSN, and none failed without it. So `Open` retries the
connect-and-ping step, and the first migration, on `SQLITE_BUSY` or
`SQLITE_LOCKED`. Waits grow from 5 ms to 200 ms, for at most 15 s. Each attempt
opens a new `sql.DB`, and the WAL switch is idempotent, so a retry is safe.
Other errors are not retried.
- `_txlock=immediate`: write transactions take the write lock at `BEGIN`, so
  they wait instead of failing with `SQLITE_BUSY` on upgrade.

## Schema (version 2)

Versioned with `PRAGMA user_version`. `migrations` in `schema.go` is an
ordered list, and `migrations[i]` takes the database from version `i` to `i+1`.
Each migration runs in one transaction with its version bump. Migrations are
applied in a loop: each step opens an `IMMEDIATE` transaction (write lock)
and reads `user_version` inside it, so two processes opening the same new file
cannot apply the same migration twice. A database whose version is newer than
this build is refused with an error. Shipped migrations are never edited.

- v1: tables and the first body index.
- v2: rebuilds `messages_body_pending` for the cursor order used by
  `PendingBodies`. It was added after v1 was written on an unmerged branch.

```
mailboxes(account, name, kind, display_name, uidvalidity,
          highest_uid, last_sync)                 PK (account, name)
messages(account, mailbox, uidvalidity, uid, message_id, subject,
         from_json, to_json, date, flags, in_reply_to, refs,
         body_state, body_attempts)               PK (account, mailbox, uidvalidity, uid)
                                                  FK (account, mailbox) -> mailboxes ON DELETE CASCADE
bodies(account, mailbox, uidvalidity, uid,
       text_gz, html_gz, attachments_meta_json)   PK = messages key
                                                  FK -> messages ON DELETE CASCADE
```

Indexes:

- `messages_mailbox_date (account, mailbox, date DESC, uid DESC)`: list pages.
- `messages_message_id (message_id)`: stable identity lookups (undo), not
  unique and not a key.
- `messages_body_pending (account, date DESC, mailbox DESC, uidvalidity DESC, uid DESC) WHERE body_state = 'none'`:
  the body queue, in the order `PendingBodies` pages through. It is a partial
  index, so it shrinks as bodies arrive.

Column notes:

- `date` is Unix milliseconds. The zero time is 0.
- `from_json`, `to_json` and `flags` are JSON arrays. Flags are stored sorted
  and deduplicated, so the same set always encodes the same way.
- `body_state` is `none` or `cached`. `body_attempts` counts failed body fetches.
- `highest_uid` is the largest cached UID of the current epoch. It is kept for
  display and diagnostics. The sync does not use it to plan work (see
  `internal/sync/README.md`).
- `last_sync` is set only when a mailbox's header and reconcile steps complete.
  It is reset to 0 when the mailbox is purged.
- `text_gz` and `html_gz` are gzip (stdlib, best compression). An empty string
  is stored as NULL. `attachments_meta_json` holds filename, content type, size
  and part ID only. Attachment content is never cached.

## Identity and epochs

A message is keyed by `(account, mailbox, UIDVALIDITY, UID)`. When a mailbox's
UIDVALIDITY changes, `UpsertMailboxes` purges that mailbox in the same
transaction. Its messages, bodies and sync state go, and the mailbox row stays
with the new value. Nothing is remapped.

A UIDVALIDITY of 0 means unknown. `UpsertMailboxes` never purges because of
0, and it never overwrites a stored value with 0. The sync also refuses to
cache a mailbox whose server reports 0.

`UpsertHeaders` rejects a header whose UIDVALIDITY does not match the
mailbox's current one, so a stale fetch cannot write into a new epoch.

## API

`Store` is an interface, and `Open` returns the SQLite implementation. Each
write method runs in one transaction. Batch writes (`UpsertHeaders`,
`UpdateFlags`, `DeleteMissing`) are atomic per call.

| Method | Purpose |
|--------|---------|
| `UpsertMailboxes` | Insert or update mailboxes. Purges a mailbox whose UIDVALIDITY changed (in chunks, before the row is updated). |
| `DeleteMailboxesNotIn` | Drop mailboxes that the server no longer lists. Refuses an empty list (`ErrEmptyListing`). Mailboxes are purged in chunks first. |
| `GetSyncState` | Per-mailbox UIDVALIDITY, highest UID, last sync time. |
| `MarkSynced` | Record a completed sync of a mailbox. |
| `PurgeMailbox` | Delete a mailbox's messages and bodies (in chunks of 1000 rows). Keep the row. |
| `CachedUIDs` | UIDs cached for one epoch, ascending. |
| `CachedFlags` | Cached flags of one epoch, by UID. Used to write only changed flags. |
| `UpsertHeaders` | Batch insert or replace headers. Keeps `body_state`. |
| `DeleteMissing` | Delete cached UIDs that are not in the keep set, in transactions of at most 1000 rows. |
| `UpdateFlags` | Set flags on cached messages. Unknown UIDs are ignored. |
| `ListMessages` | Headers of one mailbox, newest first. Keyset pagination with `Cursor{Date, UID}`. Limit 1 to 1000. |
| `GetMessage` / `GetBody` | One header or body. `ErrNotFound` if absent. |
| `SetBody` | Store a body and set `body_state = cached`. |
| `MarkBodyFailed` | Count a failed body fetch. |
| `PendingBodies` | Up to N messages with no body and fewer than `MaxBodyAttempts` (3) failures, newest first. Takes a `*PendingCursor` (the last message of the previous page; nil for the first page), so paging visits each message once. |
| `Counts` | Messages and cached bodies per mailbox. |
| `CacheStats` | On-disk bytes (db, `-wal`, `-shm`) and the message and body totals of one account. Index-only counts, see `sqlite.go`. |

## Tests

`store_test.go` and `regress_test.go` run on SQLite files in `t.TempDir()`.
`regress_test.go` covers paths with URI-special characters, concurrent first
open, upgrade from v1, UIDVALIDITY 0, chunked purge and delete, the flags read,
and cursor paging. It covers file and
directory modes, WAL, foreign keys, the schema version and refusal of newer
versions, keyset paging with equal dates, purge on UIDVALIDITY change, cascade
on purge, the body round trip through gzip, the attempt bound, flag
normalisation, and DeleteMissing with a keep set above the parameter limit.
`TestDefaultPathLinuxXDG` covers the path rules on Linux. The tests run with
`go test -race`.
