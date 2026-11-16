# cmd/actionprobe

Opt-in manual probe for the write recipes in `internal/actions`. It is not run in
CI and the app never calls it. It uses the same executor as the app, so it
exercises the real recipes, verification, cache update and audit log.

## Usage

```
actionprobe -mailbox INBOX -uid 42            # print the plan; nothing is written
actionprobe -mailbox INBOX -uid 42 -yes       # run the reversible recipes
actionprobe -mailbox INBOX -uid 42 -yes -destructive
```

Flags:

| Flag | Meaning |
|---|---|
| `-mailbox <name>` | Mailbox that holds the message (required), for example `INBOX` |
| `-uid <n>` | UID of the message in that mailbox (required) |
| `-yes` | Run the recipes. Without it the probe only prints the plan and writes nothing |
| `-destructive` | Also run trash + undo (off by default) |
| `-label <name>` | Existing label to add and remove (default `temail-test`) |
| `-account <id>` | Account ID from `config.json` (default: the first account) |
| `-cache <path>` | Cache database path (default `store.DefaultPath`) |
| `-trust` | Pin the presented Bridge certificate if it is not pinned yet |

The cache must be synced first (`mailsync`). The probe resolves the message by
the mailbox and UID in the cache and gets its stable Proton id.

## Recipes

With `-yes` it prints one row per recipe: `recipe | expected | observed | OK`.

1. **mark read** — toggles `\Seen` (read or unread depending on the current
   state), verifies it, then restores the original state.
2. **star** — same for `\Flagged`.
3. **add/remove label** — adds `Labels/<label>` by COPY, verifies the placement,
   then removes it with the label-only expunge path. If the label is not in the
   cache, the row is skipped with instructions to create it in Proton and sync.
4. **archive + undo** — archives the message, verifies the Archive placement,
   then undoes it and checks the original folder is back. Skipped when the
   message is only in All Mail (a COPY archive has no inverse).
5. **trash + undo** — only with `-destructive`. Moves to Trash, verifies, undoes
   and checks the original folder is back.

`OK` is `yes`, `NO` (with the failure on the next line) or `skip`.

The probe writes real audit entries to `audit.db`; undoing the probe's actions is
the same path the app uses. It never deletes a message permanently.

## What it does not cover

It touches one message only, so it cannot test the 100-id batch path or the
100 ms throttle between mailbox groups. Those are covered by the package tests.
A real Bridge is still the only way to confirm the researched semantics: Archive
accepts MOVE or needs COPY, All Mail refuses MOVE, COPYUID is reported, and the
label expunge removes only the label placement.
