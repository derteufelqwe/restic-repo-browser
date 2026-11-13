package web

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The folder picker lets the signed-in user choose a path on the server's own
// disks (for the repository path) instead of typing it. It lists folders only,
// never file contents.

const (
	maxFolders = 1000
	drivesPath = ":drives" // pseudo path that lists the drive letters on Windows
)

type fsEntry struct {
	Name string
	Path string
	Repo bool // looks like a restic repository
}

type fsPage struct {
	Path    string // the folder shown ("" for the drive list)
	Parent  string // "" when there is no parent
	Entries []fsEntry
	IsRepo  bool // the folder itself looks like a restic repository
	Drives  bool // showing the list of drives
	Err     string
	More    int // folders not shown
}

func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, r, "fs_picker", browseFolder(r.URL.Query().Get("path")))
}

// browseFolder resolves what the user typed to an existing folder (walking up
// from a missing or file path) and lists its sub-folders.
func browseFolder(requested string) fsPage {
	requested = strings.TrimSpace(requested)
	if requested == drivesPath && runtime.GOOS == "windows" {
		return drivePage()
	}
	dir := startFolder(requested)
	page := fsPage{Path: dir, IsRepo: looksLikeRepo(dir)}
	if parent := filepath.Dir(dir); parent != dir {
		page.Parent = parent
	} else if runtime.GOOS == "windows" {
		page.Parent = drivesPath
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		page.Err = "This folder cannot be read: " + err.Error()
		return page
	}
	for _, e := range entries {
		if !e.IsDir() && !isDirLink(filepath.Join(dir, e.Name())) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		page.Entries = append(page.Entries, fsEntry{Name: e.Name(), Path: p, Repo: looksLikeRepo(p)})
	}
	sort.Slice(page.Entries, func(i, j int) bool {
		return strings.ToLower(page.Entries[i].Name) < strings.ToLower(page.Entries[j].Name)
	})
	if len(page.Entries) > maxFolders {
		page.More = len(page.Entries) - maxFolders
		page.Entries = page.Entries[:maxFolders]
	}
	return page
}

// startFolder returns an existing folder for the typed text: the folder itself,
// else the nearest existing parent, else the home folder.
func startFolder(requested string) string {
	if requested != "" {
		p := filepath.Clean(requested)
		for {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				return p
			}
			parent := filepath.Dir(p)
			if parent == p {
				break
			}
			p = parent
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return string(filepath.Separator)
}

func drivePage() fsPage {
	page := fsPage{Drives: true}
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if _, err := os.Stat(root); err == nil {
			page.Entries = append(page.Entries, fsEntry{Name: root, Path: root})
		}
	}
	return page
}

// isDirLink reports whether p is a symlink that points at a folder.
func isDirLink(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// looksLikeRepo recognises a restic repository by its config file and keys folder.
func looksLikeRepo(dir string) bool {
	cfg, err := os.Stat(filepath.Join(dir, "config"))
	if err != nil || cfg.IsDir() {
		return false
	}
	keys, err := os.Stat(filepath.Join(dir, "keys"))
	return err == nil && keys.IsDir()
}
