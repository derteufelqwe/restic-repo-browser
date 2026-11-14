package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"restic-repo-browser/internal/domain"
)

// xorCipher is a trivial reversible cipher so tests do not need real keys.
type xorCipher struct{}

func (xorCipher) Seal(b []byte) ([]byte, error) { return append([]byte("enc:"), b...), nil }
func (xorCipher) Open(b []byte) ([]byte, error) { return b[4:], nil }

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), xorCipher{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var (
	ctx = context.Background()
	t0  = time.Date(2026, 11, 11, 14, 0, 0, 0, time.Local)
)

func i64(v int64) *int64 { return &v }

func TestRepoLifecycle(t *testing.T) {
	s := open(t)
	id, err := s.CreateRepo(ctx, NewRepo{Name: "photos", Path: "/mnt/photos", Password: "pw"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRepo(ctx, NewRepo{Name: "photos", Path: "/x", Password: "x"}, t0); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate name: err = %v", err)
	}
	if pw, _ := s.RepoPassword(ctx, id); pw != "pw" {
		t.Errorf("password = %q", pw)
	}
	if err := s.UpdateRepo(ctx, id, NewRepo{Name: "pics", Path: "/mnt/pics"}); err != nil {
		t.Fatal(err)
	}
	if pw, _ := s.RepoPassword(ctx, id); pw != "pw" {
		t.Errorf("empty password in update must keep the old one, got %q", pw)
	}
	if err := s.UpdateRepo(ctx, id, NewRepo{Name: "pics", Path: "/mnt/pics", Password: "new"}); err != nil {
		t.Fatal(err)
	}
	if pw, _ := s.RepoPassword(ctx, id); pw != "new" {
		t.Errorf("password = %q", pw)
	}
	r, err := s.Repo(ctx, id)
	if err != nil || r.Name != "pics" || r.Path != "/mnt/pics" || r.BackupCron != "" {
		t.Fatalf("repo = %+v, %v", r, err)
	}
	if err := s.UpdateRepo(ctx, id, NewRepo{Name: "pics", Path: "/mnt/pics", BackupCron: "0 3 * * 0"}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Repo(ctx, id); r.BackupCron != "0 3 * * 0" {
		t.Errorf("backup schedule = %q", r.BackupCron)
	}
	if err := s.DeleteRepo(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Repo(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v", err)
	}
}

func TestRefreshKeepsSizeOnFailure(t *testing.T) {
	s := open(t)
	id, _ := s.CreateRepo(ctx, NewRepo{Name: "r", Path: "/r", Password: "p"}, t0)
	if err := s.SaveRefresh(ctx, id, RefreshResult{At: t0, Size: 500}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRefresh(ctx, id, RefreshResult{At: t0.Add(time.Hour), Err: "boom"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Repo(ctx, id)
	if r.SizeBytes != 500 || r.RefreshError != "boom" || !r.RefreshOKAt.Equal(t0) || !r.RefreshedAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("repo after failed refresh = %+v", r)
	}
}

func TestSyncSnapshots(t *testing.T) {
	s := open(t)
	id, _ := s.CreateRepo(ctx, NewRepo{Name: "r", Path: "/r", Password: "p"}, t0)
	a := domain.Snapshot{ID: "aaa", ShortID: "aa", Time: t0, Host: "h", Paths: []string{"/p"}, Tags: []string{"x"}, Size: i64(10), Added: i64(5), Files: i64(2)}
	b := domain.Snapshot{ID: "bbb", ShortID: "bb", Time: t0.Add(time.Hour), Host: "h", Paths: []string{"/p"}}
	if err := s.SyncSnapshots(ctx, id, []domain.Snapshot{a, b}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.SnapshotsWithoutSize(ctx, id); len(ids) != 1 || ids[0] != "bbb" {
		t.Errorf("without size = %v", ids)
	}
	if err := s.SetSnapshotSize(ctx, id, "bbb", 99, 7); err != nil {
		t.Fatal(err)
	}

	// Re-sync: a loses its summary in the input (must keep cached size), b vanished, c is new, a's tags changed.
	a.Size, a.Added, a.Files, a.Tags = nil, nil, nil, []string{"x", "y"}
	c := domain.Snapshot{ID: "ccc", ShortID: "cc", Time: t0.Add(2 * time.Hour), Host: "h2", Paths: []string{"/q"}, Size: i64(1)}
	if err := s.SyncSnapshots(ctx, id, []domain.Snapshot{a, c}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Snapshots(ctx, id)
	if err != nil || len(got) != 2 {
		t.Fatalf("snapshots = %v, %v", got, err)
	}
	if got[0].ID != "aaa" || got[0].SizeBytes() != 10 || got[0].AddedBytes() != 5 || len(got[0].Tags) != 2 {
		t.Errorf("snapshot a = %+v", got[0])
	}
	if got[1].ID != "ccc" || got[1].Added != nil || got[1].Host != "h2" {
		t.Errorf("snapshot c = %+v", got[1])
	}
}

func TestLocks(t *testing.T) {
	s := open(t)
	id, _ := s.CreateRepo(ctx, NewRepo{Name: "r", Path: "/r", Password: "p"}, t0)
	if err := s.ReplaceLocks(ctx, id, []domain.Lock{{ID: "l1", Time: t0, Host: "docker-01", Username: "root", PID: 7, Exclusive: true}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Locks(ctx, id)
	if len(got) != 1 || !got[0].Exclusive || got[0].Host != "docker-01" {
		t.Fatalf("locks = %+v", got)
	}
	_ = s.ReplaceLocks(ctx, id, nil)
	if got, _ := s.Locks(ctx, id); len(got) != 0 {
		t.Errorf("locks not cleared: %+v", got)
	}
}

func TestJobAndRuns(t *testing.T) {
	s := open(t)
	id, _ := s.CreateRepo(ctx, NewRepo{Name: "r", Path: "/r", Password: "p"}, t0)
	if j, err := s.Job(ctx, id); j != nil || err != nil {
		t.Fatalf("job before save = %v, %v", j, err)
	}
	job := domain.Job{RepoID: id, Cron: "30 3 * * *", Keep: domain.KeepPolicy{Last: 7, Daily: 30}, Prune: true, Enabled: true}
	if err := s.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Keep.Last = 3
	job.Enabled = false
	if err := s.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Job(ctx, id)
	if got == nil || got.Keep.Last != 3 || got.Keep.Daily != 30 || got.Enabled || !got.Prune {
		t.Fatalf("job = %+v", got)
	}

	for i := 0; i < keepRuns+5; i++ {
		rid, err := s.StartRun(ctx, id, t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, rid, t0.Add(time.Duration(i)*time.Minute+time.Second), domain.RunOK, "done", "log"); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := s.RepoRuns(ctx, id, 1000)
	if len(runs) != keepRuns {
		t.Errorf("runs kept = %d, want %d", len(runs), keepRuns)
	}
	if runs[0].RepoName != "r" || runs[0].Status != domain.RunOK || runs[0].Duration(t0) != time.Second {
		t.Errorf("newest run = %+v", runs[0])
	}

	rid, _ := s.StartRun(ctx, id, t0)
	if err := s.AbandonRunningRuns(ctx, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	recent, _ := s.RecentRuns(ctx, 100)
	for _, r := range recent {
		if r.ID == rid && r.Status != domain.RunFailed {
			t.Errorf("running run was not abandoned: %+v", r)
		}
	}
}

func TestAuthAndSessions(t *testing.T) {
	s := open(t)
	if _, ok, _ := s.PasswordHash(ctx); ok {
		t.Fatal("no password expected initially")
	}
	if err := s.SetPasswordHash(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if h, ok, _ := s.PasswordHash(ctx); !ok || h != "h1" {
		t.Fatalf("hash = %q, %v", h, ok)
	}
	if err := s.CreateSession(ctx, "tok", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, "tok", t0.Add(time.Minute)); !ok {
		t.Error("session should be valid")
	}
	if ok, _ := s.SessionValid(ctx, "tok", t0.Add(2*time.Hour)); ok {
		t.Error("session should have expired")
	}
	_ = s.SetPasswordHash(ctx, "h2")
	if ok, _ := s.SessionValid(ctx, "tok", t0.Add(time.Minute)); ok {
		t.Error("changing the password must end all sessions")
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	if v, _ := s.Setting(ctx, SettingCacheRefreshTime, "03:00"); v != "03:00" {
		t.Errorf("default = %q", v)
	}
	_ = s.SetSetting(ctx, SettingCacheRefreshTime, "04:30")
	_ = s.SetSetting(ctx, SettingCacheRefreshTime, "05:15")
	if v, _ := s.Setting(ctx, SettingCacheRefreshTime, "03:00"); v != "05:15" {
		t.Errorf("value = %q", v)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(ctx, path, xorCipher{})
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		s.Close()
	}
}
