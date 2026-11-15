package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/MaksimZadar/temail/internal/model"
)

// The message model (schema v3). A message is one entity keyed by (account, mid),
// where mid is the Proton internal id. A placement keys one mailbox row,
// (account, mailbox, uidvalidity, uid), to a message. The body is stored once
// per message. Flags are stored once per message, because Proton keeps \Seen and
// \Flagged global. \Deleted is per mailbox and is not stored here.
//
// Queries that list a mailbox go through its placements. Message-level
// operations go through a placement key, so the public API still speaks in
// MessageKey. See README.md.

// queryer is satisfied by *sql.DB and *sql.Tx.
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// syntheticMID is the entity id for a placement whose header carries no
// InternalID. It is never canonical: it is unique to one placement, so it does
// not merge copies of a message.
func syntheticMID(mailbox string, uidValidity, uid uint32) string {
	return fmt.Sprintf("mb:%s:%d:%d", mailbox, uidValidity, uid)
}

// entityOf returns the entity id and canonical flag for a header.
func entityOf(h model.MessageHeader) (string, bool) {
	if h.InternalID != "" {
		return h.InternalID, h.Canonical
	}
	return syntheticMID(h.Mailbox, h.UIDValidity, h.UID), false
}

// midOf resolves a placement key to its entity id. ErrNotFound when there is no
// such placement.
func midOf(ctx context.Context, q queryer, key MessageKey) (string, error) {
	var mid string
	err := q.QueryRowContext(ctx, `
		SELECT mid FROM placements
		WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?`,
		key.Account, key.Mailbox, key.UIDValidity, key.UID).Scan(&mid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return mid, err
}

// gcMessages deletes the messages in mids that no longer have a placement. Their
// bodies go with them (cascade). It runs in the caller's transaction, after the
// placements were deleted.
func gcMessages(ctx context.Context, tx *sql.Tx, account string, mids []string) error {
	seen := map[string]bool{}
	for _, mid := range mids {
		if seen[mid] {
			continue
		}
		seen[mid] = true
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM messages
			WHERE account = ? AND mid = ?
			  AND NOT EXISTS (SELECT 1 FROM placements p
			                  WHERE p.account = messages.account AND p.mid = messages.mid)`,
			account, mid); err != nil {
			return fmt.Errorf("remove orphan message: %w", err)
		}
	}
	return nil
}

// purgeMailboxChunked deletes the placements of a mailbox in transactions of
// purgeChunk rows, then garbage-collects the messages they left behind, then
// resets the mailbox's sync state. An interrupted purge is finished by the next
// call, because the placements that remain are still there.
func (s *sqliteStore) purgeMailboxChunked(ctx context.Context, account, mailbox string) error {
	type placementRow struct {
		rowid int64
		mid   string
	}
	for {
		n := 0
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, `
				SELECT rowid, mid FROM placements
				WHERE account = ? AND mailbox = ? LIMIT ?`, account, mailbox, purgeChunk)
			if err != nil {
				return err
			}
			var batch []placementRow
			for rows.Next() {
				var r placementRow
				if err := rows.Scan(&r.rowid, &r.mid); err != nil {
					_ = rows.Close()
					return err
				}
				batch = append(batch, r)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			mids := make([]string, 0, len(batch))
			for _, r := range batch {
				if _, err := tx.ExecContext(ctx, `DELETE FROM placements WHERE rowid = ?`, r.rowid); err != nil {
					return fmt.Errorf("purge placements of %s: %w", mailbox, err)
				}
				mids = append(mids, r.mid)
			}
			n = len(batch)
			return gcMessages(ctx, tx, account, mids)
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
			`UPDATE mailboxes SET highest_uid = 0, last_sync = 0, unseen = 0 WHERE account = ? AND name = ?`,
			account, mailbox)
		return err
	})
}

// CachedUIDs implements Store.
func (s *sqliteStore) CachedUIDs(ctx context.Context, account, mailbox string, uidValidity uint32) ([]uint32, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT uid FROM placements
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

// CachedFlags implements Store. Flags come from the message, so they are the
// same in every mailbox the message is in.
func (s *sqliteStore) CachedFlags(ctx context.Context, account, mailbox string, uidValidity uint32) (map[uint32][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.uid, m.flags
		FROM placements p
		JOIN messages m ON m.account = p.account AND m.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ? AND p.uidvalidity = ?`, account, mailbox, uidValidity)
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

		msgStmt, err := tx.PrepareContext(ctx, `
			INSERT INTO messages (account, mid, canonical, message_id, subject,
				from_json, to_json, date, flags, in_reply_to, refs)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (account, mid) DO UPDATE SET
				canonical = MAX(messages.canonical, excluded.canonical),
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
		defer msgStmt.Close()
		plStmt, err := tx.PrepareContext(ctx, `
			INSERT INTO placements (account, mailbox, uidvalidity, uid, mid)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (account, mailbox, uidvalidity, uid) DO UPDATE SET mid = excluded.mid`)
		if err != nil {
			return err
		}
		defer plStmt.Close()

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
			mid, canonical := entityOf(h)
			canon := 0
			if canonical {
				canon = 1
			}
			if _, err := msgStmt.ExecContext(ctx, account, mid, canon, h.MessageID, h.Subject,
				string(from), string(to), toMillis(h.Date), string(flags), h.InReplyTo, h.References); err != nil {
				return fmt.Errorf("store UID %d in %s: %w", h.UID, h.Mailbox, err)
			}
			if _, err := plStmt.ExecContext(ctx, account, h.Mailbox, h.UIDValidity, h.UID, mid); err != nil {
				return fmt.Errorf("place UID %d in %s: %w", h.UID, h.Mailbox, err)
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

// DeleteMissing implements Store.
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
			rows, err := tx.QueryContext(ctx, `
				SELECT rowid, mid FROM placements
				WHERE account = ? AND mailbox = ? AND uidvalidity = ?
				  AND uid IN (`+placeholders(len(batch))+`)`, args...)
			if err != nil {
				return err
			}
			type row struct {
				rowid int64
				mid   string
			}
			var found []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.rowid, &r.mid); err != nil {
					_ = rows.Close()
					return err
				}
				found = append(found, r)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			mids := make([]string, 0, len(found))
			for _, r := range found {
				if _, err := tx.ExecContext(ctx, `DELETE FROM placements WHERE rowid = ?`, r.rowid); err != nil {
					return err
				}
				mids = append(mids, r.mid)
				deleted++
			}
			return gcMessages(ctx, tx, account, mids)
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// UpdateFlags implements Store. The flags are written to the message, so every
// placement of it sees them. Unknown UIDs are ignored.
func (s *sqliteStore) UpdateFlags(ctx context.Context, account, mailbox string, uidValidity uint32, updates []FlagUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			UPDATE messages SET flags = ?
			WHERE account = ? AND flags <> ?
			  AND mid = (SELECT mid FROM placements
			             WHERE account = ? AND mailbox = ? AND uidvalidity = ? AND uid = ?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, u := range updates {
			enc, err := json.Marshal(NormalizeFlags(u.Flags))
			if err != nil {
				return err
			}
			if _, err := stmt.ExecContext(ctx, string(enc), account, string(enc),
				account, mailbox, uidValidity, u.UID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetUnseen implements Store.
func (s *sqliteStore) SetUnseen(ctx context.Context, account, mailbox string, unseen uint32) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE mailboxes SET unseen = ? WHERE account = ? AND name = ?`, unseen, account, mailbox)
		if err != nil {
			return err
		}
		return requireOne(res, "mailbox "+mailbox)
	})
}

