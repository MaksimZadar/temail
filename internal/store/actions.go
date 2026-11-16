package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// FlagsOfMessages implements Store. Flags live on the message, so one row per
// message answers for every placement.
func (s *sqliteStore) FlagsOfMessages(ctx context.Context, account string, mids []string) (map[string][]string, error) {
	out := make(map[string][]string, len(mids))
	if len(mids) == 0 {
		return out, nil
	}
	args := []any{account}
	for _, mid := range mids {
		args = append(args, mid)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT mid, flags FROM messages
		WHERE account = ? AND mid IN (`+placeholders(len(mids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mid, enc string
		if err := rows.Scan(&mid, &enc); err != nil {
			return nil, err
		}
		var flags []string
		if err := json.Unmarshal([]byte(enc), &flags); err != nil {
			return nil, fmt.Errorf("decode flags of %s: %w", mid, err)
		}
		out[mid] = flags
	}
	return out, rows.Err()
}

// ApplyActionUpdates implements Store. Each update is the cache effect of one
// verified write: a flag change, a placement removal, a placement addition, or a
// combination (move). The triggers in schema.go adjust placements_count, bodies
// and unseen from these rows, so no counter is recomputed by hand.
func (s *sqliteStore) ApplyActionUpdates(ctx context.Context, account string, updates []ActionUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, u := range updates {
			if u.MID == "" {
				return errors.New("store: action update without a mid")
			}
			if u.Flags != nil {
				enc, err := json.Marshal(NormalizeFlags(u.Flags))
				if err != nil {
					return err
				}
				// The message may have been removed by a concurrent reconcile;
				// then there is nothing left to hold the flags.
				if _, err := tx.ExecContext(ctx, `
					UPDATE messages SET flags = ?
					WHERE account = ? AND mid = ? AND flags <> ?`,
					string(enc), account, u.MID, string(enc)); err != nil {
					return fmt.Errorf("store flags of %s: %w", u.MID, err)
				}
			}
			if u.RemoveMailbox != "" {
				if _, err := tx.ExecContext(ctx, `
					DELETE FROM placements
					WHERE account = ? AND mailbox = ? AND mid = ?`,
					account, u.RemoveMailbox, u.MID); err != nil {
					return fmt.Errorf("remove placement of %s in %s: %w", u.MID, u.RemoveMailbox, err)
				}
			}
			if u.AddMailbox != "" {
				var uidValidity uint32
				err := tx.QueryRowContext(ctx, `
					SELECT uidvalidity FROM mailboxes WHERE account = ? AND name = ?`,
					account, u.AddMailbox).Scan(&uidValidity)
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("%w: mailbox %s", ErrNotFound, u.AddMailbox)
				}
				if err != nil {
					return err
				}
				var date int64
				err = tx.QueryRowContext(ctx, `
					SELECT date FROM messages WHERE account = ? AND mid = ?`,
					account, u.MID).Scan(&date)
				if errors.Is(err, sql.ErrNoRows) {
					continue // the message vanished; nothing to place
				}
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO placements (account, mailbox, uidvalidity, uid, mid, date)
					VALUES (?, ?, ?, ?, ?, ?)
					ON CONFLICT (account, mailbox, uidvalidity, uid) DO UPDATE SET
						mid = excluded.mid, date = excluded.date`,
					account, u.AddMailbox, uidValidity, u.AddUID, u.MID, date); err != nil {
					return fmt.Errorf("add placement of %s in %s: %w", u.MID, u.AddMailbox, err)
				}
			}
		}
		return nil
	})
}
