package restic

import (
	"time"
)

// Repo identifies a repository and how to open it.
type Repo struct {
	Path     string
	Password string
}

// Snapshot is a snapshot as reported by `restic snapshots --json`.
type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Username string    `json:"username"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
	Summary  *Summary  `json:"summary"`
}

// Summary is the backup summary restic (0.17 and later) stores in each snapshot.
type Summary struct {
	DataAdded           int64 `json:"data_added"`
	TotalFilesProcessed int64 `json:"total_files_processed"`
	TotalBytesProcessed int64 `json:"total_bytes_processed"`
}

// Stats is the output of `restic stats --mode restore-size`.
type Stats struct {
	TotalSize      int64 `json:"total_size"`
	TotalFileCount int64 `json:"total_file_count"`
}

// Node is a file, directory or other entry of a snapshot.
type Node struct {
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Path        string    `json:"path"`
	Size        *int64    `json:"size"`
	Permissions string    `json:"permissions"`
	MTime       time.Time `json:"mtime"`
	UID         uint32    `json:"uid"`
	GID         uint32    `json:"gid"`
}

// IsDir reports whether the node is a directory.
func (n Node) IsDir() bool { return n.Type == "dir" }

// Lock is the content of a lock file in the repository.
type Lock struct {
	ID        string    `json:"-"`
	Time      time.Time `json:"time"`
	Exclusive bool      `json:"exclusive"`
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	PID       int       `json:"pid"`
}

// ForgetGroup is one group of a `restic forget --json` result.
type ForgetGroup struct {
	Host   string     `json:"host"`
	Paths  []string   `json:"paths"`
	Tags   []string   `json:"tags"`
	Keep   []Snapshot `json:"keep"`
	Remove []Snapshot `json:"remove"`
}
