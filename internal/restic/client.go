// Package restic runs the restic command line program and parses its output.
// Repository passwords are passed through the child's environment, never on
// the command line.
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
	"path"
	"strconv"
	"strings"

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

// New creates a Client for the restic executable at bin. cacheDir keeps restic's
// own cache inside the application's data directory.
func New(bin, cacheDir string) *Client {
	return &Client{runner: execRunner{bin: bin}, cacheDir: cacheDir}
}

// NewWithRunner creates a Client with a custom Runner (for tests).
func NewWithRunner(r Runner, cacheDir string) *Client {
	return &Client{runner: r, cacheDir: cacheDir}
}

// environment is the child's environment: ours minus any RESTIC_* variables
// that could redirect the command to another repository, plus the credentials.
func (c *Client) environment(repo Repo) []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "RESTIC_") {
			env = append(env, kv)
		}
	}
	env = append(env, "RESTIC_PASSWORD="+repo.Password, "RESTIC_PROGRESS_FPS=0.0001")
	if c.cacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+c.cacheDir)
	}
	return env
}

// run executes `restic -r <path> args...` and returns its stdout.
func (c *Client) run(ctx context.Context, repo Repo, args ...string) ([]byte, error) {
	var out, errOut bytes.Buffer
	if err := c.stream(ctx, repo, &out, &errOut, args...); err != nil {
		return nil, wrapRunError(ctx, err, out.Bytes(), errOut.Bytes())
	}
	return out.Bytes(), nil
}

// stream executes a command with caller-supplied output writers. A non-nil error
// is the raw exec error; use wrapRunError to classify it.
func (c *Client) stream(ctx context.Context, repo Repo, stdout, stderr io.Writer, args ...string) error {
	full := append([]string{"-r", repo.Path}, args...)
	return c.runner.Run(ctx, c.environment(repo), full, stdout, stderr)
}

// streamChecked is stream with errors classified.
func (c *Client) streamChecked(ctx context.Context, repo Repo, stdout io.Writer, args ...string) error {
	var errOut bytes.Buffer
	if err := c.stream(ctx, repo, stdout, &errOut, args...); err != nil {
		return wrapRunError(ctx, err, nil, errOut.Bytes())
	}
	return nil
}

func wrapRunError(ctx context.Context, err error, stdout, stderr []byte) error {
	if ctx.Err() != nil {
		return ctx.Err() // cancelled or timed out: not a restic failure
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		return &Error{Kind: classify(code), Code: code, Message: messageFrom(stdout, stderr)}
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) || errors.Is(err, os.ErrNotExist) {
		return &Error{Kind: KindBinaryMissing, Code: -1, Message: err.Error()}
	}
	var coded interface{ ExitCode() int } // fakes in tests
	if errors.As(err, &coded) {
		code := coded.ExitCode()
		return &Error{Kind: classify(code), Code: code, Message: messageFrom(stdout, stderr)}
	}
	return err
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
func (c *Client) Probe(ctx context.Context, repo Repo) error {
	_, err := c.run(ctx, repo, "cat", "config", "--no-lock")
	return err
}

// Snapshots lists all snapshots of the repository.
func (c *Client) Snapshots(ctx context.Context, repo Repo) ([]Snapshot, error) {
	out, err := c.run(ctx, repo, "snapshots", "--json", "--no-lock")
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("restic: parse snapshots: %w", err)
	}
	return snaps, nil
}

// SnapshotStats computes the restored size of one snapshot.
func (c *Client) SnapshotStats(ctx context.Context, repo Repo, snapshotID string) (Stats, error) {
	out, err := c.run(ctx, repo, "stats", "--json", "--mode", "restore-size", "--no-lock", snapshotID)
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	if err := json.Unmarshal(out, &st); err != nil {
		return Stats{}, fmt.Errorf("restic: parse stats: %w", err)
	}
	return st, nil
}

// Locks lists the locks currently present in the repository.
func (c *Client) Locks(ctx context.Context, repo Repo) ([]Lock, error) {
	out, err := c.run(ctx, repo, "list", "locks", "--no-lock", "--quiet")
	if err != nil {
		return nil, err
	}
	var locks []Lock
	for _, id := range strings.Fields(string(out)) {
		raw, err := c.run(ctx, repo, "cat", "lock", id, "--no-lock")
		if err != nil {
			var re *Error
			if errors.As(err, &re) && re.Kind == KindOther {
				continue // the lock vanished between list and cat
			}
			return nil, err
		}
		l := Lock{ID: id}
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, fmt.Errorf("restic: parse lock %s: %w", id, err)
		}
		locks = append(locks, l)
	}
	return locks, nil
}

