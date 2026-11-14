package web

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"restic-repo-browser/internal/archive"
	"restic-repo-browser/internal/backend"
	"restic-repo-browser/internal/domain"
)

// The explorer is the only part of the UI that asks the backend while the request
// is handled. It shows exactly what the backend lists, starting at the root of
// the snapshot's file tree: one listing per folder that is opened, nothing else.

const (
	maxListed = 2000 // entries shown per folder
	rootLabel = "Snapshot root"
)

type crumb struct{ Name, Path string }

type entry struct {
	Name  string
	Path  string
	Dir   bool
	Size  *int64
	MTime time.Time
	Mode  string
}

// listingPane is the right-hand side of the explorer: path bar and folder contents.
type listingPane struct {
	Base      string // URL prefix of the snapshot, e.g. /repos/1/snapshots/abcd1234
	Path      string
	Crumbs    []crumb
	Entries   []entry
	Truncated int // entries that were left out
	Err       string
	// Loading marks the placeholder pane of a freshly opened explorer page: it
	// holds no entries yet and fetches its listing as soon as it is on screen.
	Loading bool
}

// treeNode is a folder in the left-hand tree. Folders are loaded on demand, one
// listing per expansion.
type treeNode struct {
	Base     string
	Name     string
	Path     string
	Open     bool
	Current  bool
	Children []treeNode
}

type explorerData struct {
	S      domain.Summary
	Snap   domain.Snapshot
	Base   string
	Pane   listingPane
	Tree   treeNode
	Notice string // set when browsing is impossible
	Zip    string // download links for the whole snapshot
	TarGz  string
}

// crumbs is the path bar for a folder: the snapshot root, then one entry per folder.
func crumbs(p string) []crumb {
	out := []crumb{{Name: rootLabel, Path: "/"}}
	acc := "/"
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "" {
			continue
		}
		acc = path.Join(acc, seg)
		out = append(out, crumb{Name: seg, Path: acc})
	}
	return out
}

func snapshotBase(repoID int64, snapID string) string {
	return fmt.Sprintf("/repos/%d/snapshots/%s", repoID, snapID)
}

// findSnapshot resolves a full or abbreviated (at least 4 characters) snapshot id.
func findSnapshot(sum domain.Summary, key string) (domain.Snapshot, bool) {
	if len(key) < 4 {
		return domain.Snapshot{}, false
	}
	for _, sn := range sum.Snapshots {
		if sn.ID == key || strings.HasPrefix(sn.ID, key) {
			return sn, true
		}
	}
	return domain.Snapshot{}, false
}

// snapshotRequest is what a handler that reaches into a snapshot needs.
type snapshotRequest struct {
	Sum  domain.Summary
	Snap domain.Snapshot
	Ref  backend.Ref // credentials to open the repository
	Base string      // URL prefix of the snapshot
}

// snapshotFromPath loads the repository and snapshot named in the URL and
// answers the error itself when that fails.
func (s *Server) snapshotFromPath(w http.ResponseWriter, r *http.Request) (snapshotRequest, bool) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return snapshotRequest{}, false
	}
	snap, found := findSnapshot(sum, r.PathValue("snap"))
	if !found {
		s.notFound(w, r)
		return snapshotRequest{}, false
	}
	ref, err := backend.Resolve(r.Context(), s.Store, sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return snapshotRequest{}, false
	}
	return snapshotRequest{Sum: sum, Snap: snap, Ref: ref, Base: snapshotBase(sum.Repo.ID, snap.ID)}, true
}

func (s *Server) handleExplorer(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	data := explorerData{S: req.Sum, Snap: req.Snap, Base: req.Base,
		Zip: downloadURL(req.Base, "/", archive.Zip), TarGz: downloadURL(req.Base, "/", archive.TarGz)}
	title := "Snapshot " + req.Snap.ShortID
	active := fmt.Sprintf("repo:%d", req.Sum.Repo.ID)
	if req.Sum.Repo.Unreachable() {
		data.Notice = req.Sum.Repo.RefreshError
		s.renderPage(w, r, "explorer", title, active, data)
		return
	}

	// The page opens at once with a placeholder; the folder listing, which can be
	// slow on remote repositories, arrives as a fragment (see handleListing).
	current := backend.CleanPath(r.URL.Query().Get("path"))
	data.Pane = newPane(req.Base, current, nil, nil)
	data.Pane.Loading = true
	data.Tree = buildTree(req.Base, current, nil)
	s.renderPage(w, r, "explorer", title, active, data)
}

