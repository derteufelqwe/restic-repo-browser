// Package backend defines how the application talks to a backup repository.
// The rest of the program only knows this interface and its neutral types; the
// restic command line client in internal/restic is one implementation of it.
// Another way of reaching a repository (a REST server, rclone) is added by
// teaching an implementation about a new domain.BackendKind.
package backend

import (
	"context"
	"io"
	"path"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
)

// Ref identifies a repository and carries what is needed to open it.
type Ref struct {
	Kind     domain.BackendKind
	Location string
	Password string
}

// PasswordSource hands out the secret of a stored repository.
type PasswordSource interface {
	RepoPassword(ctx context.Context, id int64) (string, error)
}

// Resolve builds the Ref of a stored repository.
func Resolve(ctx context.Context, passwords PasswordSource, repo domain.Repo) (Ref, error) {
	pw, err := passwords.RepoPassword(ctx, repo.ID)
	if err != nil {
		return Ref{}, err
	}
	return Ref{Kind: repo.Backend, Location: repo.Location, Password: pw}, nil
}

// Node is a file, directory or other entry of a snapshot.
type Node struct {
	Name        string
	Type        string // "dir", "file", "symlink", ...
	Path        string // absolute, slash-separated path inside the snapshot
	Size        *int64 // nil when unknown
	Permissions string
	MTime       time.Time
	UID, GID    uint32
}

// IsDir reports whether the node is a directory.
func (n Node) IsDir() bool { return n.Type == "dir" }

// SnapshotSize is the restored size of one snapshot.
type SnapshotSize struct {
	Bytes int64
	Files int64
}

// ForgetResult says what a forget run did.
type ForgetResult struct {
	Kept    int
	Removed []domain.Snapshot
}

// PruneResult says what a prune run did.
type PruneResult struct {
	Freed string // human-readable amount of freed space, empty when not reported
}

// Backend is everything the application needs from a repository. Methods
// return the errors described in errors.go; a cancelled context yields ctx.Err().
type Backend interface {
	// Version describes the underlying tool, for the startup log.
	Version(ctx context.Context) (string, error)
	// Probe checks that the repository can be opened with the given credentials.
	Probe(ctx context.Context, ref Ref) error

	// Snapshots lists all snapshots.
	Snapshots(ctx context.Context, ref Ref) ([]domain.Snapshot, error)
	// SnapshotSize computes the restored size of a snapshot that carries none.
	SnapshotSize(ctx context.Context, ref Ref, snapshotID string) (SnapshotSize, error)
	// Locks lists the locks currently held on the repository.
	Locks(ctx context.Context, ref Ref) ([]domain.Lock, error)
	// RepoSize is the space the repository occupies. Backends that cannot
	// tell return ErrUnsupported.
	RepoSize(ctx context.Context, ref Ref) (int64, error)

	// List returns the direct children of dir inside a snapshot.
	List(ctx context.Context, ref Ref, snapshotID, dir string) ([]Node, error)
	// Node returns a single entry of a snapshot.
	Node(ctx context.Context, ref Ref, snapshotID, p string) (Node, error)
	// Dump writes a file's bytes, or a folder as a tar archive when asTar is set.
	Dump(ctx context.Context, ref Ref, snapshotID, p string, asTar bool, w io.Writer) error

	// Forget removes the snapshots that the keep rules do not match.
	Forget(ctx context.Context, ref Ref, keep domain.KeepPolicy) (ForgetResult, error)
	// Prune removes unreferenced data; progress text goes to log.
	Prune(ctx context.Context, ref Ref, log io.Writer) (PruneResult, error)
}

// CleanPath normalises a path inside a snapshot to an absolute, slash-separated path.
func CleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Clean("/" + p)
}
