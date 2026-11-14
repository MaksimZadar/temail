# internal/mail

Read-only IMAP client for Proton Bridge on `127.0.0.1`. Phase 2. It returns the
plain types in `internal/model` and has no UI, Wails, or SQLite knowledge.

Built on `github.com/emersion/go-imap/v2` (beta, kept behind the `Client`
interface), `github.com/jhillyerd/enmime` for MIME, and
`github.com/microcosm-cc/bluemonday` for HTML sanitisation.

## Read-only guarantee

- The `Client` interface has no write methods. A test checks the method names.
- The implementation issues only: `LOGIN`, `LIST`, `STATUS`, `EXAMINE` (SELECT
  with `ReadOnly`), `UID SEARCH`, `UID FETCH` with `BODY.PEEK`, `LOGOUT`/close.
  It never sends `STORE`, `COPY`, `MOVE`, `EXPUNGE`, `APPEND`, `CREATE`,
  `DELETE`, `RENAME`, `SUBSCRIBE` or `UNSUBSCRIBE`.
- Three checks back this up in the test suite:
  1. `TestNoWriteCallsInSource` parses the non-test source and fails on a call
     to any write method.
  2. `TestReadOnlyAcrossAllOperations` runs every operation against a test
     server that wraps its session. Each write method there records the call and
     returns an error. The test asserts that nothing was recorded, that every
     SELECT was `EXAMINE`, and that every body fetch used `PEEK`.
  3. Fetching a message does not set `\Seen`. The test checks the unseen count
     after a fetch.

## Types (`internal/model`)

- `Mailbox{Name, DisplayName, Kind, UIDValidity, Messages, Unseen}`.
- `MessageHeader{Mailbox, UIDValidity, UID, MessageID, Subject, From, To, Date,
  Flags, InReplyTo, References}`. `MessageID` and `InReplyTo` have no angle
  brackets. `References` is the raw header.
- `Message{MessageHeader, TextBody, HTMLBody, Attachments}`.
- `Attachment{Filename, ContentType, Size, PartID}`. `PartID` is the IMAP
  section number (`2`, `2.1`), validated before it reaches the server.

Mailbox kinds: `inbox`, `sent`, `drafts`, `trash`, `spam`, `archive`, `folder`,
`label`. Mapping order:

1. `Folders/X` is `folder` and `Labels/X` is `label`, with display name `X`.
2. A SPECIAL-USE attribute (`\Sent`, `\Drafts`, `\Trash`, `\Junk`, `\Archive`)
   sets the kind.
3. Otherwise the name decides (`INBOX`, `Sent`, `Drafts`, `Trash`, `Spam`,
   `Archive`; case-insensitive).
4. Anything else is `folder`. `All Mail` and the `\All`, `\Flagged` and
   `\Important` attributes are plain folders. They have no kind of their own.

`\Noselect` entries, such as the `Folders` and `Labels` parents, are not
listed.

## Operations

- **Headers.** `FetchHeaders(mailbox, UIDRange)` searches for UIDs in the
  range and fetches them in batches of `BatchSize` (200). `UIDsAbove(n)` gives
  the incremental-sync range. The server's `n:*` answer is filtered so that a
  range above the highest UID returns nothing.
- **Mailbox state.** `MailboxInfo` returns `UIDValidity`, `UIDNext`,
  `HighestUID` (`UIDNext-1`, an upper bound that may include expunged UIDs) and
  the message count. If `UIDValidity` changes, the cache for that mailbox must
  be dropped (see `docs/ARCHITECTURE.md` §6).
- **Message.** `FetchMessage` returns the raw message parsed by enmime. The text
  body is always kept. If a message has only HTML, the text is derived from it.
  The HTML body is sanitised (below).
- **Attachments.** `FetchAttachment(mailbox, uid, partID)` returns the decoded
  bytes (base64 and quoted-printable are undone). Attachments are listed from
  `BODYSTRUCTURE`, not from enmime, so a `PartID` is always the IMAP section.
  The whole part is buffered in memory.
- **Cancellation.** Cancelling the context of an operation closes the
  connection. Later calls return `ErrNotConnected` until `Connect` runs again.

## HTML sanitisation

`sanitizeHTML` is a bluemonday allowlist.

- Removed: scripts, styles, forms, frames, objects, all `on*` handlers, the
  `style` attribute, and any link scheme other than `http`, `https` or `mailto`.
- Remote images are blocked: an `<img>` keeps its `src` only if it is `cid:`.
  Remote `http(s)` and `data:` images are removed, and `alt` text is kept.
- Links get `rel="nofollow noopener"` and `target="_blank"`.

The result is still untrusted. The UI must render it in a sandboxed iframe
without scripts (see `docs/ARCHITECTURE.md` §3.1).

## Connection and TLS

- `Connect` honours `tlsMode`: `starttls` (default, IMAP 1143) or `ssl`
  (implicit TLS). Only loopback hosts are accepted (`127.0.0.1`, `::1`,
  `localhost`). Any other host gets `ErrNotLoopback` before any dial.
- Bridge's certificate is self-signed, so the normal chain check cannot pass.
  The loopback-only `InsecureSkipVerify` turns off that chain check and is never
  used for other hosts. The trust decision is the pin: `VerifyPeerCertificate`
  compares the SHA-256 of the leaf certificate with `certFingerprint` on every
  handshake.
- Login uses the Bridge password. It is never logged or put in an error.

### TOFU pin flow

1. **Not pinned** (`certFingerprint` empty). `Connect` returns an
   `*UntrustedCertError` that matches `ErrUntrustedCert`, with `Fingerprint` and
   `Subject`. The UI (or `cmd/mailprobe -trust`) shows both and asks the user to
   confirm the value shown in Proton Bridge. After confirmation the fingerprint
   is saved to the account's `certFingerprint` in `config.json`, and the
   connection is retried.
2. **Pinned and matching.** The connection proceeds.
3. **Pinned and different.** `Connect` returns a `*CertMismatchError` that
   matches `ErrCertMismatch`. Its message asks the user to re-pin after
   checking the new fingerprint in Bridge. Nothing is logged in.

Re-pinning after a mismatch is not automated in this package. The UI owns the
confirmation. `cmd/mailprobe -trust` only pins untrusted certificates.

## Tests

`bridge_test.go` starts an in-memory IMAP server (`imapserver/imapmemserver`)
on `127.0.0.1` with a self-signed certificate. `fixtures_test.go` holds the
messages: multipart/alternative, HTML with script and remote image, attachments
(base64 PDF and quoted-printable text), and bulk messages. The tests cover
pinning in both TLS modes, mailbox kinds and counts, UIDVALIDITY changes,
batching and incremental UID queries, parsing, sanitisation, attachments, and
the read-only guarantee. No real mailbox is used.

## Manual probe

`cmd/mailprobe` is a manual smoke tool and is not run in CI. It reads the
account from `config.json` and the password from the keyring (or
`TEMAIL_BRIDGE_PASSWORD`), and prints the mailbox list and the five newest
INBOX subjects. It writes nothing to the mailbox.
