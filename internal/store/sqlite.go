package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"github.com/MaksimZadar/temail/internal/model"
)

// sqliteStore implements Store on a SQLite file.
type sqliteStore struct {
	db *sql.DB
}

// Open opens (creating if needed) the cache at path and applies pending
// migrations. The directory is created with 0700 and the file with 0600.
// WAL mode, foreign keys and a 5 s busy timeout are set on every connection.
// Write transactions start with BEGIN IMMEDIATE, so they wait for the write
// lock instead of failing on upgrade.
func Open(path string) (Store, error) {
	if path == "" {
		return nil, errors.New("store: empty path")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create cache directory: %w", err)
	}
	if err := ensureFile(path); err != nil {
		return nil, err
	}
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cache: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open cache %s: %w", path, err)
	}
	s := &sqliteStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// ensureFile creates the database file with 0600 if it does not exist yet.
// SQLite creates its WAL and SHM files with the same mode.
func ensureFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create cache file: %w", err)
	}
	return f.Close()
}

// sqliteDSN returns a file: URI for path with the per-connection pragmas. The
// path is escaped by net/url, so "?", "#", "%" and spaces in it are safe.
// Windows drive paths (C:/x) become /C:/x inside the URI.
func sqliteDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve cache path: %w", err)
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{
		Scheme: "file",
		Path:   p,
		RawQuery: url.Values{
			"_pragma": []string{
				"busy_timeout(5000)",
				"journal_mode(WAL)",
				"foreign_keys(1)",
				"synchronous(NORMAL)",
			},
			"_txlock": []string{"immediate"},
		}.Encode(),
	}
	return u.String(), nil
}

// migrate applies every migration above the database's user_version. Each
// migration and its version bump run in one transaction.
func (s *sqliteStore) migrate(ctx context.Context) error {
	for {
		done := false
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			// The version is read after BEGIN IMMEDIATE holds the write lock, so
			// two processes opening the same file cannot both apply a migration.
			var v int
			if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
				return fmt.Errorf("read schema version: %w", err)
			}
			switch {
			case v > SchemaVersion:
				return fmt.Errorf("cache schema version %d is newer than this build supports (%d)", v, SchemaVersion)
			case v == SchemaVersion:
				done = true
				return nil
			}
			if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
				return err
			}
			// PRAGMA does not accept bound parameters; the value is an int.
			_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migrate cache: %w", err)
		}
		if done {
			return nil
		}
	}
}

// Close implements Store.
func (s *sqliteStore) Close() error { return s.db.Close() }

