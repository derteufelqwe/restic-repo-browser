package web

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"restic-repo-browser/internal/domain"
	"restic-repo-browser/internal/restic"
)

// The explorer is the only part of the UI that asks restic while the request
// is handled. It shows exactly what `restic ls` reports, starting at the root of
// the snapshot's file tree: one `ls` per folder that is opened, nothing else.

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
}

// treeNode is a folder in the left-hand tree. Folders are loaded on demand, one
// `restic ls` per expansion.
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

// snapshotFromPath loads the repository and snapshot named in the URL.
func (s *Server) snapshotFromPath(w http.ResponseWriter, r *http.Request) (domain.Summary, domain.Snapshot, bool) {
	sum, ok := s.repoFromPath(w, r)
	if !ok {
		return sum, domain.Snapshot{}, false
	}
	snap, found := findSnapshot(sum, r.PathValue("snap"))
	if !found {
		s.notFound(w, r)
		return sum, snap, false
	}
	return sum, snap, true
}

func (s *Server) handleExplorer(w http.ResponseWriter, r *http.Request) {
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	base := snapshotBase(sum.Repo.ID, snap.ID)
	data := explorerData{S: sum, Snap: snap, Base: base,
		Zip: downloadURL(base, "/", formatZip), TarGz: downloadURL(base, "/", formatTarGz)}
	title := "Snapshot " + snap.ShortID
	active := fmt.Sprintf("repo:%d", sum.Repo.ID)
	if sum.Repo.Unreachable() {
		data.Notice = sum.Repo.RefreshError
		s.renderPage(w, r, "explorer", title, active, data)
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	current := restic.CleanPath(r.URL.Query().Get("path"))
	nodes, err := s.Explorer.List(r.Context(), rr, snap.ID, current)
	data.Pane = newPane(base, current, nodes, err)
	data.Tree = buildTree(base, current, nodes)
	s.renderPage(w, r, "explorer", title, active, data)
}

func newPane(base, dir string, nodes []restic.Node, err error) listingPane {
	p := listingPane{Base: base, Path: dir, Crumbs: crumbs(dir)}
	if err != nil {
		p.Err = restic.UserMessage(err)
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
func childFolders(base string, listing []restic.Node, current string) []treeNode {
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
func buildTree(base, current string, listing []restic.Node) treeNode {
	top := treeNode{Base: base, Name: rootLabel, Path: "/", Current: current == "/"}
	if current == "/" {
		top.Open, top.Children = true, childFolders(base, listing, current)
	}
	return top
}

func (s *Server) handleListing(w http.ResponseWriter, r *http.Request) {
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	dir := restic.CleanPath(r.URL.Query().Get("path"))
	nodes, err := s.Explorer.List(r.Context(), rr, snap.ID, dir)
	s.renderFragment(w, r, "listing_pane", newPane(snapshotBase(sum.Repo.ID, snap.ID), dir, nodes, err))
}

// handleTree expands one tree folder.
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	dir := restic.CleanPath(r.URL.Query().Get("path"))
	nodes, err := s.Explorer.List(r.Context(), rr, snap.ID, dir)
	if err != nil {
		sendToast(w, toast{Title: "Cannot open the folder", Sub: restic.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	base := snapshotBase(sum.Repo.ID, snap.ID)
	name := path.Base(dir)
	if dir == "/" {
		name = rootLabel
	}
	n := treeNode{Base: base, Name: name, Path: dir, Open: true, Children: childFolders(base, nodes, "")}
	s.renderFragment(w, r, "tree_node", n)
}

type detailsData struct {
	Base    string
	Node    restic.Node
	Preview bool
	Owner   string
	Snap    domain.Snapshot
}

func (s *Server) handleDetails(w http.ResponseWriter, r *http.Request) {
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	node, err := s.Explorer.Node(r.Context(), rr, snap.ID, restic.CleanPath(r.URL.Query().Get("path")))
	if err != nil {
		sendToast(w, toast{Title: "Cannot read the item", Sub: restic.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.renderFragment(w, r, "details", detailsData{
		Base: snapshotBase(sum.Repo.ID, snap.ID), Node: node, Snap: snap,
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
