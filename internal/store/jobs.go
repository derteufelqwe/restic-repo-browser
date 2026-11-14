package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"restic-repo-browser/internal/domain"
)

const jobColumns = `repo_id, cron, keep_last, keep_daily, keep_weekly, keep_monthly, keep_yearly,
	prune, enabled`

func scanJob(row interface{ Scan(...any) error }) (domain.Job, error) {
	var j domain.Job
	var prune, enabled int
	k := &j.Keep
	err := row.Scan(&j.RepoID, &j.Cron, &k.Last, &k.Daily, &k.Weekly, &k.Monthly, &k.Yearly,
		&prune, &enabled)
	j.Prune, j.Enabled = prune != 0, enabled != 0
	return j, err
}

// Job returns the cleanup job of a repository, or nil when none is set up.
func (s *Store) Job(ctx context.Context, repoID int64) (*domain.Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE repo_id = ?`, repoID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get job of repo %d: %w", repoID, err)
	}
	return &j, nil
}

// Jobs returns all cleanup jobs keyed by repository id.
func (s *Store) Jobs(ctx context.Context) (map[int64]domain.Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM jobs`)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()
	out := map[int64]domain.Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan job: %w", err)
		}
		out[j.RepoID] = j
	}
	return out, rows.Err()
}

// SaveJob creates or replaces the cleanup job of j.RepoID.
func (s *Store) SaveJob(ctx context.Context, j domain.Job) error {
	k := j.Keep
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (repo_id) DO UPDATE SET
			cron = excluded.cron, keep_last = excluded.keep_last,
			keep_daily = excluded.keep_daily, keep_weekly = excluded.keep_weekly,
			keep_monthly = excluded.keep_monthly, keep_yearly = excluded.keep_yearly,
			prune = excluded.prune, enabled = excluded.enabled`,
		j.RepoID, j.Cron, k.Last, k.Daily, k.Weekly, k.Monthly, k.Yearly,
		boolInt(j.Prune), boolInt(j.Enabled))
	if err != nil {
		return fmt.Errorf("store: save job of repo %d: %w", j.RepoID, err)
	}
	return nil
}

// keepRuns is how many runs per repository are kept in the history.
const keepRuns = 50

// StartRun records the beginning of a run and returns its id.
func (s *Store) StartRun(ctx context.Context, repoID int64, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (repo_id, started_at, status) VALUES (?, ?, ?)`, repoID, toMillis(at), domain.RunRunning)
	if err != nil {
		return 0, fmt.Errorf("store: start run: %w", err)
	}
	return res.LastInsertId()
}

// FinishRun records the outcome of a run and trims old history of that repository.
func (s *Store) FinishRun(ctx context.Context, id int64, at time.Time, status domain.RunStatus, summary, log string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var repoID int64
		err := tx.QueryRowContext(ctx,
			`UPDATE runs SET finished_at = ?, status = ?, summary = ?, log = ? WHERE id = ? RETURNING repo_id`,
			toMillis(at), status, summary, log, id).Scan(&repoID)
		if err != nil {
			return fmt.Errorf("finish run %d: %w", id, err)
		}
		_, err = tx.ExecContext(ctx, `
			DELETE FROM runs WHERE repo_id = ? AND id NOT IN
				(SELECT id FROM runs WHERE repo_id = ? ORDER BY started_at DESC, id DESC LIMIT ?)`,
			repoID, repoID, keepRuns)
		return err
	})
}

// AbandonRunningRuns marks runs left "running" by a previous process as failed.
func (s *Store) AbandonRunningRuns(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = ?, finished_at = ?, summary = 'Interrupted: the server stopped while this run was in progress'
		 WHERE status = ?`, domain.RunFailed, toMillis(at), domain.RunRunning)
	if err != nil {
		return fmt.Errorf("store: abandon running runs: %w", err)
	}
	return nil
}

const runColumns = `runs.id, runs.repo_id, repos.name, runs.started_at, runs.finished_at, runs.status, runs.summary, runs.log`

func scanRun(row interface{ Scan(...any) error }) (domain.Run, error) {
	var r domain.Run
	var started, finished int64
	var status string
	err := row.Scan(&r.ID, &r.RepoID, &r.RepoName, &started, &finished, &status, &r.Summary, &r.Log)
	r.StartedAt, r.FinishedAt, r.Status = fromMillis(started), fromMillis(finished), domain.RunStatus(status)
	return r, err
}

func (s *Store) queryRuns(ctx context.Context, where string, args ...any) ([]domain.Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runColumns+` FROM runs JOIN repos ON repos.id = runs.repo_id `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()
	var out []domain.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoRuns returns the newest runs of one repository.
func (s *Store) RepoRuns(ctx context.Context, repoID int64, limit int) ([]domain.Run, error) {
	return s.queryRuns(ctx, `WHERE runs.repo_id = ? ORDER BY runs.started_at DESC, runs.id DESC LIMIT ?`, repoID, limit)
}

// RecentRuns returns the newest runs across all repositories.
func (s *Store) RecentRuns(ctx context.Context, limit int) ([]domain.Run, error) {
	return s.queryRuns(ctx, `ORDER BY runs.started_at DESC, runs.id DESC LIMIT ?`, limit)
}
