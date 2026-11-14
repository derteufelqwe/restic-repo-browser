package web

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/store"
	"restic-repo-browser/internal/tasks"
)

// ---- one task at a time, with progress ----

func TestOnlyOneTaskAtATime(t *testing.T) {
	e := newEnv(t)
	e.login()
	repo := fmt.Sprintf("/repos/%d", e.repoID)
	must(t, e.store.SaveJob(context.Background(), domain.Job{RepoID: e.repoID, Cron: "0 4 * * 0", Keep: domain.KeepPolicy{Last: 2}, Enabled: true}))

	// Something else holds the slot, as a sync or cleanup would.
	held, err := e.tasks.TryStart(tasks.KindSync, "Syncing all repositories", 0)
	must(t, err)
	held.Step("Repository 2 of 5")
	held.Progress(1, 5)
	held.Detail("nas-photos: reading the snapshot list")

	for _, req := range []struct{ name, path string }{
		{"sync all", "/sync"},
		{"sync one", repo + "/sync"},
		{"cleanup", repo + "/cleanup/run"},
	} {
		resp, _ := e.hx("POST", req.path, url.Values{})
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s while busy: status %d, want 409", req.name, resp.StatusCode)
		}
		if h := resp.Header.Get("HX-Trigger"); !strings.Contains(h, "Another task is running") || !strings.Contains(h, "Syncing all repositories") {
			t.Errorf("%s while busy: toast = %q", req.name, h)
		}
	}

	// The banner shows what runs and how far it is.
	_, banner := e.hx("GET", "/tasks/banner", nil)
	wantContains(t, "banner", banner, `data-running="true"`, "Syncing all repositories", "Repository 2 of 5", "nas-photos: reading the snapshot list",
		`aria-valuenow="20"`, "width:20%")
	_, page := e.get(repo)
	wantContains(t, "repo page while busy", page, `id="task-banner"`, `data-running="true"`, "data-needs-idle")

	held.Progress(0, 0) // unmeasurable progress is shown as an indeterminate bar
	_, banner = e.hx("GET", "/tasks/banner", nil)
	wantContains(t, "indeterminate banner", banner, "indeterminate")

	held.Finish(nil)
	_, banner = e.hx("GET", "/tasks/banner", nil)
	wantContains(t, "idle banner", banner, `data-running="false"`, fmt.Sprintf(`data-last-id="%d"`, held.Info().ID))

	// Free again: a sync of one repository runs and updates the sync state.
	before, _ := e.store.Repo(context.Background(), e.repoID)
	resp, _ := e.hx("POST", repo+"/sync", url.Values{})
	if resp.StatusCode != http.StatusNoContent || !strings.Contains(resp.Header.Get("HX-Trigger"), "task-changed") {
		t.Fatalf("sync = %d, HX-Trigger %q", resp.StatusCode, resp.Header.Get("HX-Trigger"))
	}
	waitUntil(t, 60*time.Second, func() bool {
		_, running := e.tasks.Current()
		after, _ := e.store.Repo(context.Background(), e.repoID)
		return !running && after.RefreshedAt.After(before.RefreshedAt)
	})
	if last := e.tasks.Last(); last == nil || last.Err != "" || last.Info.Title != "Syncing photos" {
		t.Errorf("last task = %+v", last)
	}
	_, page = e.get(repo)
	wantContains(t, "repo page after sync", page, "Last sync", "Succeeded", "Synced ")
}

func TestSyncAllRunsOneRepositoryAfterTheOther(t *testing.T) {
	e := newEnv(t)
	e.login()
	// A second repository that does not exist makes the sync report a failure.
	_, err := e.store.CreateRepo(context.Background(), storeRepo("gone", filepath.Join(t.TempDir(), "nothing")), time.Now())
	must(t, err)

	resp, _ := e.hx("POST", "/sync", url.Values{})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("sync all = %d", resp.StatusCode)
	}
	waitUntil(t, 60*time.Second, func() bool { _, running := e.tasks.Current(); return !running && e.tasks.Last() != nil })
	last := e.tasks.Last()
	if last.Info.Title != "Syncing all repositories" || !strings.Contains(last.Err, "1 of 2 repositories could not be read") {
		t.Errorf("last task = %+v", last)
	}
	_, settings := e.get("/settings")
	wantContains(t, "settings", settings, "Failed: No restic repository found", "Succeeded")
	_, over := e.get("/")
	wantContains(t, "overview", over, "Last sync ", "Sync all")
}

// ---- folder picker ----

