package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"restic-repo-browser/internal/domain"
)

const repoColumns = `id, name, path, created_at, refreshed_at, refresh_ok_at, refresh_error, size_bytes`

func scanRepo(row interface{ Scan(...any) error }) (domain.Repo, error) {
	var r domain.Repo
	var created, refreshed, ok int64
	err := row.Scan(&r.ID, &r.Name, &r.Path, &created, &refreshed, &ok, &r.RefreshError, &r.SizeBytes)
	r.CreatedAt, r.RefreshedAt, r.RefreshOKAt = fromMillis(created), fromMillis(refreshed), fromMillis(ok)
	return r, err
}

// ListRepos returns all repositories ordered by name.
func (s *Store) ListRepos(ctx context.Context) ([]domain.Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+repoColumns+` FROM repos ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: list repos: %w", err)
	}
	defer rows.Close()
	var out []domain.Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan repo: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Repo returns one repository or ErrNotFound.
func (s *Store) Repo(ctx context.Context, id int64) (domain.Repo, error) {
	r, err := scanRepo(s.db.QueryRowContext(ctx, `SELECT `+repoColumns+` FROM repos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Repo{}, ErrNotFound
	}
	if err != nil {
		return domain.Repo{}, fmt.Errorf("store: get repo %d: %w", id, err)
	}
	return r, nil
}

// NewRepo are the user-editable fields of a repository.
type NewRepo struct {
	Name     string
	Path     string
	Password string
}

// CreateRepo adds a repository and returns its id. ErrDuplicate means the name is taken.
func (s *Store) CreateRepo(ctx context.Context, n NewRepo, now time.Time) (int64, error) {
	enc, err := s.cipher.Seal([]byte(n.Password))
	if err != nil {
		return 0, fmt.Errorf("store: encrypt password: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO repos (name, path, password_enc, created_at) VALUES (?, ?, ?, ?)`,
		n.Name, n.Path, enc, toMillis(now))
	if isUniqueViolation(err) {
		return 0, ErrDuplicate
	}
	if err != nil {
		return 0, fmt.Errorf("store: create repo: %w", err)
	}
	return res.LastInsertId()
}

// UpdateRepo changes name and path, and the password when n.Password is not empty.
func (s *Store) UpdateRepo(ctx context.Context, id int64, n NewRepo) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE repos SET name = ?, path = ? WHERE id = ?`, n.Name, n.Path, id)
		if err != nil {
			return err
		}
		if cnt, _ := res.RowsAffected(); cnt == 0 {
			return ErrNotFound
		}
		if n.Password == "" {
			return nil
		}
		enc, err := s.cipher.Seal([]byte(n.Password))
		if err != nil {
			return fmt.Errorf("encrypt password: %w", err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE repos SET password_enc = ? WHERE id = ?`, enc, id)
		return err
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	case isUniqueViolation(err):
		return ErrDuplicate
	case err != nil:
		return fmt.Errorf("store: update repo %d: %w", id, err)
	}
	return nil
}

// DeleteRepo removes a repository with all its cached data, jobs and runs.
func (s *Store) DeleteRepo(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repos WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete repo %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RepoPassword returns the decrypted repository password.
func (s *Store) RepoPassword(ctx context.Context, id int64) (string, error) {
	var enc []byte
	err := s.db.QueryRowContext(ctx, `SELECT password_enc FROM repos WHERE id = ?`, id).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: read password of repo %d: %w", id, err)
	}
	plain, err := s.cipher.Open(enc)
	if err != nil {
		return "", fmt.Errorf("store: repo %d: %w", id, err)
	}
	return string(plain), nil
}

// RefreshResult records the outcome of a cache refresh.
type RefreshResult struct {
	At   time.Time
	Err  string // empty on success
	Size int64  // repository size on disk; only stored on success
}

// SaveRefresh stores the outcome of a refresh attempt. On failure the previous
// size and the cached snapshots stay as they were.
func (s *Store) SaveRefresh(ctx context.Context, id int64, r RefreshResult) error {
	var err error
	if r.Err == "" {
		_, err = s.db.ExecContext(ctx,
			`UPDATE repos SET refreshed_at = ?, refresh_ok_at = ?, refresh_error = '', size_bytes = ? WHERE id = ?`,
			toMillis(r.At), toMillis(r.At), r.Size, id)
	} else {
		_, err = s.db.ExecContext(ctx,
			`UPDATE repos SET refreshed_at = ?, refresh_error = ? WHERE id = ?`, toMillis(r.At), r.Err, id)
	}
	if err != nil {
		return fmt.Errorf("store: save refresh of repo %d: %w", id, err)
	}
	return nil
}