func newPane(base, dir string, nodes []backend.Node, err error) listingPane {
	p := listingPane{Base: base, Path: dir, Crumbs: crumbs(dir)}
	if err != nil {
		p.Err = backend.UserMessage(err)
		return p
	}
	for _, n := range nodes {
		p.Entries = append(p.Entries, entry{Name: n.Name, Path: n.Path, Dir: n.IsDir(), Size: n.Size, MTime: n.MTime, Mode: n.Permissions})
	}
	sort.SliceStable(p.Entries, func(i, j int) bool {
		a, b := p.Entries[i], p.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if len(p.Entries) > maxListed {
		p.Truncated = len(p.Entries) - maxListed
		p.Entries = p.Entries[:maxListed]
	}
	return p
}

// childFolders turns the folders of a listing into tree nodes, sorted by name.
func childFolders(base string, listing []backend.Node, current string) []treeNode {
	var out []treeNode
	for _, n := range listing {
		if n.IsDir() {
			out = append(out, treeNode{Base: base, Name: n.Name, Path: n.Path, Current: n.Path == current})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// buildTree creates the initial tree: the snapshot root, expanded with its folders
// when that is the folder being shown.
func buildTree(base, current string, listing []backend.Node) treeNode {
	top := treeNode{Base: base, Name: rootLabel, Path: "/", Current: current == "/"}
	if current == "/" {
		top.Open, top.Children = true, childFolders(base, listing, current)
	}
	return top
}

func (s *Server) handleListing(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	dir := backend.CleanPath(r.URL.Query().Get("path"))
	nodes, err := s.Explorer.List(r.Context(), req.Ref, req.Snap.ID, dir)
	pane := newPane(req.Base, dir, nodes, err)
	if r.URL.Query().Get("init") != "" {
		// First listing of a freshly opened page: fill the folder tree as well.
		s.renderFragment(w, r, "listing_init", struct {
			Pane listingPane
			Tree treeNode
		}{pane, buildTree(req.Base, dir, nodes)})
		return
	}
	s.renderFragment(w, r, "listing_pane", pane)
}

// handleTree expands one tree folder.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	dir := backend.CleanPath(r.URL.Query().Get("path"))
	nodes, err := s.Explorer.List(r.Context(), req.Ref, req.Snap.ID, dir)
	if err != nil {
		sendToast(w, toast{Title: "Cannot open the folder", Sub: backend.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	name := path.Base(dir)
	if dir == "/" {
		name = rootLabel
	}
	n := treeNode{Base: req.Base, Name: name, Path: dir, Open: true, Children: childFolders(req.Base, nodes, "")}
	s.renderFragment(w, r, "tree_node", n)
}

type detailsData struct {
	Base    string
	Node    backend.Node
	Preview bool
	Owner   string
	Snap    domain.Snapshot
}

func (s *Server) handleDetails(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	node, err := s.Explorer.Node(r.Context(), req.Ref, req.Snap.ID, r.URL.Query().Get("path"))
	if err != nil {
		sendToast(w, toast{Title: "Cannot read the item", Sub: backend.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.renderFragment(w, r, "details", detailsData{
		Base: req.Base, Node: node, Snap: req.Snap,
		Preview: previewType(node.Name) != "" && node.Size != nil && *node.Size <= maxPreview,
		Owner:   fmt.Sprintf("%d:%d", node.UID, node.GID),
	})
}

// downloadURL builds a download link for one path; "/" is the whole snapshot.
func downloadURL(base, p, format string) string {
	q := url.Values{"path": {p}, "format": {format}}
	return base + "/download?" + q.Encode()
}

// pathQuery encodes a path for use as a query parameter inside an attribute that
// html/template does not treat as a URL.
func pathQuery(p string) string { return url.QueryEscape(p) }
