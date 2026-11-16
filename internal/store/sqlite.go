package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	sqlite "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"github.com/MaksimZadar/temail/internal/model"
)

// sqliteStore implements Store on a SQLite file.
type sqliteStore struct {
	db   *sql.DB
	path string
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
	ctx := context.Background()
	// The first connection to a new file switches it to WAL. That switch can
	// return SQLITE_BUSY at once, without waiting on busy_timeout, while another
	// process or goroutine is opening the same file (measured: about 7% of
	// concurrent first opens, none without the WAL switch). The switch is
	// idempotent, so the whole open is retried on busy. See retryBusy.
	var db *sql.DB
	err = retryBusy(ctx, func() error {
		d, err := sql.Open("sqlite", dsn)
		if err != nil {
			return err
		}
		if err := d.Ping(); err != nil {
			_ = d.Close()
			return err
		}
		db = d
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("open cache %s: %w", path, err)
	}
	s := &sqliteStore{db: db, path: path}
	if err := retryBusy(ctx, func() error { return s.migrate(ctx) }); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// busyRetryTimeout bounds how long Open retries SQLITE_BUSY. Each attempt
// already waits busy_timeout (5 s) for locks it can wait on, so this is a
// second bound for the cases that return BUSY immediately.
const busyRetryTimeout = 15 * time.Second

// retryBusy runs op until it succeeds, fails with an error other than
// SQLITE_BUSY or SQLITE_LOCKED, or busyRetryTimeout passes. Waits between
// attempts grow from 5 ms to 200 ms. op must be safe to run again: every
// attempt here starts a new connection or a new transaction.
func retryBusy(ctx context.Context, op func() error) error {
	deadline := time.Now().Add(busyRetryTimeout)
	delay := 5 * time.Millisecond
	for {
		err := op()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		delay = min(delay*2, 200*time.Millisecond)
	}
}

// isBusy reports whether err is SQLITE_BUSY or SQLITE_LOCKED, including their
// extended result codes (the primary code is the low byte).
func isBusy(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	code := se.Code() & 0xff
	return code == 5 || code == 6 // SQLITE_BUSY, SQLITE_LOCKED
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
		SELECT name, kind, display_name, uidvalidity, highest_uid, last_sync, unseen, uidnext
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
		if err := rows.Scan(&st.Name, &kind, &st.DisplayName, &st.UIDValidity, &st.HighestUID, &lastSync, &st.Unseen, &st.UIDNext); err != nil {
			return nil, err
		}
		st.Kind = model.MailboxKind(kind)
		st.LastSync = fromMillis(lastSync)
		out = append(out, st)
	}
	return out, rows.Err()
}

// MarkSynced implements Store.
func (s *sqliteStore) MarkSynced(ctx context.Context, account, mailbox string, at time.Time, uidNext uint32) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET last_sync = ?, uidnext = ? WHERE account = ? AND name = ?`,
			toMillis(at), uidNext, account, mailbox)
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

// MarkMailboxesStale implements Store. LastSync zero makes the next watcher
// check see the mailbox as changed (mailboxChanged checks LastSync first).
func (s *sqliteStore) MarkMailboxesStale(ctx context.Context, account string, mailboxes []string) error {
	if len(mailboxes) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, mailbox := range mailboxes {
			if _, err := tx.ExecContext(ctx,
				`UPDATE mailboxes SET last_sync = 0 WHERE account = ? AND name = ?`,
				account, mailbox); err != nil {
				return fmt.Errorf("mark mailbox %s stale: %w", mailbox, err)
			}
		}
		return nil
	})
}

// purgeChunk bounds the rows deleted per transaction, so a large purge does not
// hold the write lock for long.
const purgeChunk = 1000

// attachmentMeta is the stored form of model.Attachment (no content).
type attachmentMeta struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	PartID      string `json:"partId"`
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
