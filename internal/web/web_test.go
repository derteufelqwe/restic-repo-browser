package web

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"restic-repo-browser/internal/auth"
	"restic-repo-browser/internal/cache"
	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/jobs"
	"restic-repo-browser/internal/restic"
	"restic-repo-browser/internal/secret"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
	"restic-repo-browser/internal/testutil"
)

// env is a complete application (store, restic, cache, jobs, web) running in
// a test server on top of a real restic repository.
type env struct {
	t       *testing.T
	srv     *httptest.Server
	client  *http.Client
	store   *store.Store
	cache   *cache.Refresher
	tasks   *tasks.Manager
	runner  *jobs.Runner
	repoID  int64
	repoDir string
}

const (
	repoPassword  = "repo-pw"
	loginPassword = "login-password"
)

func newEnv(t *testing.T) *env {
	t.Helper()
	bin := testutil.Binary(t)
	dir := t.TempDir()
	repoDir := filepath.Join(dir, "repo")
	src := filepath.Join(dir, "src")
	must(t, os.MkdirAll(filepath.Join(src, "photos", "2026"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "readme.txt"), []byte("hello world"), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "photos", "2026", "a.jpg"), []byte("not really a jpeg"), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "photos", "b.txt"), []byte("bbb"), 0o644))
	testutil.Restic(t, bin, repoDir, repoPassword, "init", "--quiet")
	for i, ts := range []string{"2026-01-01 10:00:00", "2026-01-02 10:00:00", "2026-01-03 10:00:00"} {
		must(t, os.WriteFile(filepath.Join(src, "counter.txt"), []byte(fmt.Sprint(i)), 0o644))
		testutil.Restic(t, bin, repoDir, repoPassword, "backup", src, "--host", "h1", "--tag", "t", "--time", ts, "--quiet")
	}

	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	must(t, err)
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), box)
	must(t, err)
	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(testLog{t}, nil))
	rc := restic.New(bin, filepath.Join(dir, "cache"))
	tm := tasks.NewManager()
	refresher := cache.New(st, rc, tm, log)
	runner := jobs.New(st, rc, refresher, tm, log)
	authSvc := auth.New(st)
	must(t, authSvc.SetPassword(context.Background(), loginPassword))

	handler, err := New(Deps{Store: st, Explorer: rc, Cache: refresher, Jobs: runner, Tasks: tm, Auth: authSvc, Log: log})
	must(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); runner.Wait() })
	runner.Start(ctx)
	refreshDone := make(chan struct{})
	go func() { refresher.Run(ctx); close(refreshDone) }()
	t.Cleanup(func() { cancel(); <-refreshDone })

	jar, _ := cookiejar.New(nil)
	e := &env{t: t, srv: srv, store: st, cache: refresher, tasks: tm, runner: runner, repoDir: repoDir,
		client: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	e.repoID, err = st.CreateRepo(ctx, store.NewRepo{Name: "photos", Path: repoDir, Password: repoPassword}, time.Now())
	must(t, err)
	must(t, refresher.Refresh(ctx, e.repoID))
	return e
}

// testLog sends the application log to the test output.
type testLog struct{ t *testing.T }

func (l testLog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (e *env) do(method, path string, form url.Values, hdr map[string]string) (*http.Response, string) {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, e.srv.URL+path, body)
	must(e.t, err)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	must(e.t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	must(e.t, err)
	return resp, string(b)
}

func (e *env) get(path string) (*http.Response, string) { return e.do("GET", path, nil, nil) }
func (e *env) hx(method, path string, form url.Values) (*http.Response, string) {
	return e.do(method, path, form, map[string]string{"HX-Request": "true"})
}

func (e *env) login() {
	e.t.Helper()
	resp, _ := e.do("POST", "/login", url.Values{"password": {loginPassword}}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("login status = %d", resp.StatusCode)
	}
}

func wantContains(t *testing.T, what, body string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(body, s) {
			t.Errorf("%s: missing %q in:\n%s", what, s, abbreviate(body))
		}
	}
}

