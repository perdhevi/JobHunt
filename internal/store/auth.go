package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// These methods hold the remote listener's sign-in state (remote.State), in the
// database so it survives restarts and is shared with "jobhunt auth" commands.

const settingRevokedBefore = "auth_revoked_before"

// RevokedBefore is when every browser and app was last signed out. Sessions
// and tokens from sign-ins before it are void.
func (s *Store) RevokedBefore(ctx context.Context) (time.Time, error) {
	v, err := s.GetSetting(ctx, settingRevokedBefore)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

// RevokeAll signs out every browser and app. The password stays the same.
func (s *Store) RevokeAll(ctx context.Context) error {
	return s.SetSetting(ctx, settingRevokedBefore, strconv.FormatInt(time.Now().UnixMilli(), 10))
}

// Mark records a sign-in id until exp, such as a used refresh token. It
// reports whether this call recorded it, and when it was first recorded.
func (s *Store) Mark(ctx context.Context, id string, exp time.Time) (bool, time.Time, error) {
	now := time.Now().UnixMilli()
	var first bool
	var at int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_marks WHERE expires < ?", now); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO auth_marks(id, at, expires) VALUES (?,?,?)", id, now, exp.UnixMilli())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		first = n == 1
		return tx.QueryRowContext(ctx, "SELECT at FROM auth_marks WHERE id = ?", id).Scan(&at)
	})
	return first, time.UnixMilli(at), err
}

// Marked reports whether id is recorded and not yet expired.
func (s *Store) Marked(ctx context.Context, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM auth_marks WHERE id = ? AND expires >= ?", id, time.Now().UnixMilli()).Scan(&n)
	return n > 0, err
}