// headerColumns is the SELECT list of a placement joined with its message.
const headerColumns = `
	p.mailbox, p.uidvalidity, p.uid, m.mid, m.canonical, m.message_id, m.subject,
	m.from_json, m.to_json, m.date, m.flags, m.in_reply_to, m.refs`

// ListMessages implements Store. It lists the placements of one mailbox, newest
// first by message date. The cursor is (date, UID) within the mailbox.
func (s *sqliteStore) ListMessages(ctx context.Context, account, mailbox string, opts ListOptions) ([]model.MessageHeader, error) {
	limit := opts.Limit
	if limit <= 0 || limit > MaxListLimit {
		return nil, fmt.Errorf("store: list limit %d out of range 1-%d", limit, MaxListLimit)
	}
	q := `
		SELECT ` + headerColumns + `
		FROM placements p
		JOIN messages m ON m.account = p.account AND m.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ?
		  AND p.uidvalidity = (SELECT uidvalidity FROM mailboxes WHERE account = ? AND name = ?)`
	args := []any{account, mailbox, account, mailbox}
	if opts.Before != nil {
		q += ` AND (m.date < ? OR (m.date = ? AND p.uid < ?))`
		ms := toMillis(opts.Before.Date)
		args = append(args, ms, ms, opts.Before.UID)
	}
	q += ` ORDER BY m.date DESC, p.uid DESC LIMIT ?`
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
		SELECT `+headerColumns+`
		FROM placements p
		JOIN messages m ON m.account = p.account AND m.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ? AND p.uidvalidity = ? AND p.uid = ?`,
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
	var canonical int
	var fromJSON, toJSON, flagsJSON string
	var date int64
	if err := sc.Scan(&h.Mailbox, &h.UIDValidity, &h.UID, &h.InternalID, &canonical,
		&h.MessageID, &h.Subject, &fromJSON, &toJSON, &date, &flagsJSON,
		&h.InReplyTo, &h.References); err != nil {
		return model.MessageHeader{}, err
	}
	h.Canonical = canonical == 1
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

// SetBody implements Store. The body belongs to the message, so it is stored
// once for all of its placements.
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
		mid, err := midOf(ctx, tx, key)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE messages SET body_state = 'cached' WHERE account = ? AND mid = ?`,
			key.Account, mid)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO bodies (account, mid, text_gz, html_gz, attachments_meta_json)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (account, mid) DO UPDATE SET
				text_gz = excluded.text_gz,
				html_gz = excluded.html_gz,
				attachments_meta_json = excluded.attachments_meta_json`,
			key.Account, mid, textGz, htmlGz, string(metaJSON))
		return err
	})
}

// GetBody implements Store.
func (s *sqliteStore) GetBody(ctx context.Context, key MessageKey) (Body, error) {
	var textGz, htmlGz []byte
	var metaJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT b.text_gz, b.html_gz, b.attachments_meta_json
		FROM placements p
		JOIN bodies b ON b.account = p.account AND b.mid = p.mid
		WHERE p.account = ? AND p.mailbox = ? AND p.uidvalidity = ? AND p.uid = ?`,
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
		mid, err := midOf(ctx, tx, key)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE messages SET body_attempts = body_attempts + 1 WHERE account = ? AND mid = ?`,
			key.Account, mid)
		return err
	})
}

// preferredPlacementOrder picks the placement a body is fetched through: All
// Mail first (it holds every message), then any other mailbox except Recovered
// Messages.
const preferredPlacementOrder = `
	AND mailbox <> 'Recovered Messages'
	ORDER BY (mailbox = 'All Mail') DESC, mailbox ASC, uidvalidity DESC, uid DESC
	LIMIT 1`

// PendingBodies implements Store. It pages through messages (entities), not
// placements, so a message in five mailboxes is offered once. The partial index
// on body_state keeps the page query cheap.
func (s *sqliteStore) PendingBodies(ctx context.Context, account string, after *PendingCursor, limit int) ([]PendingBody, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("store: pending limit %d must be positive", limit)
	}
	// A message whose only placement is Recovered Messages is not fetched: it
	// is not a mailbox the user reads, and its bodies are not worth the traffic.
	q := `
		SELECT mid, date FROM messages
		WHERE account = ? AND body_state = 'none' AND body_attempts < ?
		  AND EXISTS (SELECT 1 FROM placements p
		              WHERE p.account = messages.account AND p.mid = messages.mid
		                AND p.mailbox <> 'Recovered Messages')`
	args := []any{account, MaxBodyAttempts}
	if after != nil {
		d := toMillis(after.Date)
		q += ` AND (date < ? OR (date = ? AND mid < ?))`
		args = append(args, d, d, after.MID)
	}
	q += ` ORDER BY date DESC, mid DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	type entity struct {
		mid  string
		date int64
	}
	var page []entity
	for rows.Next() {
		var e entity
		if err := rows.Scan(&e.mid, &e.date); err != nil {
			_ = rows.Close()
			return nil, err
		}
		page = append(page, e)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	out := make([]PendingBody, 0, len(page))
	for _, e := range page {
		var pb PendingBody
		pb.MID = e.mid
		pb.Date = fromMillis(e.date)
		pb.Key.Account = account
		err := s.db.QueryRowContext(ctx, `
			SELECT mailbox, uidvalidity, uid FROM placements
			WHERE account = ? AND mid = ?`+preferredPlacementOrder,
			account, e.mid).Scan(&pb.Key.Mailbox, &pb.Key.UIDValidity, &pb.Key.UID)
		if errors.Is(err, sql.ErrNoRows) {
			continue // no placement left: nothing to fetch through
		}
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, nil
}

// MembershipOf implements Store.
func (s *sqliteStore) MembershipOf(ctx context.Context, key MessageKey) ([]Placement, error) {
	mid, err := midOf(ctx, s.db, key)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.mailbox, mb.kind, mb.display_name, p.uidvalidity, p.uid
		FROM placements p
		JOIN mailboxes mb ON mb.account = p.account AND mb.name = p.mailbox
		WHERE p.account = ? AND p.mid = ?
		ORDER BY mb.name`, key.Account, mid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Placement
	for rows.Next() {
		var pl Placement
		var kind string
		if err := rows.Scan(&pl.Mailbox, &kind, &pl.DisplayName, &pl.UIDValidity, &pl.UID); err != nil {
			return nil, err
		}
		pl.Kind = model.MailboxKind(kind)
		out = append(out, pl)
	}
	return out, rows.Err()
}

// Counts implements Store. Messages are the placements of a mailbox. Bodies are
// the placements whose message has a cached body. Both are indexed reads on the
// placement primary key.
func (s *sqliteStore) Counts(ctx context.Context, account string) ([]MailboxCounts, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mb.name,
			(SELECT COUNT(*) FROM placements p
				WHERE p.account = mb.account AND p.mailbox = mb.name
				  AND p.uidvalidity = mb.uidvalidity),
			(SELECT COUNT(*) FROM placements p
				JOIN messages m ON m.account = p.account AND m.mid = p.mid
				WHERE p.account = mb.account AND p.mailbox = mb.name
				  AND p.uidvalidity = mb.uidvalidity AND m.body_state = 'cached')
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

// CacheStats implements Store. Messages and bodies are counted per account on
// their primary keys, which start with account, so SQLite counts from the index.
// The file sizes come from os.Stat.
func (s *sqliteStore) CacheStats(ctx context.Context, account string) (CacheStats, error) {
	var out CacheStats
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE account = ?`, account).Scan(&out.Messages); err != nil {
		return CacheStats{}, fmt.Errorf("count messages: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bodies WHERE account = ?`, account).Scan(&out.Bodies); err != nil {
		return CacheStats{}, fmt.Errorf("count bodies: %w", err)
	}
	for _, name := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		fi, err := statFile(name)
		if err != nil {
			return CacheStats{}, err
		}
		out.Bytes += fi
	}
	return out, nil
}

// statFile returns the size of name, or 0 when it does not exist.
func statFile(name string) (int64, error) {
	fi, err := os.Stat(name)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("stat cache file: %w", err)
	}
	return fi.Size(), nil
}
