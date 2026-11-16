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
	"sort"
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
// need an expunge outside the label/trash paths, or a no-op that changed
// nothing).
var ErrNotUndoable = errors.New("action has no inverse")

// ErrDiverged is returned when the cache no longer matches the after-state the
// audit log recorded, for example because the user moved the message elsewhere
// between the action and the undo. It also satisfies errors.Is(err,
// ErrNotUndoable): nothing can be restored reliably.
var ErrDiverged = fmt.Errorf("%w: the message changed after the action", ErrNotUndoable)

// ErrWrongAccount is returned when an audit entry belongs to another account.
var ErrWrongAccount = errors.New("audit entry belongs to another account")

// Entry is one row of the audit log. It holds metadata only: action type,
// mailbox names, flags and the Proton internal id. It never holds subjects,
// bodies or secrets. Recent groups a batch into one Entry, so Count and
// AuditIDs describe the whole batch there.
type Entry struct {
	ID          int64     `json:"id"`
	TS          time.Time `json:"ts"`
	Account     string    `json:"-"`
	Type        string    `json:"type"`
	ParamsJSON  string    `json:"paramsJson"`
	InverseJSON string    `json:"inverseJson"`
	MID         string    `json:"mid"`
	BeforeJSON  string    `json:"beforeJson"`
	AfterJSON   string    `json:"afterJson"`
	Verified    bool      `json:"verified"`
	Error       string    `json:"error"`
	Undone      bool      `json:"undone"`
	BatchID     string    `json:"batchId"`
	UndoOf      int64     `json:"undoOf"`
	Count       int       `json:"count"`
	Undoable    bool      `json:"undoable"`
	AuditIDs    []int64   `json:"auditIds"`
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
	undone_ts          INTEGER NOT NULL DEFAULT 0,
	batch_id           TEXT    NOT NULL DEFAULT '',
	undo_of            INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS actions_recent ON actions (id DESC);
CREATE INDEX IF NOT EXISTS actions_pending ON actions (undone) WHERE undone = 0;
`

func (a *Audit) migrate(ctx context.Context) error {
	if _, err := a.db.ExecContext(ctx, auditSchema); err != nil {
		return fmt.Errorf("migrate audit log: %w", err)
	}
	// Older audit files predate batching and the undo marker. Add the columns
	// in place; ALTER TABLE ... ADD COLUMN is cheap and keeps the log.
	cols, err := a.columns(ctx)
	if err != nil {
		return err
	}
	add := []struct{ name, decl string }{
		{"batch_id", "TEXT NOT NULL DEFAULT ''"},
		{"undo_of", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range add {
		if cols[c.name] {
			continue
		}
		if _, err := a.db.ExecContext(ctx, `ALTER TABLE actions ADD COLUMN `+c.name+` `+c.decl); err != nil {
			return fmt.Errorf("migrate audit log (%s): %w", c.name, err)
		}
	}
	if _, err := a.db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS actions_batch ON actions (account, batch_id)`); err != nil {
		return fmt.Errorf("migrate audit log (batch index): %w", err)
	}
	return nil
}

// columns lists the table's column names.
func (a *Audit) columns(ctx context.Context) (map[string]bool, error) {
	rows, err := a.db.QueryContext(ctx, `PRAGMA table_info(actions)`)
	if err != nil {
		return nil, fmt.Errorf("read audit schema: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, typ        string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
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
			before_state_json, after_state_json, verified, error, batch_id, undo_of)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.TS.UnixMilli(), e.Account, e.Type, e.ParamsJSON, e.InverseJSON, e.MID,
		e.BeforeJSON, e.AfterJSON, boolInt(e.Verified), e.Error, e.BatchID, e.UndoOf)
	if err != nil {
		return 0, fmt.Errorf("write audit entry: %w", err)
	}
	return res.LastInsertId()
}

// Finalize updates a pre-inserted row after its write was attempted. The row
// was inserted with verified = 0 before the write, so a crash leaves evidence;
// this records the real outcome.
func (a *Audit) Finalize(ctx context.Context, id int64, e Entry) error {
	if a == nil {
		return errors.New("audit log is not open")
	}
	res, err := a.db.ExecContext(ctx, `
		UPDATE actions SET verified = ?, error = ?, after_state_json = ?, inverse_json = ?
		WHERE id = ?`,
		boolInt(e.Verified), e.Error, e.AfterJSON, e.InverseJSON, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAuditNotFound
	}
	return nil
}

