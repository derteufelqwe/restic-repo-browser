// Package restic implements backend.Backend with the restic command line
// program. It is the only package that knows restic's flags, exit codes and
// output formats. Repository passwords are passed through the child's
// environment, never on the command line.
package restic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
)

// Runner starts the restic executable. The production implementation uses
// os/exec; tests substitute a fake.
type Runner interface {
	Run(ctx context.Context, env, args []string, stdout, stderr io.Writer) error
}

type execRunner struct{ bin string }

func (r execRunner) Run(ctx context.Context, env, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

// Client runs restic commands.
type Client struct {
	runner   Runner
	cacheDir string
}

var _ backend.Backend = (*Client)(nil)

// New creates a Client for the restic executable at bin. cacheDir keeps restic's
// own cache inside the application's data directory.
func New(bin, cacheDir string, opts ...Option) *Client {
	return NewWithRunner(execRunner{bin: bin}, cacheDir, opts...)
}

// NewWithRunner creates a Client with a custom Runner (for tests).
func NewWithRunner(r Runner, cacheDir string, opts ...Option) *Client {
	c := &Client{runner: r, cacheDir: cacheDir}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// target translates a repository reference into restic's -r argument. This is
// the place to add `rest:<url>` for further kinds. For rclone, restic starts
// the rclone program itself, which must be configured on the host.
func (c *Client) target(ref backend.Ref) (string, error) {
	switch ref.Kind {
	case domain.BackendLocal:
		return ref.Location, nil
	case domain.BackendRclone:
		return "rclone:" + ref.Location, nil
	}
	return "", backend.Unsupported("repository kind %q is not supported", ref.Kind)
}

// environment is the child's environment: ours minus any RESTIC_* variables
// that could redirect the command to another repository, plus the credentials.
func (c *Client) environment(ref backend.Ref) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "RESTIC_") {
			env = append(env, kv)
		}
	}
	env = append(env, "RESTIC_PASSWORD="+ref.Password, "RESTIC_PROGRESS_FPS=0.0001")
	if c.cacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+c.cacheDir)
	}
	return env
}

// exec runs `restic -r <repo> args...` with caller-supplied output writers. A
// non-nil error is the raw exec error; wrapRunError classifies it.
func (c *Client) exec(ctx context.Context, ref backend.Ref, stdout, stderr io.Writer, args ...string) error {
	repo, err := c.target(ref)
	if err != nil {
		return err
	}
	return c.runner.Run(ctx, c.environment(ref), append([]string{"-r", repo}, args...), stdout, stderr)
}

// capture runs a command and returns its stdout.
func (c *Client) capture(ctx context.Context, ref backend.Ref, args ...string) ([]byte, error) {
	var out, errOut bytes.Buffer
	if err := c.exec(ctx, ref, &out, &errOut, args...); err != nil {
		return nil, wrapRunError(ctx, err, out.Bytes(), errOut.Bytes())
	}
	return out.Bytes(), nil
}

// stream runs a command whose stdout goes straight to w.
func (c *Client) stream(ctx context.Context, ref backend.Ref, w io.Writer, args ...string) error {
	var errOut bytes.Buffer
	if err := c.exec(ctx, ref, w, &errOut, args...); err != nil {
		return wrapRunError(ctx, err, nil, errOut.Bytes())
	}
	return nil
}

// Version returns the output of `restic version`.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out bytes.Buffer
	if err := c.runner.Run(ctx, os.Environ(), []string{"version"}, &out, io.Discard); err != nil {
		return "", wrapRunError(ctx, err, out.Bytes(), nil)
	}
	return strings.TrimSpace(out.String()), nil
}

// Probe checks that the repository can be opened with the given password.
func (c *Client) Probe(ctx context.Context, ref backend.Ref) error {
	_, err := c.capture(ctx, ref, "cat", "config", "--no-lock")
	return err
}

// Snapshots lists all snapshots of the repository.
func (c *Client) Snapshots(ctx context.Context, ref backend.Ref) ([]domain.Snapshot, error) {
	out, err := c.capture(ctx, ref, "snapshots", "--json", "--no-lock")
	if err != nil {
		return nil, err
	}
	var raw []snapshotJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("restic: parse snapshots: %w", err)
	}
	return toSnapshots(raw), nil
}

// SnapshotSize computes the restored size of one snapshot.
func (c *Client) SnapshotSize(ctx context.Context, ref backend.Ref, snapshotID string) (backend.SnapshotSize, error) {
	out, err := c.capture(ctx, ref, "stats", "--json", "--mode", "restore-size", "--no-lock", snapshotID)
	if err != nil {
		return backend.SnapshotSize{}, err
	}
	var st statsJSON
	if err := json.Unmarshal(out, &st); err != nil {
		return backend.SnapshotSize{}, fmt.Errorf("restic: parse stats: %w", err)
	}
	return backend.SnapshotSize{Bytes: st.TotalSize, Files: st.TotalFileCount}, nil
}

