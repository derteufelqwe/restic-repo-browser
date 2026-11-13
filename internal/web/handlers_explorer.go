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
// is handled: it lists folders of a snapshot live. Everything is shown relative
// to the folder the snapshot was taken from ("the root"), not to the disk root.

const maxListed = 2000 // entries shown per folder

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
	Root      string // the explorer root the links keep
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
	Root     string
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

// view is where the explorer starts and which folders it may show.
type view struct {
	snap    domain.Snapshot
	roots   []string // backed-up folders as paths inside the snapshot
	Root    string   // base of the display; "/" shows the whole tree or, with several roots, the list of roots
	Virtual bool     // Root is the synthetic list of several backed-up folders
}

// newView picks the explorer root. requested comes from the URL and is only
// honoured when it is "/" or one of the snapshot's backed-up folders.
func newView(snap domain.Snapshot, requested string) view {
	v := view{snap: snap}
	for _, p := range snap.Paths {
		v.roots = append(v.roots, restic.NodePath(p))
	}
	v.Root = "/"
	if len(v.roots) == 1 {
		v.Root = v.roots[0]
	}
	if requested != "" {
		req := restic.CleanPath(requested)
		if req == "/" {
			v.Root = "/"
		}
		for _, r := range v.roots {
			if r == req {
				v.Root = r
			}
		}
	}
	v.Virtual = len(v.roots) > 1 && v.Root == "/"
	return v
}

// clamp keeps p inside the view; anything outside falls back to the root.
func (v view) clamp(p string) string {
	p = restic.CleanPath(p)
	if v.Virtual {
		if p == "/" {
			return p
		}
		for _, r := range v.roots {
			if restic.Within(p, r) {
				return p
			}
		}
		return v.Root
	}
	if restic.Within(p, v.Root) {
		return p
	}
	return v.Root
}

// rootLabel names the root folder in the path bar and tree.
func (v view) rootLabel() string {
	if v.Root == "/" {
		return "Snapshot root"
	}
	return path.Base(v.Root)
}

// nativeName is the original, native path of a backed-up folder.
func (v view) nativeName(root string) string {
	for i, r := range v.roots {
		if r == root {
			return v.snap.Paths[i]
		}
	}
	return root
}

// rel is p relative to the root, with a leading slash ("/" for the root itself).
func (v view) rel(p string) string {
	base := v.Root
	if v.Virtual {
		base = ""
		for _, r := range v.roots {
			if restic.Within(p, r) {
				base = r
			}
		}
	}
	if base == "" || base == "/" {
		return p
	}
	if p == base {
		return "/"
	}
	return strings.TrimPrefix(p, base)
}

func (v view) crumbs(p string) []crumb {
	var out []crumb
	base := v.Root
	if v.Virtual {
		out = append(out, crumb{Name: "All backed-up folders", Path: "/"})
		base = ""
		for _, r := range v.roots {
			if restic.Within(p, r) {
				base = r
			}
		}
		if base == "" {
			return out
		}
		out = append(out, crumb{Name: v.nativeName(base), Path: base})
	} else {
		out = append(out, crumb{Name: v.rootLabel(), Path: v.Root})
	}
	acc := base
	for _, seg := range strings.Split(strings.Trim(strings.TrimPrefix(p, base), "/"), "/") {
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
		Zip: wholeSnapshotURL(base, snap, formatZip), TarGz: wholeSnapshotURL(base, snap, formatTarGz)}
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

	q := r.URL.Query()
	v := newView(snap, q.Get("root"))
	current := v.Root
	if q.Get("path") != "" {
		current = v.clamp(q.Get("path"))
	}
	nodes, err := s.list(r, rr, snap, v, current)
	if err == nil && len(nodes) == 0 && !v.Virtual && v.Root != "/" && q.Get("path") == "" {
		// The recorded source folder does not exist inside the tree (backups made
		// with relative paths): start at the snapshot root instead.
		v = newView(snap, "/")
		current = "/"
		nodes, err = s.list(r, rr, snap, v, current)
	}
	data.Pane = newPane(base, v, current, nodes, err)
	data.Tree = buildTree(base, v, current, nodes)
	s.renderPage(w, r, "explorer", title, active, data)
}

// list reads one folder; the list of several backed-up folders needs no restic call.
func (s *Server) list(r *http.Request, rr restic.Repo, snap domain.Snapshot, v view, dir string) ([]restic.Node, error) {
	if v.Virtual && dir == "/" {
		nodes := make([]restic.Node, len(v.roots))
		for i, root := range v.roots {
			nodes[i] = restic.Node{Name: snap.Paths[i], Type: "dir", Path: root}
		}
		return nodes, nil
	}
	return s.Explorer.List(r.Context(), rr, snap.ID, dir)
}

