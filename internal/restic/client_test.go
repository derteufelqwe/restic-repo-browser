package restic

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"restic-repo-browser/internal/domain"
)

const testPassword = "test-pw"

// findRestic locates a restic binary for integration tests, or skips the test.
func findRestic(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("RESTIC_BIN"), filepath.Join("..", "..", "restic.exe"), filepath.Join("..", "..", "restic")}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if abs, err := filepath.Abs(c); err == nil {
			if _, err := os.Stat(abs); err == nil {
				return abs
			}
		}
	}
	if p, err := exec.LookPath("restic"); err == nil {
		return p
	}
	t.Skip("restic binary not found; set RESTIC_BIN to run integration tests")
	return ""
}

// fixture is a real repository with two snapshots on disk.
type fixture struct {
	client *Client
	repo   Repo
	bin    string
	snaps  []Snapshot
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bin := findRestic(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	must(t, os.MkdirAll(filepath.Join(src, "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world!"), 0o644))

	f := &fixture{bin: bin, repo: Repo{Path: filepath.Join(dir, "repo"), Password: testPassword}}
	f.client = New(bin, filepath.Join(dir, "cache"))
	f.restic(t, "init", "--quiet")
	f.restic(t, "backup", src, "--host", "h1", "--tag", "t1", "--time", "2026-01-01 10:00:00", "--quiet")
	must(t, os.WriteFile(filepath.Join(src, "c.txt"), []byte("more data"), 0o644))
	f.restic(t, "backup", src, "--host", "h1", "--time", "2026-01-02 10:00:00", "--quiet")
	var err error
	f.snaps, err = f.client.Snapshots(context.Background(), f.repo)
	must(t, err)
	return f
}

func (f *fixture) restic(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command(f.bin, append([]string{"-r", f.repo.Path}, args...)...)
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD="+f.repo.Password)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restic %v: %v\n%s", args, err, out)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotsHaveSummary(t *testing.T) {
	f := newFixture(t)
	if len(f.snaps) != 2 {
		t.Fatalf("got %d snapshots", len(f.snaps))
	}
	first := f.snaps[0]
	if first.Hostname != "h1" || !reflect.DeepEqual(first.Tags, []string{"t1"}) || first.ShortID == "" {
		t.Errorf("snapshot = %+v", first)
	}
	if first.Summary == nil || first.Summary.TotalFilesProcessed != 2 || first.Summary.TotalBytesProcessed != 11 || first.Summary.DataAdded <= 0 {
		t.Errorf("summary = %+v", first.Summary)
	}
}

func TestEmptyRepoHasNoSnapshots(t *testing.T) {
	bin := findRestic(t)
	dir := t.TempDir()
	repo := Repo{Path: filepath.Join(dir, "repo"), Password: "x"}
	cmd := exec.Command(bin, "-r", repo.Path, "init", "--quiet")
	cmd.Env = append(os.Environ(), "RESTIC_PASSWORD=x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	snaps, err := New(bin, "").Snapshots(context.Background(), repo)
	if err != nil || len(snaps) != 0 {
		t.Errorf("snaps = %v, err = %v", snaps, err)
	}
}

func TestErrorKinds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	err := f.client.Probe(ctx, Repo{Path: f.repo.Path, Password: "wrong"})
	if !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong password: err = %v", err)
	}
	err = f.client.Probe(ctx, Repo{Path: filepath.Join(t.TempDir(), "nope"), Password: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("missing repo: err = %v", err)
	}
	if msg := UserMessage(err); !strings.Contains(msg, "No restic repository") {
		t.Errorf("message = %q", msg)
	}
	if err := f.client.Probe(ctx, f.repo); err != nil {
		t.Errorf("good probe: %v", err)
	}
	missing := New(filepath.Join(t.TempDir(), "no-such-restic"), "")
	if err := missing.Probe(ctx, f.repo); !errors.Is(err, ErrBinaryMissing) {
		t.Errorf("missing binary: err = %v", err)
	}
}

func TestStats(t *testing.T) {
	f := newFixture(t)
	st, err := f.client.SnapshotStats(context.Background(), f.repo, f.snaps[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalSize != 5+6+9 || st.TotalFileCount < 3 {
		t.Errorf("stats = %+v", st)
	}
}

func TestListAndNode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.snaps[1].ID

	src := snapshotRoot(f.snaps[1])
	nodes, err := f.client.List(ctx, f.repo, id, src)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range nodes {
		names = append(names, n.Name)
	}
	if !reflect.DeepEqual(names, []string{"a.txt", "c.txt", "sub"}) {
		t.Fatalf("children of %s = %v", src, names)
	}
	n, err := f.client.Node(ctx, f.repo, id, src+"/sub")
	if err != nil || !n.IsDir() {
		t.Errorf("node = %+v, %v", n, err)
	}
	if _, err := f.client.Node(ctx, f.repo, id, src+"/missing"); err == nil {
		t.Error("expected error for a missing path")
	}
	root, err := f.client.Node(ctx, f.repo, id, "/")
	if err != nil || !root.IsDir() {
		t.Errorf("root = %+v, %v", root, err)
	}
}

func TestDumpFileAndTar(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.snaps[1].ID
	src := snapshotRoot(f.snaps[1])

	var file bytes.Buffer
	if err := f.client.Dump(ctx, f.repo, id, src+"/a.txt", false, &file); err != nil || file.String() != "hello" {
		t.Fatalf("dump file = %q, %v", file.String(), err)
	}

	var archive bytes.Buffer
	if err := f.client.Dump(ctx, f.repo, id, src, true, &archive); err != nil {
		t.Fatal(err)
	}
	var names []string
	tr := tar.NewReader(&archive)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		names = append(names, h.Name)
	}
	t.Logf("tar entries: %v", names)
	if len(names) < 4 {
		t.Errorf("expected at least 4 tar entries, got %v", names)
	}
}

func TestForgetDryRunAndReal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.client.Forget(ctx, f.repo, domain.KeepPolicy{}, true); err == nil {
		t.Error("an empty policy must be refused")
	}
	res, err := f.client.Forget(ctx, f.repo, domain.KeepPolicy{Last: 1}, true)
	if err != nil || res.Removed != 1 || res.Kept != 1 {
		t.Fatalf("dry run = %+v, %v", res, err)
	}
	if snaps, _ := f.client.Snapshots(ctx, f.repo); len(snaps) != 2 {
		t.Fatal("dry run must not forget anything")
	}
	if _, err := f.client.Forget(ctx, f.repo, domain.KeepPolicy{Last: 1}, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	if err := f.client.Prune(ctx, f.repo, &log); err != nil {
		t.Fatalf("prune: %v\n%s", err, log.String())
	}
	t.Logf("prune output:\n%s", log.String())
	if snaps, _ := f.client.Snapshots(ctx, f.repo); len(snaps) != 1 {
		t.Errorf("snapshots after forget = %d", len(snaps))
	}
}

func TestLocks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if locks, err := f.client.Locks(ctx, f.repo); err != nil || len(locks) != 0 {
		t.Fatalf("locks = %v, %v", locks, err)
	}
	// Creating a real lock needs a concurrently running restic process; the
	// parsing of lock files is covered by TestLocksParsing with a fake runner.
}