// List returns the direct children of dir inside a snapshot. dir uses forward
// slashes and starts with "/".
func (c *Client) List(ctx context.Context, repo Repo, snapshotID, dir string) ([]Node, error) {
	dir = CleanPath(dir)
	out, err := c.run(ctx, repo, "ls", "--json", "--no-lock", snapshotID, dir)
	if err != nil {
		return nil, err
	}
	var nodes []Node
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
		var n Node
		if err := json.Unmarshal(line, &n); err != nil {
			return nil, fmt.Errorf("restic: parse node: %w", err)
		}
		if n.Path == dir { // `ls` also reports the directory itself
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// Node finds a single entry of a snapshot by its path.
func (c *Client) Node(ctx context.Context, repo Repo, snapshotID, p string) (Node, error) {
	p = CleanPath(p)
	if p == "/" {
		return Node{Name: "", Type: "dir", Path: "/"}, nil
	}
	nodes, err := c.List(ctx, repo, snapshotID, path.Dir(p))
	if err != nil {
		return Node{}, err
	}
	for _, n := range nodes {
		if n.Path == p {
			return n, nil
		}
	}
	return Node{}, &Error{Kind: KindOther, Code: 1, Message: "path not found in snapshot: " + p}
}

// Dump writes a file's bytes, or a directory as a tar archive when asTar is set, to w.
func (c *Client) Dump(ctx context.Context, repo Repo, snapshotID, p string, asTar bool, w io.Writer) error {
	args := []string{"dump", "--no-lock"}
	if asTar {
		args = append(args, "--archive", "tar")
	}
	args = append(args, snapshotID, CleanPath(p))
	return c.streamChecked(ctx, repo, w, args...)
}

// ForgetResult summarises what `restic forget` did.
type ForgetResult struct {
	Groups  []ForgetGroup
	Removed int
	Kept    int
}

// Forget applies keep rules and forgets the other snapshots. With dryRun set
// nothing is changed.
func (c *Client) Forget(ctx context.Context, repo Repo, keep domain.KeepPolicy, dryRun bool) (ForgetResult, error) {
	if keep.Empty() {
		return ForgetResult{}, errors.New("restic: refusing to forget without any keep rule")
	}
	args := append([]string{"forget", "--json", "--retry-lock", "5m"}, KeepArgs(keep)...)
	if dryRun {
		args = append(args, "--dry-run")
	}
	out, err := c.run(ctx, repo, args...)
	if err != nil {
		return ForgetResult{}, err
	}
	var res ForgetResult
	if err := json.Unmarshal(out, &res.Groups); err != nil {
		return ForgetResult{}, fmt.Errorf("restic: parse forget output: %w", err)
	}
	for _, g := range res.Groups {
		res.Removed += len(g.Remove)
		res.Kept += len(g.Keep)
	}
	return res, nil
}

// Prune removes unreferenced data; its progress text goes to log.
func (c *Client) Prune(ctx context.Context, repo Repo, log io.Writer) error {
	return c.logged(ctx, repo, log, "prune", "--retry-lock", "5m")
}

// logged runs a command whose stdout and stderr both go to log.
func (c *Client) logged(ctx context.Context, repo Repo, log io.Writer, args ...string) error {
	var errOut bytes.Buffer
	w := io.MultiWriter(log, &errOut)
	if err := c.stream(ctx, repo, w, w, args...); err != nil {
		return wrapRunError(ctx, err, nil, errOut.Bytes())
	}
	return nil
}

// KeepArgs converts a keep policy into restic command line flags.
func KeepArgs(k domain.KeepPolicy) []string {
	var a []string
	add := func(flag string, n int) {
		if n > 0 {
			a = append(a, flag, strconv.Itoa(n))
		}
	}
	add("--keep-last", k.Last)
	add("--keep-hourly", k.Hourly)
	add("--keep-daily", k.Daily)
	add("--keep-weekly", k.Weekly)
	add("--keep-monthly", k.Monthly)
	add("--keep-yearly", k.Yearly)
	return a
}

// CleanPath normalises a path inside a snapshot to an absolute, slash-separated path.
func CleanPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return path.Clean("/" + p)
}
