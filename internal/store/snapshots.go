package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"restic-repo-browser/internal/domain"
)

// Snapshots returns the cached snapshots of a repository, oldest first.
func (s *Store) Snapshots(ctx context.Context, repoID int64) ([]domain.Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, short_id, time, host, username, paths, tags, size_bytes, added_bytes, files
		FROM snapshots WHERE repo_id = ? ORDER BY time, id`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: list snapshots of repo %d: %w", repoID, err)
	}
	defer rows.Close()
	var out []domain.Snapshot
	for rows.Next() {
		var sn domain.Snapshot
		var ms int64
		var paths, tags string
		var size, added, files sql.NullInt64
		if err := rows.Scan(&sn.ID, &sn.ShortID, &ms, &sn.Host, &sn.Username, &paths, &tags, &size, &added, &files); err != nil {
			return nil, fmt.Errorf("store: scan snapshot: %w", err)
		}
		sn.Time = fromMillis(ms)
		if err := json.Unmarshal([]byte(paths), &sn.Paths); err != nil {
			return nil, fmt.Errorf("store: snapshot %s paths: %w", sn.ID, err)
		}
		if err := json.Unmarshal([]byte(tags), &sn.Tags); err != nil {
			return nil, fmt.Errorf("store: snapshot %s tags: %w", sn.ID, err)
		}
		sn.Size, sn.Added, sn.Files = nullPtr(size), nullPtr(added), nullPtr(files)
		out = append(out, sn)
	}
	return out, rows.Err()
}

// SyncSnapshots makes the cached snapshot set of a repository equal to snaps:
// new snapshots are inserted, vanished ones deleted, and tags updated. Sizes
// that are already cached survive when the incoming snapshot carries none.
func (s *Store) SyncSnapshots(ctx context.Context, repoID int64, snaps []domain.Snapshot) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		existing := map[string]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM snapshots WHERE repo_id = ?`, repoID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			existing[id] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		upsert, err := tx.PrepareContext(ctx, `
			INSERT INTO snapshots (repo_id, id, short_id, time, host, username, paths, tags, size_bytes, added_bytes, files)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (repo_id, id) DO UPDATE SET
				tags = excluded.tags,
				size_bytes  = COALESCE(excluded.size_bytes,  size_bytes),
				added_bytes = COALESCE(excluded.added_bytes, added_bytes),
				files       = COALESCE(excluded.files,       files)`)
		if err != nil {
			return err
		}
		defer upsert.Close()

		for _, sn := range snaps {
			paths, _ := json.Marshal(nonNil(sn.Paths))
			tags, _ := json.Marshal(nonNil(sn.Tags))
			if _, err := upsert.ExecContext(ctx, repoID, sn.ID, sn.ShortID, toMillis(sn.Time), sn.Host, sn.Username,
				string(paths), string(tags), ptrNull(sn.Size), ptrNull(sn.Added), ptrNull(sn.Files)); err != nil {
				return err
			}
			delete(existing, sn.ID)
		}
		for id := range existing { // whatever is left no longer exists in the repository
			if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE repo_id = ? AND id = ?`, repoID, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// SnapshotsWithoutSize lists ids of snapshots whose size is not known yet.
func (s *Store) SnapshotsWithoutSize(ctx context.Context, repoID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM snapshots WHERE repo_id = ? AND size_bytes IS NULL ORDER BY time`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: snapshots without size: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SetSnapshotSize records a size computed separately (restic stats).
func (s *Store) SetSnapshotSize(ctx context.Context, repoID int64, id string, size, files int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE snapshots SET size_bytes = ?, files = ? WHERE repo_id = ? AND id = ?`, size, files, repoID, id)
	if err != nil {
		return fmt.Errorf("store: set size of snapshot %s: %w", id, err)
	}
	return nil
}

// ReplaceLocks stores the locks currently seen in a repository.
func (s *Store) ReplaceLocks(ctx context.Context, repoID int64, locks []domain.Lock) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM locks WHERE repo_id = ?`, repoID); err != nil {
			return err
		}
		for _, l := range locks {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO locks (repo_id, id, time, host, username, pid, exclusive) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				repoID, l.ID, toMillis(l.Time), l.Host, l.Username, l.PID, boolInt(l.Exclusive)); err != nil {
				return err
			}
		}
		return nil
	})
}

// Locks returns the cached locks of a repository.
func (s *Store) Locks(ctx context.Context, repoID int64) ([]domain.Lock, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, time, host, username, pid, exclusive FROM locks WHERE repo_id = ? ORDER BY time`, repoID)
	if err != nil {
		return nil, fmt.Errorf("store: list locks: %w", err)
	}
	defer rows.Close()
	var out []domain.Lock
	for rows.Next() {
		var l domain.Lock
		var ms int64
		var excl int
		if err := rows.Scan(&l.ID, &ms, &l.Host, &l.Username, &l.PID, &excl); err != nil {
			return nil, err
		}
		l.Time, l.Exclusive = fromMillis(ms), excl != 0
		out = append(out, l)
	}
	return out, rows.Err()
}

func nullPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func ptrNull(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
