package restic

import (
	"strconv"
	"time"

	"restic-repo-browser/internal/domain"
)

// snapshotJSON is a snapshot as reported by `restic snapshots --json`.
type snapshotJSON struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	// Summary is the backup summary restic (0.17 and later) stores in each snapshot.
	Summary *struct {
		DataAdded           int64 `json:"data_added"`
		TotalFilesProcessed int64 `json:"total_files_processed"`
		TotalBytesProcessed int64 `json:"total_bytes_processed"`
	} `json:"summary"`
}

func (s snapshotJSON) toDomain() domain.Snapshot {
	d := domain.Snapshot{
		ID: s.ID, ShortID: s.ShortID, Time: s.Time, Host: s.Hostname, Username: s.Username,
		Paths: s.Paths, Tags: s.Tags,
	}
	if s.Summary != nil {
		size, added, files := s.Summary.TotalBytesProcessed, s.Summary.DataAdded, s.Summary.TotalFilesProcessed
		d.Size, d.Added, d.Files = &size, &added, &files
	}
	return d
}

func toSnapshots(in []snapshotJSON) []domain.Snapshot {
	out := make([]domain.Snapshot, len(in))
	for i, s := range in {
		out[i] = s.toDomain()
	}
	return out
}

// statsJSON is the output of `restic stats --json`.
type statsJSON struct {
	TotalSize      int64 `json:"total_size"`
	TotalFileCount int64 `json:"total_file_count"`
}

// nodeJSON is an entry of `restic ls --json`. Its fields match backend.Node, so
// the two convert directly.
type nodeJSON struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Path        string    `json:"path"`
	Size        *int64    `json:"size"`
	Permissions string    `json:"permissions"`
	MTime       time.Time `json:"mtime"`
	UID         uint32    `json:"uid"`
	GID         uint32    `json:"gid"`
}

// keepArgs converts a keep policy into restic command line flags.
func keepArgs(k domain.KeepPolicy) []string {
	var a []string
	add := func(flag string, n int) {
		if n > 0 {
			a = append(a, flag, strconv.Itoa(n))
		}
	}
	add("--keep-last", k.Last)
	add("--keep-daily", k.Daily)
	add("--keep-weekly", k.Weekly)
	add("--keep-monthly", k.Monthly)
	add("--keep-yearly", k.Yearly)
	return a
}
