package restic

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/testutil"
)

const testPassword = "test-pw"

// fixture is a real repository with two snapshots on disk.
type fixture struct {
	client *Client
	ref    backend.Ref
	snaps  []domain.Snapshot
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	bin := testutil.Binary(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	testutil.WriteFile(t, filepath.Join(src, "a.txt"), "hello")
	testutil.WriteFile(t, filepath.Join(src, "sub", "b.txt"), "world!")

	repo := filepath.Join(dir, "repo")
	testutil.Restic(t, bin, repo, testPassword, "init", "--quiet")
	testutil.Restic(t, bin, repo, testPassword, "backup", src, "--host", "h1", "--tag", "t1", "--time", "2026-01-01 10:00:00", "--quiet")
	testutil.WriteFile(t, filepath.Join(src, "c.txt"), "more data")
	testutil.Restic(t, bin, repo, testPassword, "backup", src, "--host", "h1", "--time", "2026-01-02 10:00:00", "--quiet")

	f := &fixture{
		client: New(bin, filepath.Join(dir, "cache")),
		ref:    backend.Ref{Kind: domain.BackendLocal, Location: repo, Password: testPassword},
	}
	var err error
	f.snaps, err = f.client.Snapshots(context.Background(), f.ref)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// snapshotRoot converts a snapshot's native source path to the path restic uses inside the snapshot.
func snapshotRoot(s domain.Snapshot) string {
	p := s.Paths[0]
	if v := filepath.VolumeName(p); v != "" {
		return "/" + strings.TrimSuffix(v, ":") + filepath.ToSlash(strings.TrimPrefix(p, v))
	}
	return filepath.ToSlash(p)
}

func TestSnapshotsAndSizes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if len(f.snaps) != 2 {
		t.Fatalf("got %d snapshots", len(f.snaps))
	}
	first := f.snaps[0]
	if first.Host != "h1" || !reflect.DeepEqual(first.Tags, []string{"t1"}) || first.ShortID == "" {
		t.Errorf("snapshot = %+v", first)
	}
	if first.FileCount() != 2 || first.SizeBytes() != 11 || first.AddedBytes() <= 0 {
		t.Errorf("summary values = %d files, %d bytes, %d added", first.FileCount(), first.SizeBytes(), first.AddedBytes())
	}
	sz, err := f.client.SnapshotSize(ctx, f.ref, f.snaps[1].ID)
	if err != nil || sz.Bytes != 5+6+9 || sz.Files < 3 {
		t.Errorf("size = %+v, %v", sz, err)
	}
	if size, err := f.client.RepoSize(ctx, f.ref); err != nil || size <= 0 {
		t.Errorf("repo size = %d, %v", size, err)
	}
	if locks, err := f.client.Locks(ctx, f.ref); err != nil || len(locks) != 0 {
		t.Errorf("locks = %v, %v", locks, err)
	}
}

func TestBrowseAndDump(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.snaps[1].ID
	src := snapshotRoot(f.snaps[1])

	nodes, err := f.client.List(ctx, f.ref, id, src)
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
	if n, err := f.client.Node(ctx, f.ref, id, src+"/sub"); err != nil || !n.IsDir() {
		t.Errorf("node = %+v, %v", n, err)
	}
	if _, err := f.client.Node(ctx, f.ref, id, src+"/missing"); err == nil {
		t.Error("expected an error for a missing path")
	}

	var file bytes.Buffer
	if err := f.client.Dump(ctx, f.ref, id, src+"/a.txt", false, &file); err != nil || file.String() != "hello" {
		t.Fatalf("dump file = %q, %v", file.String(), err)
	}
	var archive bytes.Buffer
	if err := f.client.Dump(ctx, f.ref, id, src, true, &archive); err != nil {
		t.Fatal(err)
	}
	entries := 0
	for tr := tar.NewReader(&archive); ; entries++ {
		if _, err := tr.Next(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if entries < 4 {
		t.Errorf("expected at least 4 tar entries, got %d", entries)
	}
}

func TestErrorKinds(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	wrong := f.ref
	wrong.Password = "wrong"
	if err := f.client.Probe(ctx, wrong); !errors.Is(err, backend.ErrWrongPassword) {
		t.Errorf("wrong password: err = %v", err)
	}
	missing := backend.Ref{Kind: domain.BackendLocal, Location: filepath.Join(t.TempDir(), "nope"), Password: "x"}
	err := f.client.Probe(ctx, missing)
	if !errors.Is(err, backend.ErrNotFound) || !strings.Contains(backend.UserMessage(err), "No restic repository") {
		t.Errorf("missing repo: err = %v", err)
	}
	if err := f.client.Probe(ctx, f.ref); err != nil {
		t.Errorf("good probe: %v", err)
	}
	noBinary := New(filepath.Join(t.TempDir(), "no-such-restic"), "")
	if err := noBinary.Probe(ctx, f.ref); !errors.Is(err, backend.ErrBinaryMissing) {
		t.Errorf("missing binary: err = %v", err)
	}
	other := f.ref
	other.Kind = "carrier-pigeon"
	if err := f.client.Probe(ctx, other); !errors.Is(err, backend.ErrUnsupported) {
		t.Errorf("unknown kind: err = %v", err)
	}
}

func TestForgetAndPrune(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.client.Forget(ctx, f.ref, domain.KeepPolicy{}); err == nil {
		t.Error("an empty policy must be refused")
	}
	res, err := f.client.Forget(ctx, f.ref, domain.KeepPolicy{Last: 1})
	if err != nil || res.Kept != 1 || len(res.Removed) != 1 || res.Removed[0].ID != f.snaps[0].ID {
		t.Fatalf("forget = %+v, %v", res, err)
	}
	var log bytes.Buffer
	if _, err := f.client.Prune(ctx, f.ref, &log); err != nil {
		t.Fatalf("prune: %v\n%s", err, log.String())
	}
	if snaps, _ := f.client.Snapshots(ctx, f.ref); len(snaps) != 1 {
		t.Errorf("snapshots after forget = %d", len(snaps))
	}
}

// fakeRunner scripts restic responses for tests that need no binary.
type fakeRunner struct {
	handle func(args []string) (stdout string, code int)
	env    []string
}

type exitCode int

func (e exitCode) Error() string { return "exit status" }
func (e exitCode) ExitCode() int { return int(e) }

func (r *fakeRunner) Run(_ context.Context, env, args []string, stdout, _ io.Writer) error {
	r.env = env
	out, code := r.handle(args)
	io.WriteString(stdout, out)
	if code != 0 {
		return exitCode(code)
	}
	return nil
}

var fakeRef = backend.Ref{Kind: domain.BackendLocal, Location: "/r", Password: "secret"}

func TestLocksParsingAndEnvironment(t *testing.T) {
	t.Setenv("RESTIC_REPOSITORY", "/elsewhere")
	runner := &fakeRunner{handle: func(args []string) (string, int) {
		switch {
		case args[2] == "list":
			return "abc123\ndef456\n", 0
		case args[2] == "cat" && args[4] == "abc123":
			return `{"time":"2026-11-11T14:17:00+01:00","exclusive":true,"hostname":"docker-01","username":"root","pid":2231}`, 0
		default:
			return "", 1 // vanished between list and cat
		}
	}}
	locks, err := NewWithRunner(runner, "/cache").Locks(context.Background(), fakeRef)
	if err != nil || len(locks) != 1 {
		t.Fatalf("locks = %v, %v", locks, err)
	}
	if l := locks[0]; l.ID != "abc123" || !l.Exclusive || l.Host != "docker-01" || l.PID != 2231 || l.Time.Year() != 2026 {
		t.Errorf("lock = %+v", l)
	}

	var sawPassword, sawCache bool
	for _, kv := range runner.env {
		if strings.HasPrefix(kv, "RESTIC_REPOSITORY") {
			t.Errorf("inherited variable leaked: %s", kv)
		}
		sawPassword = sawPassword || kv == "RESTIC_PASSWORD=secret"
		sawCache = sawCache || kv == "RESTIC_CACHE_DIR=/cache"
	}
	if !sawPassword || !sawCache {
		t.Errorf("child environment lacks credentials: password=%v cache=%v", sawPassword, sawCache)
	}
}

func TestContextCancellationIsNotAResticError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{handle: func([]string) (string, int) { return "", 1 }}
	if err := NewWithRunner(runner, "").Probe(ctx, fakeRef); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