const auditColumns = `id, ts, account, type, params_json, inverse_json, mid,
	before_state_json, after_state_json, verified, error, undone, batch_id, undo_of`

func scanEntry(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var ts, undoOf int64
	var verified, undone int
	if err := sc.Scan(&e.ID, &ts, &e.Account, &e.Type, &e.ParamsJSON, &e.InverseJSON,
		&e.MID, &e.BeforeJSON, &e.AfterJSON, &verified, &e.Error, &undone,
		&e.BatchID, &undoOf); err != nil {
		return Entry{}, err
	}
	e.TS = time.UnixMilli(ts)
	e.Verified = verified == 1
	e.Undone = undone == 1
	e.UndoOf = undoOf
	return e, nil
}

// Recent returns one entry per action batch, newest batch first. limit bounds
// the number of batches, not rows; it defaults to 50 and is capped at 500. The
// entries of one batch share batch_id; a legacy row with no batch id is its own
// batch.
func (a *Audit) Recent(ctx context.Context, account string, limit int) ([]Entry, error) {
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
		SELECT `+auditColumns+`
		FROM actions
		WHERE account = ? AND (
			CASE WHEN batch_id = '' THEN 'legacy-' || id ELSE batch_id END
		) IN (
			SELECT bid FROM (
				SELECT CASE WHEN batch_id = '' THEN 'legacy-' || id ELSE batch_id END AS bid, id AS rid
				FROM actions WHERE account = ?
			) GROUP BY bid ORDER BY MAX(rid) DESC LIMIT ?
		)
		ORDER BY id ASC`, account, account, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byBatch := map[string]*Entry{}
	var order []string
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		key := e.BatchID
		if key == "" {
			key = fmt.Sprintf("legacy-%d", e.ID)
		}
		g := byBatch[key]
		if g == nil {
			cp := e
			cp.Count = 0
			cp.Verified = true
			cp.Undone = true
			cp.Error = ""
			cp.AuditIDs = nil
			g = &cp
			byBatch[key] = g
			order = append(order, key)
		}
		g.ID = e.ID
		g.Count++
		g.Verified = g.Verified && e.Verified
		g.Undone = g.Undone && e.Undone
		if g.Error == "" && e.Error != "" {
			g.Error = e.Error
		}
		if e.Verified && !e.Undone && e.InverseJSON != "" && e.InverseJSON != "{}" {
			g.Undoable = true
			g.AuditIDs = append(g.AuditIDs, e.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(order))
	for _, key := range order {
		out = append(out, *byBatch[key])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Get returns one entry.
func (a *Audit) Get(ctx context.Context, id int64) (Entry, error) {
	if a == nil {
		return Entry{}, errors.New("audit log is not open")
	}
	e, err := scanEntry(a.db.QueryRowContext(ctx,
		`SELECT `+auditColumns+` FROM actions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrAuditNotFound
	}
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// LastUndoableBatch returns the verified, not-undone rows of the newest batch
// that has at least one such row. The caller filters rows without an inverse,
// so a no-op batch still counts as the last action (and is not undoable) rather
// than silently skipping to an older one. It is scoped to one account.
func (a *Audit) LastUndoableBatch(ctx context.Context, account string) ([]Entry, error) {
	if a == nil {
		return nil, errors.New("audit log is not open")
	}
	var batch string
	var id int64
	err := a.db.QueryRowContext(ctx, `
		SELECT CASE WHEN batch_id = '' THEN 'legacy-' || id ELSE batch_id END, id
		FROM actions WHERE account = ? AND verified = 1 AND undone = 0
		ORDER BY id DESC LIMIT 1`, account).Scan(&batch, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAuditNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT `+auditColumns+`
		FROM actions
		WHERE account = ? AND verified = 1 AND undone = 0 AND (
			CASE WHEN batch_id = '' THEN 'legacy-' || id ELSE batch_id END
		) = ?
		ORDER BY id ASC`, account, batch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
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
