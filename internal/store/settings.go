package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Setting keys.
const (
	// SettingCacheRefreshTime is the local time of day ("15:04") of the daily cache update.
	SettingCacheRefreshTime = "cache_refresh_time"
)

// Setting returns the stored value for key, or def when it has not been set.
func (s *Store) Setting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: read setting %s: %w", key, err)
	}
	return v, nil
}

// SetSetting stores a value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: write setting %s: %w", key, err)
	}
	return nil
}

// PasswordHash returns the login password hash; ok is false when none is set yet.
func (s *Store) PasswordHash(ctx context.Context) (hash string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT password_hash FROM auth WHERE id = 1`).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: read password hash: %w", err)
	}
	return hash, true, nil
}

// SetPasswordHash stores the login password hash and invalidates all sessions.
func (s *Store) SetPasswordHash(ctx context.Context, hash string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO auth (id, password_hash) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET password_hash = excluded.password_hash`, hash); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions`)
		return err
	})
}

// CreateSession stores a session by the hash of its token.
func (s *Store) CreateSession(ctx context.Context, tokenHash string, now, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, created_at, expires_at) VALUES (?, ?, ?)`, tokenHash, toMillis(now), toMillis(expires))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionValid reports whether a session exists and has not expired.
func (s *Store) SessionValid(ctx context.Context, tokenHash string, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, toMillis(now)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: check session: %w", err)
	}
	return n > 0, nil
}

// DeleteSession ends a session.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, toMillis(now)); err != nil {
		return fmt.Errorf("store: purge sessions: %w", err)
	}
	return nil
}