// inTx runs fn in a transaction and rolls back on error.
func (s *sqliteStore) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpsertMailboxes implements Store.
func (s *sqliteStore) UpsertMailboxes(ctx context.Context, account string, boxes []model.Mailbox) error {
	for _, b := range boxes {
		stale, err := s.epochChanged(ctx, account, b.Name, b.UIDValidity)
		if err != nil {
			return err
		}
		if stale {
			// Cached UIDs from the old epoch are invalid. Purge in chunks first;
			// if this is interrupted, the old validity is still stored and the
			// next run purges again.
			if err := s.purgeMailboxChunked(ctx, account, b.Name); err != nil {
				return err
			}
		}
		err = s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO mailboxes (account, name, kind, display_name, uidvalidity)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (account, name) DO UPDATE SET
					kind = excluded.kind,
					display_name = excluded.display_name,
					uidvalidity = CASE WHEN excluded.uidvalidity = 0
						THEN mailboxes.uidvalidity ELSE excluded.uidvalidity END`,
				account, b.Name, string(b.Kind), b.DisplayName, b.UIDValidity,
			); err != nil {
				return fmt.Errorf("upsert mailbox %s: %w", b.Name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// epochChanged reports whether the cached UIDVALIDITY of a mailbox differs from
// a known new one. A zero value on either side means unknown, so it never
// counts as a change: a zero must not wipe a cache.
func (s *sqliteStore) epochChanged(ctx context.Context, account, name string, uidValidity uint32) (bool, error) {
	var cur uint32
	err := s.db.QueryRowContext(ctx,
		`SELECT uidvalidity FROM mailboxes WHERE account = ? AND name = ?`, account, name).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return cur != 0 && uidValidity != 0 && cur != uidValidity, nil
}

// DeleteMailboxesNotIn implements Store.
func (s *sqliteStore) DeleteMailboxesNotIn(ctx context.Context, account string, names []string) error {
	if len(names) == 0 {
		// An empty listing is more likely a failed or partial listing than a
		// real state, and it would wipe every mailbox of the account.
		return ErrEmptyListing
	}
	keep := make(map[string]bool, len(names))
	for _, n := range names {
		keep[n] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM mailboxes WHERE account = ?`, account)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			_ = rows.Close()
			return err
		}
		if !keep[n] {
			gone = append(gone, n)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, n := range gone {
		if err := s.purgeMailboxChunked(ctx, account, n); err != nil {
			return err
		}
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `DELETE FROM mailboxes WHERE account = ? AND name = ?`, account, n)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// GetSyncState implements Store.
func (s *sqliteStore) GetSyncState(ctx context.Context, account string) ([]MailboxState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, kind, display_name, uidvalidity, highest_uid, last_sync
		FROM mailboxes WHERE account = ? ORDER BY name`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailboxState
	for rows.Next() {
		var st MailboxState
		var kind string
		var lastSync int64
		if err := rows.Scan(&st.Name, &kind, &st.DisplayName, &st.UIDValidity, &st.HighestUID, &lastSync); err != nil {
			return nil, err
		}
		st.Kind = model.MailboxKind(kind)
		st.LastSync = fromMillis(lastSync)
		out = append(out, st)
	}
	return out, rows.Err()
}

// MarkSynced implements Store.
func (s *sqliteStore) MarkSynced(ctx context.Context, account, mailbox string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET last_sync = ? WHERE account = ? AND name = ?`,
			toMillis(at), account, mailbox)
		if err != nil {
			return err
		}
		return requireOne(res, "mailbox "+mailbox)
	})
}

// PurgeMailbox implements Store.
func (s *sqliteStore) PurgeMailbox(ctx context.Context, account, mailbox string) error {
	return s.purgeMailboxChunked(ctx, account, mailbox)
}

// purgeChunk bounds the rows deleted per transaction, so a large purge does not
// hold the write lock for long.
const purgeChunk = 1000

