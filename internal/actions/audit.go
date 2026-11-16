// Package actions applies user actions to Proton Bridge and records them in an
// audit log with undo. It is the only package that calls the write-capable
// mail.Writer. See README.md and docs/ARCHITECTURE.md §3.3 and §7.
package actions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"github.com/MaksimZadar/temail/internal/store"
)

// ErrAuditNotFound is returned when an audit id does not exist.
var ErrAuditNotFound = errors.New("audit entry not found")

// ErrAlreadyUndone is returned when an audit entry was already undone.
var ErrAlreadyUndone = errors.New("action was already undone")

// ErrNotUndoable is returned for an action that has no inverse (for example an
// archive of a message that was only in All Mail, where undoing the copy would
// need an expunge outside the label/trash paths).
var ErrNotUndoable = errors.New("action has no inverse")

// ErrWrongAccount is returned when an audit entry belongs to another account.
var ErrWrongAccount = errors.New("audit entry belongs to another account")

// Entry is one row of the audit log. It holds metadata only: action type,
// mailbox names, flags and the Proton internal id. It never holds subjects,
// bodies or secrets.
type Entry struct {
	ID          int64
	TS          time.Time
	Account     string
	Type        string
	ParamsJSON  string
	InverseJSON string
	MID         string
	BeforeJSON  string
	AfterJSON   string
	Verified    bool
	Error       string
	Undone      bool
}

// Audit is the separate audit.db. Deleting cache.db never removes it.
type Audit struct {
	db   *sql.DB
	path string
}

// OpenAudit opens (creating if needed) the audit log at path and applies the
// schema. An empty path means store.DefaultAuditPath().
func OpenAudit(path string) (*Audit, error) {
	if path == "" {
		p, err := store.DefaultAuditPath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create audit file: %w", err)
	}
	_ = f.Close()
	dsn, err := auditDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	a := &Audit{db: db, path: path}
	if err := a.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return a, nil
}

// auditDSN mirrors the cache DSN: WAL, a busy timeout, and immediate write
// transactions.
func auditDSN(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve audit path: %w", err)
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
				"synchronous(NORMAL)",
			},
			"_txlock": []string{"immediate"},
		}.Encode(),
	}
	return u.String(), nil
}

const auditSchema = `
CREATE TABLE IF NOT EXISTS actions (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	ts                 INTEGER NOT NULL,
	account            TEXT    NOT NULL,
	type               TEXT    NOT NULL,
	params_json        TEXT    NOT NULL DEFAULT '{}',
	inverse_json       TEXT    NOT NULL DEFAULT '{}',
	mid                TEXT    NOT NULL,
	before_state_json  TEXT    NOT NULL DEFAULT '{}',
	after_state_json   TEXT    NOT NULL DEFAULT '{}',
	verified           INTEGER NOT NULL DEFAULT 0,
	error              TEXT    NOT NULL DEFAULT '',
	undone             INTEGER NOT NULL DEFAULT 0,
	undone_ts          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS actions_recent ON actions (id DESC);
CREATE INDEX IF NOT EXISTS actions_pending ON actions (undone) WHERE undone = 0;
`

func (a *Audit) migrate(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, auditSchema); err != nil {
		return fmt.Errorf("migrate audit log: %w", err)
	}
	return nil
}

// Close closes the audit database.
func (a *Audit) Close() error {
	if a == nil {
		return nil
	}
	return a.db.Close()
}

// Path returns the file the audit log lives in.
func (a *Audit) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Insert adds one entry and returns its id.
func (a *Audit) Insert(ctx context.Context, e Entry) (int64, error) {
	if a == nil {
		return 0, errors.New("audit log is not open")
	}
	res, err := a.db.ExecContext(ctx, `
		INSERT INTO actions (ts, account, type, params_json, inverse_json, mid,
			before_state_json, after_state_json, verified, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS.UnixMilli(), e.Account, e.Type, e.ParamsJSON, e.InverseJSON, e.MID,
		e.BeforeJSON, e.AfterJSON, boolInt(e.Verified), e.Error)
	if err != nil {
		return 0, fmt.Errorf("write audit entry: %w", err)
	}
	return res.LastInsertId()
}

// Recent returns the newest entries, newest first. limit defaults to 50 and is
// capped at 500.
func (a *Audit) Recent(ctx context.Context, limit int) ([]Entry, error) {
	if a == nil {
		return nil, errors.New("audit log is not open")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, ts, account, type, params_json, inverse_json, mid,
			before_state_json, after_state_json, verified, error, undone
		FROM actions ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var ts int64
		var verified, undone int
		if err := rows.Scan(&e.ID, &ts, &e.Account, &e.Type, &e.ParamsJSON, &e.InverseJSON,
			&e.MID, &e.BeforeJSON, &e.AfterJSON, &verified, &e.Error, &undone); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts)
		e.Verified = verified == 1
		e.Undone = undone == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// Get returns one entry.
func (a *Audit) Get(ctx context.Context, id int64) (Entry, error) {
	if a == nil {
		return Entry{}, errors.New("audit log is not open")
	}
	var e Entry
	var ts int64
	var verified, undone int
	err := a.db.QueryRowContext(ctx, `
		SELECT id, ts, account, type, params_json, inverse_json, mid,
			before_state_json, after_state_json, verified, error, undone
		FROM actions WHERE id = ?`, id).Scan(&e.ID, &ts, &e.Account, &e.Type,
		&e.ParamsJSON, &e.InverseJSON, &e.MID, &e.BeforeJSON, &e.AfterJSON,
		&verified, &e.Error, &undone)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrAuditNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	e.TS = time.UnixMilli(ts)
	e.Verified = verified == 1
	e.Undone = undone == 1
	return e, nil
}

// LastUndoable returns the newest entry that can still be undone.
func (a *Audit) LastUndoable(ctx context.Context) (Entry, error) {
	if a == nil {
		return Entry{}, errors.New("audit log is not open")
	}
	var e Entry
	var ts int64
	var verified, undone int
	err := a.db.QueryRowContext(ctx, `
		SELECT id, ts, account, type, params_json, inverse_json, mid,
			before_state_json, after_state_json, verified, error, undone
		FROM actions WHERE undone = 0 AND inverse_json <> '{}' AND verified = 1
		ORDER BY id DESC LIMIT 1`).Scan(&e.ID, &ts, &e.Account, &e.Type,
		&e.ParamsJSON, &e.InverseJSON, &e.MID, &e.BeforeJSON, &e.AfterJSON,
		&verified, &e.Error, &undone)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrAuditNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	e.TS = time.UnixMilli(ts)
	e.Verified = verified == 1
	e.Undone = undone == 1
	return e, nil
}

// MarkUndone records that the inverse of entry id was applied.
func (a *Audit) MarkUndone(ctx context.Context, id int64, at time.Time) error {
	if a == nil {
		return errors.New("audit log is not open")
	}
	res, err := a.db.ExecContext(ctx,
		`UPDATE actions SET undone = 1, undone_ts = ? WHERE id = ? AND undone = 0`,
		at.UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlreadyUndone
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