func TestFolderPicker(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "my-repo")
	for _, d := range []string{filepath.Join(repo, "keys"), filepath.Join(repo, "data"), filepath.Join(root, "Plain folder"), filepath.Join(root, "alpha")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.WriteFile(filepath.Join(repo, "config"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "a-file.txt"), []byte("x"), 0o644))

	page := browseFolder(root, "")
	var names []string
	var repoFlag bool
	for _, en := range page.Entries {
		names = append(names, en.Name)
		if en.Name == "my-repo" {
			repoFlag = en.Repo
		}
	}
	if want := []string{"alpha", "my-repo", "Plain folder"}; !reflect.DeepEqual(names, want) {
		t.Errorf("entries = %v, want %v (folders only, sorted)", names, want)
	}
	if !repoFlag || page.IsRepo {
		t.Errorf("repo detection wrong: entry=%v folder=%v", repoFlag, page.IsRepo)
	}
	if page.Parent != filepath.Dir(root) {
		t.Errorf("parent = %q", page.Parent)
	}
	if inside := browseFolder(repo, ""); !inside.IsRepo {
		t.Error("the repository folder itself must be recognised")
	}
	// Typed text that does not exist yet starts at the nearest existing folder.
	if got := browseFolder(filepath.Join(root, "alpha", "not", "there"), "").Path; got != filepath.Join(root, "alpha") {
		t.Errorf("start folder = %q", got)
	}
	if got := browseFolder(filepath.Join(root, "a-file.txt"), "").Path; got != root {
		t.Errorf("a file path must show its folder, got %q", got)
	}
	if got := browseFolder("", "").Path; got == "" {
		t.Error("an empty path must fall back to a real folder")
	}

	// The configured default folder is where an empty path starts, when it exists.
	if got := browseFolder("", filepath.Join(root, "alpha")).Path; got != filepath.Join(root, "alpha") {
		t.Errorf("default folder = %q", got)
	}
	if got := browseFolder("", filepath.Join(root, "missing")).Path; got == filepath.Join(root, "missing") || got == "" {
		t.Errorf("a missing default folder must fall back, got %q", got)
	}
	// A path that exists wins over the default folder.
	if got := browseFolder(root, filepath.Join(root, "alpha")).Path; got != root {
		t.Errorf("typed path = %q", got)
	}

	// Over HTTP, from the repository dialog.
	e := newEnv(t)
	e.login()
	_, frag := e.hx("GET", "/fs?path="+url.QueryEscape(root), nil)
	wantContains(t, "picker", frag, `id="fs-picker"`, "my-repo", "restic repository", "Use this folder", "data-fs-pick=", "(up)")
	if strings.Contains(frag, "a-file.txt") {
		t.Error("files must not be listed")
	}
	_, form := e.hx("GET", "/settings/repos/new", nil)
	wantContains(t, "repo form", form, "Browse…", `hx-get="/fs"`, `id="fs-picker"`)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := anon.Get(e.srv.URL + "/fs"); err != nil || resp.StatusCode != http.StatusSeeOther {
		t.Errorf("picker without a session: %v, %v", resp, err)
	}
}

// ---- explorer: shows exactly what restic ls returns ----

func TestCrumbs(t *testing.T) {
	if got, want := crumbs("/"), []crumb{{"Snapshot root", "/"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("crumbs(/) = %v", got)
	}
	want := []crumb{{"Snapshot root", "/"}, {"a", "/a"}, {"b c", "/a/b c"}}
	if got := crumbs("/a/b c"); !reflect.DeepEqual(got, want) {
		t.Errorf("crumbs = %v", got)
	}
}

func TestExplorerShowsTheWholeTree(t *testing.T) {
	e := newEnv(t)
	e.login()
	snaps, _ := e.store.Snapshots(context.Background(), e.repoID)
	snap := snaps[len(snaps)-1]
	root := treePath(snap.Paths[0])
	base := fmt.Sprintf("/repos/%d/snapshots/%s", e.repoID, snap.ShortID)

	_, page := e.get(base)
	wantContains(t, "explorer", page, `data-path="/"`, "Snapshot root", `data-name="`+topFolder(root)+`"`, "format=zip&amp;path=%2F")
	for _, banned := range []string{"root=", "data-root"} {
		if strings.Contains(page, banned) {
			t.Errorf("the page still carries a %q parameter", banned)
		}
	}

	// Folders further down are reached with the paths restic reports.
	_, listing := e.hx("GET", base+"/listing?path="+url.QueryEscape(root), nil)
	wantContains(t, "backup folder", listing, "readme.txt", `data-path="`+root+`"`)
	_, details := e.hx("GET", base+"/details?path="+url.QueryEscape(root+"/photos/2026/a.jpg"), nil)
	wantContains(t, "details", details, `<dd class="mono">`+root+`/photos/2026/a.jpg</dd>`)

	// "Download snapshot" packs the whole tree, as restic dump of "/".
	resp, body := e.get(downloadURL(base, "/", "zip"))
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Disposition"), "photos-"+snap.ShortID+".zip") {
		t.Fatalf("whole download = %d %q", resp.StatusCode, resp.Header.Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	must(t, err)
	top := map[string]bool{}
	for _, f := range zr.File {
		top[strings.SplitN(f.Name, "/", 2)[0]] = true
	}
	if len(top) != 1 || !top[topFolder(root)] {
		t.Errorf("top-level entries of the archive = %v, want only %q", top, topFolder(root))
	}

	// The snapshot list offers the same link.
	_, list := e.hx("GET", fmt.Sprintf("/repos/%d/snapshots", e.repoID), nil)
	wantContains(t, "snapshot list", list, "format=zip&amp;path=%2F")
}

func storeRepo(name, dir string) store.NewRepo {
	return store.NewRepo{Name: name, Path: dir, Password: "x"}
}