func TestKeepArgs(t *testing.T) {
	got := KeepArgs(domain.KeepPolicy{Last: 3, Daily: 7, Yearly: 2})
	want := []string{"--keep-last", "3", "--keep-daily", "7", "--keep-yearly", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("KeepArgs = %v", got)
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{"": "/", "/": "/", "a/b": "/a/b", "/a//b/": "/a/b", `C:\x`: "/C:/x", "/a/../b": "/b"}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeRunner scripts restic responses for parsing tests that need no binary.
type fakeRunner struct {
	handle func(args []string) (stdout string, code int)
	env    []string
}

type exitCode int

func (e exitCode) Error() string { return "exit status" }
func (e exitCode) ExitCode() int { return int(e) }

func (r *fakeRunner) Run(_ context.Context, env, args []string, stdout, stderr io.Writer) error {
	r.env = env
	out, code := r.handle(args)
	io.WriteString(stdout, out)
	if code != 0 {
		return exitCode(code)
	}
	return nil
}

func TestLocksParsing(t *testing.T) {
	runner := &fakeRunner{handle: func(args []string) (string, int) {
		switch {
		case args[2] == "list":
			return "abc123\ndef456\n", 0
		case args[2] == "cat" && args[4] == "abc123":
			return `{"time":"2026-11-11T14:17:00+01:00","exclusive":true,"hostname":"docker-01","username":"root","pid":2231,"uid":0,"gid":0}`, 0
		default:
			return "", 1 // vanished between list and cat
		}
	}}
	c := NewWithRunner(runner, "/cache")
	locks, err := c.Locks(context.Background(), Repo{Path: "/r", Password: "secret"})
	if err != nil || len(locks) != 1 {
		t.Fatalf("locks = %v, %v", locks, err)
	}
	l := locks[0]
	if l.ID != "abc123" || !l.Exclusive || l.Hostname != "docker-01" || l.PID != 2231 || l.Time.Year() != 2026 {
		t.Errorf("lock = %+v", l)
	}
	var sawPassword, sawCache bool
	for _, kv := range runner.env {
		sawPassword = sawPassword || kv == "RESTIC_PASSWORD=secret"
		sawCache = sawCache || kv == "RESTIC_CACHE_DIR=/cache"
	}
	if !sawPassword || !sawCache {
		t.Errorf("child environment lacks credentials: password=%v cache=%v", sawPassword, sawCache)
	}
}

func TestEnvironmentDropsInheritedResticVariables(t *testing.T) {
	t.Setenv("RESTIC_REPOSITORY", "/elsewhere")
	t.Setenv("RESTIC_PASSWORD_FILE", "/some/file")
	env := New("restic", "").environment(Repo{Path: "/r", Password: "p"})
	for _, kv := range env {
		if strings.HasPrefix(kv, "RESTIC_REPOSITORY") || strings.HasPrefix(kv, "RESTIC_PASSWORD_FILE") {
			t.Errorf("inherited variable leaked: %s", kv)
		}
	}
}

func TestContextCancellationIsNotAResticError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{handle: func([]string) (string, int) { return "", 1 }}
	err := NewWithRunner(runner, "").Probe(ctx, Repo{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

// snapshotRoot converts a snapshot's native source path to the path restic uses inside the snapshot.
func snapshotRoot(s Snapshot) string {
	p := s.Paths[0]
	if v := filepath.VolumeName(p); v != "" {
		return "/" + strings.TrimSuffix(v, ":") + filepath.ToSlash(strings.TrimPrefix(p, v))
	}
	return filepath.ToSlash(p)
}