// purgeMailboxChunked deletes the messages (and, by cascade, the bodies) of a
// mailbox in transactions of purgeChunk rows, then resets its sync state. An
// interrupted purge leaves a state that the next call finishes.
func (s *sqliteStore) purgeMailboxChunked(ctx context.Context, account, mailbox string) error {
	for {
		var n int64
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `
				DELETE FROM messages WHERE rowid IN (
					SELECT rowid FROM messages WHERE account = ? AND mailbox = ? LIMIT ?)`,
				account, mailbox, purgeChunk)
			if err != nil {
				return fmt.Errorf("purge messages of %s: %w", mailbox, err)
			}
			n, _ = res.RowsAffected()
			return nil
		})
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET highest_uid = 0, last_sync = 0 WHERE account = ? AND name = ?`,
			account, mailbox)
		return err
	})
}

// CachedUIDs implements Store.
func (s *sqliteStore) CachedUIDs(ctx context.Context, account, mailbox string, uidValidity uint32) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uid FROM messages
		WHERE account = ? AND mailbox = ? AND uidvalidity = ?
		ORDER BY uid`, account, mailbox, uidValidity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uint32
	for rows.Next() {
		var uid uint32
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// CachedFlags implements Store.
func (s *sqliteStore) CachedFlags(ctx context.Context, account, mailbox string, uidValidity uint32) (map[uint32][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uid, flags FROM messages
		WHERE account = ? AND mailbox = ? AND uidvalidity = ?`, account, mailbox, uidValidity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uint32][]string{}
	for rows.Next() {
		var uid uint32
		var enc string
		if err := rows.Scan(&uid, &enc); err != nil {
			return nil, err
		}
		var flags []string
		if err := json.Unmarshal([]byte(enc), &flags); err != nil {
			return nil, fmt.Errorf("decode flags of UID %d: %w", uid, err)
		}
		out[uid] = flags
	}
	return out, rows.Err()
}

// UpsertHeaders implements Store.
func (s *sqliteStore) UpsertHeaders(ctx context.Context, account string, headers []model.MessageHeader) error {
	if len(headers) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		epochs := map[string]uint32{}
		highest := map[string]uint32{}
		for _, h := range headers {
			cur, ok := epochs[h.Mailbox]
			if !ok {
				var v uint32
				err := tx.QueryRowContext(ctx,
					`SELECT uidvalidity FROM mailboxes WHERE account = ? AND name = ?`,
					account, h.Mailbox).Scan(&v)
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: mailbox %s is not cached", ErrNotFound, h.Mailbox)
				}
				if err != nil {
					return err
				}
				cur = v
				epochs[h.Mailbox] = v
			}
			if h.UIDValidity != cur {
				return fmt.Errorf("header UID %d in %s has UIDVALIDITY %d, cache has %d",
					h.UID, h.Mailbox, h.UIDValidity, cur)
			}
			if h.UID > highest[h.Mailbox] {
				highest[h.Mailbox] = h.UID
			}
		}

		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO messages (account, mailbox, uidvalidity, uid, message_id, subject,
				from_json, to_json, date, flags, in_reply_to, refs)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account, mailbox, uidvalidity, uid) DO UPDATE SET
				message_id = excluded.message_id,
				subject = excluded.subject,
				from_json = excluded.from_json,
				to_json = excluded.to_json,
				date = excluded.date,
				flags = excluded.flags,
				in_reply_to = excluded.in_reply_to,
				refs = excluded.refs`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range headers {
			from, err := json.Marshal(nonNilAddrs(h.From))
			if err != nil {
				return err
			}
			to, err := json.Marshal(nonNilAddrs(h.To))
			if err != nil {
				return err
			}
			flags, err := json.Marshal(NormalizeFlags(h.Flags))
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, account, h.Mailbox, h.UIDValidity, h.UID,
				h.MessageID, h.Subject, string(from), string(to), toMillis(h.Date),
				string(flags), h.InReplyTo, h.References); err != nil {
				return fmt.Errorf("store UID %d in %s: %w", h.UID, h.Mailbox, err)
			}
		}

		for mb, top := range highest {
			if _, err := tx.ExecContext(ctx, `
				UPDATE mailboxes SET highest_uid = MAX(highest_uid, ?)
				WHERE account = ? AND name = ?`, top, account, mb); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteMissing implements Store. The keep set goes into a temporary table, so
// the statement does not depend on the SQLite parameter limit.
func (s *sqliteStore) DeleteMissing(ctx context.Context, account, mailbox string, uidValidity uint32, keep []uint32) (int, error) {
	cached, err := s.CachedUIDs(ctx, account, mailbox, uidValidity)
	if err != nil {
		return 0, err
	}
	keepSet := make(map[uint32]bool, len(keep))
	for _, uid := range keep {
		keepSet[uid] = true
	}
	var missing []uint32
	for _, uid := range cached {
		if !keepSet[uid] {
			missing = append(missing, uid)
		}
	}
	deleted := 0
	for start := 0; start < len(missing); start += purgeChunk {
		batch := missing[start:min(start+purgeChunk, len(missing))]
		args := []any{account, mailbox, uidValidity}
		for _, uid := range batch {
			args = append(args, uid)
		}
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `
				DELETE FROM messages
				WHERE account = ? AND mailbox = ? AND uidvalidity = ?
				  AND uid IN (`+placeholders(len(batch))+`)`, args...)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			deleted += int(n)
			return nil
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// UpdateFlags implements Store.
func (s *sqliteStore) UpdateFlags(ctx context.Context, account, mailbox string, uidValidity uint32, updates []FlagUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			UPDATE messages SET flags = ?
			WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ? AND flags <> ?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range updates {
			enc, err := json.Marshal(NormalizeFlags(u.Flags))
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, string(enc), account, mailbox, uidValidity, u.UID, string(enc)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListMessages implements Store.
func (s *sqliteStore) ListMessages(ctx context.Context, account, mailbox string, opts ListOptions) ([]model.MessageHeader, error) {
	limit := opts.Limit
	if limit <= 0 || limit > MaxListLimit {
		return nil, fmt.Errorf("store: list limit %d out of range 1-%d", limit, MaxListLimit)
	}
	q := `
		SELECT mailbox, uidvalidity, uid, message_id, subject, from_json, to_json,
			date, flags, in_reply_to, refs
		FROM messages
		WHERE account = ? AND mailbox = ?
		  AND uidvalidity = (SELECT uidvalidity FROM mailboxes WHERE account = ? AND name = ?)`
	args := []any{account, mailbox, account, mailbox}
	if opts.Before != nil {
		q += ` AND (date < ? OR (date = ? AND uid < ?))`
		ms := toMillis(opts.Before.Date)
		args = append(args, ms, ms, opts.Before.UID)
	}
	q += ` ORDER BY date DESC, uid DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MessageHeader
	for rows.Next() {
		h, err := scanHeader(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetMessage implements Store.
func (s *sqliteStore) GetMessage(ctx context.Context, key MessageKey) (model.MessageHeader, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT mailbox, uidvalidity, uid, message_id, subject, from_json, to_json,
			date, flags, in_reply_to, refs
		FROM messages WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?`,
		key.Account, key.Mailbox, key.UIDValidity, key.UID)
	h, err := scanHeader(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.MessageHeader{}, ErrNotFound
	}
	return h, err
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanHeader(sc scanner) (model.MessageHeader, error) {
	var h model.MessageHeader
	var fromJSON, toJSON, flagsJSON string
	var date int64
	if err := sc.Scan(&h.Mailbox, &h.UIDValidity, &h.UID, &h.MessageID, &h.Subject,
		&fromJSON, &toJSON, &date, &flagsJSON, &h.InReplyTo, &h.References); err != nil {
		return model.MessageHeader{}, err
	}
	if err := json.Unmarshal([]byte(fromJSON), &h.From); err != nil {
		return model.MessageHeader{}, fmt.Errorf("decode from of UID %d: %w", h.UID, err)
	}
	if err := json.Unmarshal([]byte(toJSON), &h.To); err != nil {
		return model.MessageHeader{}, fmt.Errorf("decode to of UID %d: %w", h.UID, err)
	}
	if err := json.Unmarshal([]byte(flagsJSON), &h.Flags); err != nil {
		return model.MessageHeader{}, fmt.Errorf("decode flags of UID %d: %w", h.UID, err)
	}
	h.Date = fromMillis(date)
	return h, nil
}

// attachmentMeta is the stored form of model.Attachment (no content).
type attachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	PartID      string `json:"partId"`
}

// SetBody implements Store.
func (s *sqliteStore) SetBody(ctx context.Context, key MessageKey, body Body) error {
	textGz, err := gzipBytes(body.Text)
	if err != nil {
		return err
	}
	htmlGz, err := gzipBytes(body.HTML)
	if err != nil {
		return err
	}
	metas := make([]attachmentMeta, 0, len(body.Attachments))
	for _, a := range body.Attachments {
		metas = append(metas, attachmentMeta{Filename: a.Filename, ContentType: a.ContentType, Size: a.Size, PartID: a.PartID})
	}
	metaJSON, err := json.Marshal(metas)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE messages SET body_state = 'cached'
			WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?`,
			key.Account, key.Mailbox, key.UIDValidity, key.UID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO bodies (account, mailbox, uidvalidity, uid, text_gz, html_gz, attachments_meta_json)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account, mailbox, uidvalidity, uid) DO UPDATE SET
				text_gz = excluded.text_gz,
				html_gz = excluded.html_gz,
				attachments_meta_json = excluded.attachments_meta_json`,
			key.Account, key.Mailbox, key.UIDValidity, key.UID, textGz, htmlGz, string(metaJSON))
		return err
	})
}

// GetBody implements Store.
func (s *sqliteStore) GetBody(ctx context.Context, key MessageKey) (Body, error) {
	var textGz, htmlGz []byte
	var metaJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT text_gz, html_gz, attachments_meta_json FROM bodies
		WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?`,
		key.Account, key.Mailbox, key.UIDValidity, key.UID,
	).Scan(&textGz, &htmlGz, &metaJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Body{}, ErrNotFound
	}
	if err != nil {
		return Body{}, err
	}
	var b Body
	if b.Text, err = gunzipString(textGz); err != nil {
		return Body{}, err
	}
	if b.HTML, err = gunzipString(htmlGz); err != nil {
		return Body{}, err
	}
	var metas []attachmentMeta
	if err := json.Unmarshal([]byte(metaJSON), &metas); err != nil {
		return Body{}, fmt.Errorf("decode attachments: %w", err)
	}
	for _, m := range metas {
		b.Attachments = append(b.Attachments, model.Attachment{Filename: m.Filename, ContentType: m.ContentType, Size: m.Size, PartID: m.PartID})
	}
	return b, nil
}

// MarkBodyFailed implements Store.
func (s *sqliteStore) MarkBodyFailed(ctx context.Context, key MessageKey) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE messages SET body_attempts = body_attempts + 1
			WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?`,
			key.Account, key.Mailbox, key.UIDValidity, key.UID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// PendingBodies implements Store. It uses the partial index on body_state.
func (s *sqliteStore) PendingBodies(ctx context.Context, account string, after *PendingCursor, limit int) ([]PendingBody, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store: pending limit %d must be positive", limit)
	}
	q := `
		SELECT mailbox, uidvalidity, uid, date FROM messages
		WHERE account = ? AND body_state = 'none' AND body_attempts < ?`
	args := []any{account, MaxBodyAttempts}
	if after != nil {
		// Strictly after the cursor in the order below (row-value comparison is
		// written out so it works with mixed sort directions).
		d := toMillis(after.Date)
		q += ` AND (date < ? OR (date = ? AND (mailbox < ? OR (mailbox = ? AND
			(uidvalidity < ? OR (uidvalidity = ? AND uid < ?))))))`
		k := after.Key
		args = append(args, d, d, k.Mailbox, k.Mailbox, k.UIDValidity, k.UIDValidity, k.UID)
	}
	q += ` ORDER BY date DESC, mailbox DESC, uidvalidity DESC, uid DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingBody
	for rows.Next() {
		var pb PendingBody
		var d int64
		pb.Key.Account = account
		if err := rows.Scan(&pb.Key.Mailbox, &pb.Key.UIDValidity, &pb.Key.UID, &d); err != nil {
			return nil, err
		}
		pb.Date = fromMillis(d)
		out = append(out, pb)
	}
	return out, rows.Err()
}

// Counts implements Store.
func (s *sqliteStore) Counts(ctx context.Context, account string) ([]MailboxCounts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mb.name,
			(SELECT COUNT(*) FROM messages m
				WHERE m.account = mb.account AND m.mailbox = mb.name
				  AND m.uidvalidity = mb.uidvalidity),
			(SELECT COUNT(*) FROM messages m
				WHERE m.account = mb.account AND m.mailbox = mb.name
				  AND m.uidvalidity = mb.uidvalidity AND m.body_state = 'cached')
		FROM mailboxes mb WHERE mb.account = ? ORDER BY mb.name`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailboxCounts
	for rows.Next() {
		var c MailboxCounts
		if err := rows.Scan(&c.Mailbox, &c.Messages, &c.Bodies); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NormalizeFlags returns a sorted copy with duplicates removed, so the same
// flag set always encodes the same way. It never returns nil.
func NormalizeFlags(in []string) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

func nonNilAddrs(in []model.Address) []model.Address {
	if in == nil {
		return []model.Address{}
	}
	return in
}

func gzipBytes(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write([]byte(s)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipString(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("decompress body: %w", err)
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return "", fmt.Errorf("decompress body: %w", err)
	}
	return string(out), nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func requireOne(res sql.Result, what string) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}

// toMillis stores times as Unix milliseconds. The zero time maps to 0.
func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