func newPane(base string, v view, dir string, nodes []restic.Node, err error) listingPane {
	p := listingPane{Base: base, Root: v.Root, Path: dir, Crumbs: v.crumbs(dir)}
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

// buildTree creates the initial tree: the root, expanded with the sub-folders of
// the folder being shown when that is the root itself.
func buildTree(base string, v view, current string, listing []restic.Node) treeNode {
	lazyDirs := func() []treeNode {
		var out []treeNode
		for _, n := range listing {
			if n.IsDir() {
				out = append(out, treeNode{Base: base, Root: v.Root, Name: n.Name, Path: n.Path, Current: n.Path == current})
			}
		}
		sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
		return out
	}
	if v.Virtual {
		top := treeNode{Base: base, Root: v.Root, Name: "All backed-up folders", Path: "/", Open: true, Current: current == "/"}
		for _, root := range v.roots {
			n := treeNode{Base: base, Root: v.Root, Name: v.nativeName(root), Path: root, Current: current == root}
			if current == root {
				n.Open, n.Children = true, lazyDirs()
			}
			top.Children = append(top.Children, n)
		}
		return top
	}
	top := treeNode{Base: base, Root: v.Root, Name: v.rootLabel(), Path: v.Root, Current: current == v.Root}
	if current == v.Root {
		top.Open, top.Children = true, lazyDirs()
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
	v := newView(snap, r.URL.Query().Get("root"))
	dir := v.clamp(r.URL.Query().Get("path"))
	nodes, err := s.list(r, rr, snap, v, dir)
	s.renderFragment(w, r, "listing_pane", newPane(snapshotBase(sum.Repo.ID, snap.ID), v, dir, nodes, err))
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
	v := newView(snap, r.URL.Query().Get("root"))
	dir := v.clamp(r.URL.Query().Get("path"))
	nodes, err := s.list(r, rr, snap, v, dir)
	if err != nil {
		sendToast(w, toast{Title: "Cannot open the folder", Sub: restic.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	base := snapshotBase(sum.Repo.ID, snap.ID)
	name := path.Base(dir)
	if dir == v.Root {
		name = v.rootLabel()
	}
	n := treeNode{Base: base, Root: v.Root, Name: name, Path: dir, Open: true}
	for _, c := range nodes {
		if c.IsDir() {
			n.Children = append(n.Children, treeNode{Base: base, Root: v.Root, Name: c.Name, Path: c.Path})
		}
	}
	sort.Slice(n.Children, func(i, j int) bool { return strings.ToLower(n.Children[i].Name) < strings.ToLower(n.Children[j].Name) })
	s.renderFragment(w, r, "tree_node", n)
}

type detailsData struct {
	Base    string
	Node    restic.Node
	Rel     string // path relative to the explorer root
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
	v := newView(snap, r.URL.Query().Get("root"))
	node, err := s.Explorer.Node(r.Context(), rr, snap.ID, v.clamp(r.URL.Query().Get("path")))
	if err != nil {
		sendToast(w, toast{Title: "Cannot read the item", Sub: restic.UserMessage(err), Fail: true})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.renderFragment(w, r, "details", detailsData{
		Base: snapshotBase(sum.Repo.ID, snap.ID), Node: node, Snap: snap, Rel: v.rel(node.Path),
		Preview: previewType(node.Name) != "" && node.Size != nil && *node.Size <= maxPreview,
		Owner:   fmt.Sprintf("%d:%d", node.UID, node.GID),
	})
}

// downloadURL builds a download link for one path.
func downloadURL(base, p, format string) string {
	q := url.Values{"path": {p}, "format": {format}}
	return base + "/download?" + q.Encode()
}

// wholeSnapshotURL builds the link that downloads everything the snapshot holds:
// its backed-up folder(s), or the whole tree when they cannot be told apart.
func wholeSnapshotURL(base string, snap domain.Snapshot, format string) string {
	q := url.Values{"format": {format}, "whole": {"1"}}
	for _, p := range snap.Paths {
		q.Add("path", restic.NodePath(p))
	}
	return base + "/download?" + q.Encode()
}

// pathQuery encodes a path for use as a query parameter inside an attribute that
// html/template does not treat as a URL.
func pathQuery(p string) string { return url.QueryEscape(p) }
