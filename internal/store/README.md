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

Set on every connection through the DSN:

- `journal_mode(WAL)`: readers (the UI) do not block the sync writer.
- `foreign_keys(1)`: deleting a mailbox removes its messages, and deleting a
  message removes its body.
- `busy_timeout(5000)` and `synchronous(NORMAL)`.
- `_txlock=immediate`: write transactions take the write lock at `BEGIN`, so
  they wait instead of failing with `SQLITE_BUSY` on upgrade.

## Schema (version 1)

Versioned with `PRAGMA user_version`. `migrations` in `schema.go` is an
ordered list, and `migrations[i]` takes the database from version `i` to `i+1`.
Each migration runs in one transaction with its version bump. A database whose
version is newer than this build is refused with an error. Shipped migrations
are never edited.

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
- `messages_body_pending (account, date DESC, uid DESC) WHERE body_state = 'none'`:
  the body queue. It is a partial index, so it shrinks as bodies arrive.

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

`UpsertHeaders` rejects a header whose UIDVALIDITY does not match the
mailbox's current one, so a stale fetch cannot write into a new epoch.

## API

`Store` is an interface, and `Open` returns the SQLite implementation. Each
write method runs in one transaction. Batch writes (`UpsertHeaders`,
`UpdateFlags`, `DeleteMissing`) are atomic per call.

| Method | Purpose |
|--------|---------|
| `UpsertMailboxes` | Insert or update mailboxes. Purges a mailbox whose UIDVALIDITY changed. |
| `DeleteMailboxesNotIn` | Drop mailboxes that the server no longer lists. |
| `GetSyncState` | Per-mailbox UIDVALIDITY, highest UID, last sync time. |
| `MarkSynced` | Record a completed sync of a mailbox. |
| `PurgeMailbox` | Delete a mailbox's messages and bodies. Keep the row. |
| `CachedUIDs` | UIDs cached for one epoch, ascending. |
| `UpsertHeaders` | Batch insert or replace headers. Keeps `body_state`. |
| `DeleteMissing` | Delete cached UIDs that are not in the keep set. The keep set goes through a temp table, so the SQLite parameter limit does not apply. |
| `UpdateFlags` | Set flags on cached messages. Unknown UIDs are ignored. |
| `ListMessages` | Headers of one mailbox, newest first. Keyset pagination with `Cursor{Date, UID}`. Limit 1 to 1000. |
| `GetMessage` / `GetBody` | One header or body. `ErrNotFound` if absent. |
| `SetBody` | Store a body and set `body_state = cached`. |
| `MarkBodyFailed` | Count a failed body fetch. |
| `PendingBodies` | Up to N messages with no body and fewer than `MaxBodyAttempts` (3) failures, newest first. |
| `Counts` | Messages and cached bodies per mailbox. |

## Tests

`store_test.go` runs on a SQLite file in `t.TempDir()`. It covers file and
directory modes, WAL, foreign keys, the schema version and refusal of newer
versions, keyset paging with equal dates, purge on UIDVALIDITY change, cascade
on purge, the body round trip through gzip, the attempt bound, flag
normalisation, and DeleteMissing with a keep set above the parameter limit.
`TestDefaultPathLinuxXDG` covers the path rules on Linux. The tests run with
`go test -race`.