// Locks lists the locks currently present in the repository.
func (c *Client) Locks(ctx context.Context, ref backend.Ref) ([]domain.Lock, error) {
	out, err := c.capture(ctx, ref, "list", "locks", "--no-lock", "--quiet")
	if err != nil {
		return nil, err
	}
	var locks []domain.Lock
	for _, id := range strings.Fields(string(out)) {
		raw, err := c.capture(ctx, ref, "cat", "lock", id, "--no-lock")
		if err != nil {
			var be *backend.Error
			if errors.As(err, &be) && be.Kind == backend.KindOther {
				continue // the lock vanished between list and cat
			}
			return nil, err
		}
		var l struct {
			Time      time.Time `json:"time"`
			Exclusive bool      `json:"exclusive"`
			Hostname  string    `json:"hostname"`
			Username  string    `json:"username"`
			PID       int       `json:"pid"`
		}
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("restic: parse lock %s: %w", id, err)
		}
		locks = append(locks, domain.Lock{ID: id, Time: l.Time, Host: l.Hostname, Username: l.Username, PID: l.PID, Exclusive: l.Exclusive})
	}
	return locks, nil
}

// RepoSize is the space the repository occupies. A local repository is
// measured on disk; a remote one is asked for the size of its data.
func (c *Client) RepoSize(ctx context.Context, ref backend.Ref) (int64, error) {
	switch ref.Kind {
	case domain.BackendLocal:
		return dirSize(ref.Location)
	case domain.BackendRclone:
		out, err := c.capture(ctx, ref, "stats", "--json", "--mode", "raw-data", "--no-lock")
		if err != nil {
			return 0, err
		}
		var st statsJSON
		if err := json.Unmarshal(out, &st); err != nil {
			return 0, fmt.Errorf("restic: parse stats: %w", err)
		}
		return st.TotalSize, nil
	}
	return 0, backend.Unsupported("the size of %q repositories is not available", ref.Kind)
}

// List returns the direct children of dir inside a snapshot. dir uses forward
// slashes and starts with "/".
func (c *Client) List(ctx context.Context, ref backend.Ref, snapshotID, dir string) ([]backend.Node, error) {
	dir = backend.CleanPath(dir)
	out, err := c.capture(ctx, ref, "ls", "--json", "--no-lock", snapshotID, dir)
	if err != nil {
		return nil, err
	}
	var nodes []backend.Node
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var head struct {
			StructType string `json:"struct_type"`
		}
		if json.Unmarshal(line, &head) != nil || head.StructType != "node" {
			continue // the snapshot header line
		}
		var n nodeJSON
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, fmt.Errorf("restic: parse node: %w", err)
		}
		if n.Path == dir { // `ls` also reports the directory itself
			continue
		}
		nodes = append(nodes, backend.Node(n))
	}
	return nodes, nil
}

// Node finds a single entry of a snapshot by its path.
func (c *Client) Node(ctx context.Context, ref backend.Ref, snapshotID, p string) (backend.Node, error) {
	return backend.NodeVia(ctx, c, ref, snapshotID, p)
}

// Dump writes a file's bytes, or a directory as a tar archive when asTar is set, to w.
func (c *Client) Dump(ctx context.Context, ref backend.Ref, snapshotID, p string, asTar bool, w io.Writer) error {
	args := []string{"dump", "--no-lock"}
	if asTar {
		args = append(args, "--archive", "tar")
	}
	return c.stream(ctx, ref, w, append(args, snapshotID, backend.CleanPath(p))...)
}

// Forget removes the snapshots the keep rules do not match.
func (c *Client) Forget(ctx context.Context, ref backend.Ref, keep domain.KeepPolicy) (backend.ForgetResult, error) {
	if err := backend.RequireWritable(ref); err != nil {
		return backend.ForgetResult{}, err
	}
	if keep.Empty() {
		return backend.ForgetResult{}, errors.New("restic: refusing to forget without any keep rule")
	}
	args := append([]string{"forget", "--json", "--retry-lock", "5m"}, keepArgs(keep)...)
	out, err := c.capture(ctx, ref, args...)
	if err != nil {
		return backend.ForgetResult{}, err
	}
	var groups []struct {
		Keep   []snapshotJSON `json:"keep"`
		Remove []snapshotJSON `json:"remove"`
	}
	if err := json.Unmarshal(out, &groups); err != nil {
		return backend.ForgetResult{}, fmt.Errorf("restic: parse forget output: %w", err)
	}
	var res backend.ForgetResult
	for _, g := range groups {
		res.Kept += len(g.Keep)
		res.Removed = append(res.Removed, toSnapshots(g.Remove)...)
	}
	return res, nil
}

// prunedRe matches the summary line of `restic prune`: "total prune: 12 blobs / 3.4 MiB".
var prunedRe = regexp.MustCompile(`(?m)^total prune:\s+\d+ blobs / (\S+ \S+)`)

// Prune removes unreferenced data; restic's progress text goes to log.
func (c *Client) Prune(ctx context.Context, ref backend.Ref, log io.Writer) (backend.PruneResult, error) {
	if err := backend.RequireWritable(ref); err != nil {
		return backend.PruneResult{}, err
	}
	var output bytes.Buffer
	w := io.MultiWriter(log, &output)
	if err := c.exec(ctx, ref, w, w, "prune", "--retry-lock", "5m"); err != nil {
		return backend.PruneResult{}, wrapRunError(ctx, err, nil, output.Bytes())
	}
	var res backend.PruneResult
	if m := prunedRe.FindStringSubmatch(output.String()); m != nil {
		res.Freed = m[1]
	}
	return res, nil
}