func abbreviate(s string) string {
	if len(s) > 3000 {
		return s[:3000] + "…"
	}
	return s
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t)

	resp, _ := e.get("/")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Errorf("anonymous / = %d -> %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.hx("GET", "/", nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("HX-Redirect") != "/login" {
		t.Errorf("anonymous htmx = %d, HX-Redirect %q", resp.StatusCode, resp.Header.Get("HX-Redirect"))
	}
	_, body := e.get("/login")
	wantContains(t, "login page", body, "Sign in", `name="password"`)

	resp, body = e.do("POST", "/login", url.Values{"password": {"nope"}}, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password status = %d", resp.StatusCode)
	}
	wantContains(t, "wrong password", body, "Wrong password")

	resp, _ = e.do("POST", "/login", url.Values{"password": {loginPassword}}, map[string]string{"Origin": "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin login = %d", resp.StatusCode)
	}

	e.login()
	resp, body = e.get("/")
	if resp.StatusCode != 200 {
		t.Fatalf("/ after login = %d", resp.StatusCode)
	}
	wantContains(t, "overview", body, "photos", "Sign out")

	resp, _ = e.do("POST", "/logout", url.Values{}, nil)
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("logout = %d", resp.StatusCode)
	}
	resp, _ = e.get("/")
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("after logout / = %d", resp.StatusCode)
	}
}

