package web

import (
	"context"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"restic-repo-browser/internal/testutil"
)

// hostSnapshot returns the id of the snapshot taken on host.
func hostSnapshot(t *testing.T, e *env, host string) string {
	t.Helper()
	snaps, err := e.store.Snapshots(context.Background(), e.repoID)
	must(t, err)
	for _, s := range snaps {
		if s.Host == host {
			return s.ID
		}
	}
	t.Fatalf("no snapshot from host %q", host)
	return ""
}

// Folder and file names with spaces must be listed, expandable and downloadable.
func TestExplorerNamesWithSpaces(t *testing.T) {
	e := newEnv(t)
	e.login()
	src := filepath.Join(t.TempDir(), "my source")
	must(t, os.MkdirAll(filepath.Join(src, "my docs", "sub folder"), 0o755))
	must(t, os.WriteFile(filepath.Join(src, "my docs", "a b.txt"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(src, "my docs", "sub folder", "c d.txt"), []byte("y"), 0o644))
	testutil.Restic(t, testutil.Binary(t), e.repoDir, repoPassword, "backup", src, "--host", "spaced", "--quiet")
	must(t, e.cache.Refresh(context.Background(), e.repoID))

	base := fmt.Sprintf("/repos/%d/snapshots/%s", e.repoID, hostSnapshot(t, e, "spaced"))
	root := treePath(src)
	q := func(endpoint, p string) string { return base + endpoint + "?path=" + url.QueryEscape(p) }

	_, page := e.get(base)
	wantContains(t, "explorer page", page, `data-path="/"`, `data-name="`+topFolder(root)+`"`)

	_, body := e.hx("GET", q("/listing", root), nil)
	wantContains(t, "source folder", body, `data-name="my docs"`)
	if strings.Contains(body, `data-name="sub folder"`) || strings.Contains(body, `data-name="a b.txt"`) {
		t.Errorf("listing also shows entries from deeper folders:\n%s", abbreviate(body))
	}
	_, body = e.hx("GET", q("/listing", root+"/my docs"), nil)
	wantContains(t, "folder listing", body, `data-name="sub folder"`, `data-name="a b.txt"`)
	_, body = e.hx("GET", q("/tree", root+"/my docs"), nil)
	wantContains(t, "tree expansion", body, `title="sub folder"`)
	_, body = e.hx("GET", q("/listing", root+"/my docs/sub folder"), nil)
	wantContains(t, "nested listing", body, `data-name="c d.txt"`)
	_, body = e.hx("GET", q("/details", root+"/my docs/a b.txt"), nil)
	wantContains(t, "details", body, "<h2>a b.txt</h2>")
}

// Names mixing spaces with characters that have a meaning in URLs.
func TestExplorerOddNames(t *testing.T) {
	e := newEnv(t)
	e.login()
	odd := []string{"two  spaces", "R&D #1", "100% done", "a+b c", " lead", "trail "}
	if runtime.GOOS == "windows" {
		odd = odd[:5] // Windows cannot create names ending in a space
	}
	src := filepath.Join(t.TempDir(), "first root")
	for _, n := range odd {
		must(t, os.MkdirAll(filepath.Join(src, n, "inner dir"), 0o755))
		must(t, os.WriteFile(filepath.Join(src, n, "f g.txt"), []byte("x"), 0o644))
	}
	testutil.Restic(t, testutil.Binary(t), e.repoDir, repoPassword, "backup", src, "--host", "odd", "--quiet")
	must(t, e.cache.Refresh(context.Background(), e.repoID))

	base := fmt.Sprintf("/repos/%d/snapshots/%s", e.repoID, hostSnapshot(t, e, "odd"))
	root := treePath(src)
	q := func(endpoint, p string) string { return base + endpoint + "?path=" + url.QueryEscape(p) }

	_, body := e.hx("GET", q("/listing", root), nil)
	for _, n := range odd {
		wantContains(t, "listing", body, `data-name="`+strings.ReplaceAll(template.HTMLEscapeString(n), "+", "&#43;")+`"`)
		_, inner := e.hx("GET", q("/listing", root+"/"+n), nil)
		wantContains(t, "inside "+n, inner, `data-name="inner dir"`, `data-name="f g.txt"`)
		_, tree := e.hx("GET", q("/tree", root+"/"+n), nil)
		wantContains(t, "tree "+n, tree, `title="inner dir"`)
	}
}