func TestStateChangingRequestsRejectCrossSite(t *testing.T) {
	e := newEnv(t)
	e.login()
	resp, _ := e.do("POST", fmt.Sprintf("/repos/%d/sync", e.repoID), url.Values{}, map[string]string{"Origin": "http://evil.example"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin POST = %d", resp.StatusCode)
	}
	resp, _ = e.do("POST", fmt.Sprintf("/repos/%d/sync", e.repoID), url.Values{}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site POST = %d", resp.StatusCode)
	}
}

func TestOverviewAndRepoPages(t *testing.T) {
	e := newEnv(t)
	e.login()

	_, body := e.get("/")
	wantContains(t, "overview", body, "1 repositories, ", "photos", "data-spark", "3</b>")
	_, frag := e.do("GET", "/?sort=name", nil, map[string]string{"HX-Request": "true", "HX-Target": "repo-cards"})
	if strings.Contains(frag, "<html") || !strings.Contains(frag, `id="repo-cards"`) {
		t.Errorf("sort fragment is not a bare fragment:\n%s", abbreviate(frag))
	}

	repo := fmt.Sprintf("/repos/%d", e.repoID)
	resp, body := e.get(repo)
	if resp.StatusCode != 200 {
		t.Fatalf("repo page = %d\n%s", resp.StatusCode, body)
	}
	wantContains(t, "repo page", body, "photos", "Snapshots", "Cleanup", "Not set up", "data-history", "Sync now", "Synced ", "Syncs daily at 03:00",
		"No cleanup job yet", "snap-results")

	_, frag = e.hx("GET", repo+"/chart?range=all", nil)
	wantContains(t, "chart", frag, `id="chart-panel"`, "data-history")

	_, frag = e.hx("GET", repo+"/snapshots?per=15&q=nomatch", nil)
	wantContains(t, "filtered list", frag, "No snapshots match", "Showing 0 to 0 of 0")
	_, frag = e.hx("GET", repo+"/snapshots?host=h1&tag=t", nil)
	wantContains(t, "list", frag, "Showing 1 to 3 of 3 snapshots", "Browse", "Download as .zip")

	if resp, _ := e.get("/repos/9999"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown repo = %d", resp.StatusCode)
	}
}

func TestCleanupTab(t *testing.T) {
	e := newEnv(t)
	e.login()
	repo := fmt.Sprintf("/repos/%d", e.repoID)

	resp, form := e.hx("GET", repo+"/cleanup/edit", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("edit = %d\n%s", resp.StatusCode, form)
	}
	wantContains(t, "edit form", form, "Set up cleanup job", `name="cron"`, `value="30 3 * * *"`, "Every day at 03:30", "Next runs:", "Create cleanup job")
	for _, gone := range []string{"Integrity", "name=\"sched\"", "row-dow", "Hourly</span>"} {
		if strings.Contains(form, gone) {
			t.Errorf("cleanup form still contains %q", gone)
		}
	}

	// preview with a policy keeping the newest snapshot only
	_, prev := e.hx("POST", repo+"/cleanup/preview", url.Values{"cron": {"0 4 * * 0"}, "last": {"1"}, "prune": {"on"}})
	wantContains(t, "preview", prev, `Keeps <span class="keep">1</span> of 3`, "Forgets <span class=\"gone\">2</span>", "Every Sunday at 04:00", `id="save"`)
	_, prev = e.hx("POST", repo+"/cleanup/preview", url.Values{"cron": {"0 4 * * 0"}})
	wantContains(t, "no rule", prev, "Add a keep rule", "disabled")

	// invalid save
	resp, bad := e.hx("POST", repo+"/cleanup", url.Values{"cron": {"nonsense"}, "last": {"1"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid save = %d", resp.StatusCode)
	}
	wantContains(t, "invalid save", bad, "not a valid schedule")
	resp, _ = e.hx("POST", repo+"/cleanup", url.Values{"cron": {"0 4 * * *"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("save without rules = %d", resp.StatusCode)
	}

	// valid save
	resp, view := e.hx("POST", repo+"/cleanup", url.Values{"cron": {"0 4 * * 0"}, "last": {"2"}, "prune": {"on"}, "enabled": {"on"}})
	if resp.StatusCode != 200 {
		t.Fatalf("save = %d\n%s", resp.StatusCode, view)
	}
	wantContains(t, "view", view, "Cleanup job", "Every Sunday at 04:00", "2 latest", "Prune the repository", "Keeps 2 of 3")
	if !strings.Contains(resp.Header.Get("HX-Trigger"), "Cleanup job created") {
		t.Errorf("toast header = %q", resp.Header.Get("HX-Trigger"))
	}

	// run it: forget keeps 2, forgets 1; the cache is refreshed afterwards
	resp, runs := e.hx("POST", repo+"/cleanup/run", url.Values{})
	if resp.StatusCode != 200 {
		t.Fatalf("run = %d\n%s", resp.StatusCode, runs)
	}
	wantContains(t, "run fragment", runs, `id="cleanup-runs"`)
	deadline := time.Now().Add(90 * time.Second)
	var history string
	for time.Now().Before(deadline) {
		_, history = e.hx("GET", repo+"/cleanup/runs", nil)
		if !strings.Contains(history, "every 2s") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	wantContains(t, "history", history, "Forgot 1 snapshot", "restic forget --keep-last 2", "Done")

	// the cache refresh that follows a run brings the snapshot count down
	waitUntil(t, 60*time.Second, func() bool {
		snaps, _ := e.store.Snapshots(context.Background(), e.repoID)
		return len(snaps) == 2
	})
	_, jobsPage := e.get("/jobs")
	wantContains(t, "jobs page", jobsPage, "photos", "Every Sunday at 04:00", "Forget and prune", "Forgot 1 snapshot")

	// a second run while idle works, and a run for an unknown job is refused
	resp, _ = e.hx("POST", fmt.Sprintf("/repos/%d/cleanup/run", e.repoID+100), url.Values{})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("run for unknown repo = %d", resp.StatusCode)
	}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// treePath is where restic keeps a backed-up folder inside the snapshot's file
// tree: the whole absolute path ("C:\Users\me" becomes "/C/Users/me").
func treePath(native string) string {
	p := strings.ReplaceAll(native, "\\", "/")
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p[:1] + p[2:]
	}
	return path.Clean("/" + p)
}

// topFolder is the first folder of a tree path, the only entry shown at the snapshot root.
func topFolder(p string) string { return strings.Split(strings.Trim(p, "/"), "/")[0] }

func TestExplorerAndDownloads(t *testing.T) {
	e := newEnv(t)
	e.login()
	snaps, err := e.store.Snapshots(context.Background(), e.repoID)
	must(t, err)
	snap := snaps[len(snaps)-1]
	base := fmt.Sprintf("/repos/%d/snapshots/%s", e.repoID, snap.ShortID)
	root := treePath(snap.Paths[0])

	resp, page := e.get(base)
	if resp.StatusCode != 200 {
		t.Fatalf("explorer = %d\n%s", resp.StatusCode, page)
	}
	// The explorer starts at the root of the snapshot's tree, as restic ls reports it.
	wantContains(t, "explorer", page, "Snapshot "+snap.ShortID, "Download snapshot", `id="listing-pane"`, `data-path="/"`, `data-name="`+topFolder(root)+`"`)

	_, rootListing := e.hx("GET", base+"/listing?path="+url.QueryEscape(root), nil)
	wantContains(t, "backup folder", rootListing, `data-name="readme.txt"`, `data-name="counter.txt"`, `data-name="photos"`)
	_, listing := e.hx("GET", base+"/listing?path="+url.QueryEscape(root+"/photos"), nil)
	wantContains(t, "listing", listing, "2026", "b.txt", `data-name="b.txt"`)
	_, tree := e.hx("GET", base+"/tree?path="+url.QueryEscape(root+"/photos"), nil)
	wantContains(t, "tree", tree, "2026", `aria-expanded="true"`)
	_, details := e.hx("GET", base+"/details?path="+url.QueryEscape(root+"/photos/2026/a.jpg"), nil)
	wantContains(t, "details", details, "a.jpg", "17 B", "/preview?path=")

	// single file: raw bytes
	resp, content := e.get(downloadURL(base, root+"/readme.txt", "zip"))
	if resp.StatusCode != 200 || content != "hello world" || !strings.Contains(resp.Header.Get("Content-Disposition"), "readme.txt") {
		t.Errorf("file download = %d %q %q", resp.StatusCode, content, resp.Header.Get("Content-Disposition"))
	}

	// folder as zip, keeping its own name
	resp, zipBytes := e.get(downloadURL(base, root+"/photos", "zip"))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("zip download = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(zipBytes)), int64(len(zipBytes)))
	must(t, err)
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "photos/2026/,photos/2026/a.jpg,photos/b.txt" {
		t.Errorf("zip entries = %v", names)
	}
	for _, f := range zr.File {
		if f.Name == "photos/b.txt" {
			rc, _ := f.Open()
			if b, _ := io.ReadAll(rc); string(b) != "bbb" {
				t.Errorf("b.txt = %q", b)
			}
		}
	}

	// selection of a file and a folder as tar.gz
	resp, gzBytes := e.do("POST", base+"/download", url.Values{"path": {root + "/readme.txt", root + "/photos"}, "format": {"tar.gz"}}, nil)
	if resp.StatusCode != 200 || !strings.HasSuffix(resp.Header.Get("Content-Disposition"), `selection.tar.gz`) {
		t.Fatalf("selection = %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	gz, err := gzip.NewReader(strings.NewReader(gzBytes))
	must(t, err)
	tr := tar.NewReader(gz)
	var tarNames []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		tarNames = append(tarNames, h.Name)
	}
	sort.Strings(tarNames)
	if got := strings.Join(tarNames, ","); got != "photos/2026/,photos/2026/a.jpg,photos/b.txt,readme.txt" {
		t.Errorf("tar entries = %v", got)
	}

	// whole snapshot
	resp, whole := e.get(base + "/download?path=%2F&format=zip")
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "photos-"+snap.ShortID+".zip") {
		t.Errorf("whole snapshot = %d %s", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	if _, err := zip.NewReader(bytes.NewReader([]byte(whole)), int64(len(whole))); err != nil {
		t.Errorf("whole snapshot zip: %v", err)
	}

	// previews: only images, and never a directory
	resp, _ = e.get(base + "/preview?path=" + url.QueryEscape(root+"/readme.txt"))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("text preview = %d", resp.StatusCode)
	}
	resp, img := e.get(base + "/preview?path=" + url.QueryEscape(root+"/photos/2026/a.jpg"))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" || img != "not really a jpeg" {
		t.Errorf("image preview = %d %s %q", resp.StatusCode, resp.Header.Get("Content-Type"), img)
	}

	// unknown snapshot
	if resp, _ := e.get(fmt.Sprintf("/repos/%d/snapshots/deadbeef", e.repoID)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown snapshot = %d", resp.StatusCode)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	e.login()

	_, page := e.get("/settings")
	wantContains(t, "settings", page, "photos", "Sync every day at", `value="03:00"`, "Add repository", "Last successful sync", "Last sync", "Succeeded")

	_, form := e.hx("GET", "/settings/repos/new", nil)
	wantContains(t, "new form", form, "Add repository", `name="password"`)
	_, form = e.hx("GET", fmt.Sprintf("/settings/repos/%d/edit", e.repoID), nil)
	wantContains(t, "edit form", form, "Edit photos", "Leave empty to keep the current password", "Remove")

	// a path without a repository is refused unless forced
	bogus := filepath.Join(t.TempDir(), "nothing-here")
	resp, body := e.hx("POST", "/settings/repos", url.Values{"name": {"usb"}, "path": {bogus}, "password": {"x"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("bogus repo = %d\n%s", resp.StatusCode, body)
	}
	wantContains(t, "bogus", body, "No restic repository found", "Save anyway")
	resp, _ = e.hx("POST", "/settings/repos", url.Values{"name": {"usb"}, "path": {bogus}, "password": {"x"}, "force": {"1"}})
	if resp.Header.Get("HX-Redirect") != "/settings" {
		t.Errorf("forced save: HX-Redirect = %q (status %d)", resp.Header.Get("HX-Redirect"), resp.StatusCode)
	}
	waitUntil(t, 30*time.Second, func() bool {
		repos, _ := e.store.ListRepos(context.Background())
		return len(repos) == 2
	})

	// duplicate names are refused, wrong passwords are explained
	resp, body = e.hx("POST", "/settings/repos", url.Values{"name": {"photos"}, "path": {e.repoDir}, "password": {repoPassword}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("duplicate = %d", resp.StatusCode)
	}
	wantContains(t, "duplicate", body, "already exists")
	_, body = e.hx("POST", "/settings/repos", url.Values{"name": {"other"}, "path": {e.repoDir}, "password": {"wrong"}})
	wantContains(t, "wrong password", body, "password is wrong")

	// test button
	resp, _ = e.hx("POST", fmt.Sprintf("/settings/repos/%d/test", e.repoID), url.Values{})
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("HX-Trigger"), "Repository opens") {
		t.Errorf("test = %d %q", resp.StatusCode, resp.Header.Get("HX-Trigger"))
	}

	// cache time
	resp, _ = e.hx("POST", "/settings/cache", url.Values{"refresh_time": {"25:61"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("invalid time = %d", resp.StatusCode)
	}
	resp, _ = e.hx("POST", "/settings/cache", url.Values{"refresh_time": {"04:45"}})
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("valid time = %d", resp.StatusCode)
	}
	if v, _ := e.store.Setting(context.Background(), store.SettingCacheRefreshTime, ""); v != "04:45" {
		t.Errorf("stored time = %q", v)
	}
	_, page = e.get("/settings")
	wantContains(t, "settings after save", page, `value="04:45"`)

	// the unreachable repo shows up as such
	_, over := e.get("/")
	wantContains(t, "overview", over, "usb", "Unreachable", "No restic repository found")

	// removing a repository
	repos, _ := e.store.ListRepos(context.Background())
	for _, r := range repos {
		if r.Name == "usb" {
			resp, _ = e.hx("POST", fmt.Sprintf("/settings/repos/%d/delete", r.ID), url.Values{})
			if resp.Header.Get("HX-Redirect") != "/settings" {
				t.Errorf("delete = %d", resp.StatusCode)
			}
		}
	}
	if repos, _ := e.store.ListRepos(context.Background()); len(repos) != 1 {
		t.Errorf("repos after delete = %d", len(repos))
	}
}

func TestUnreachableRepoServesCachedData(t *testing.T) {
	e := newEnv(t)
	e.login()
	must(t, os.Rename(e.repoDir, e.repoDir+".gone"))
	// refresh fails, but the cached snapshots stay
	err := e.cache.Refresh(context.Background(), e.repoID)
	if err == nil {
		t.Fatal("refresh of a missing repository should fail")
	}
	repo := fmt.Sprintf("/repos/%d", e.repoID)
	_, body := e.get(repo)
	wantContains(t, "repo page", body, "This repository cannot be reached", "Unreachable", "Unavailable", "Showing 1 to 3 of 3 snapshots")
	_, over := e.get("/")
	wantContains(t, "overview", over, "Unreachable", "data-spark")

	snaps, _ := e.store.Snapshots(context.Background(), e.repoID)
	_, page := e.get(fmt.Sprintf("%s/snapshots/%s", repo, snaps[0].ShortID))
	wantContains(t, "explorer", page, "This repository cannot be reached")
}

var _ = domain.StateOK
