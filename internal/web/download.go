package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"restic-repo-browser/internal/archive"
	"restic-repo-browser/internal/backend"
)

// Downloads stream straight from the backend; nothing is staged on disk. A
// single file is sent as it is, everything else is packed on the fly by the
// archive package.

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	paths := make([]string, 0, len(r.Form["path"]))
	for _, p := range r.Form["path"] {
		paths = append(paths, backend.CleanPath(p))
	}
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	format := archive.Zip
	if r.FormValue("format") == archive.TarGz {
		format = archive.TarGz
	}
	nodes, err := s.resolveNodes(r.Context(), req, paths)
	if err != nil {
		http.Error(w, backend.UserMessage(err), http.StatusBadGateway)
		return
	}

	if len(nodes) == 1 && !nodes[0].IsDir() {
		s.sendFile(w, r, req, nodes[0])
		return
	}
	setDownloadHeaders(w, archiveName(req.Sum.Repo.Name, req.Snap.ShortID, nodes, format))
	if format == archive.Zip {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "application/gzip")
	}
	dump := func(p string, asTar bool, w io.Writer) error {
		return s.Explorer.Dump(r.Context(), req.Ref, req.Snap.ID, p, asTar, w)
	}
	if err := archive.Write(r.Context(), w, format, dump, nodes); err != nil && r.Context().Err() == nil {
		s.Log.Error("download failed", "repo", req.Sum.Repo.ID, "snapshot", req.Snap.ShortID, "err", err)
		// The response is already under way; abort so the client sees a failed
		// download instead of a silently truncated archive.
		panic(http.ErrAbortHandler)
	}
}

// errNotInSnapshot means a path is not part of a snapshot's file tree.
var errNotInSnapshot = errors.New("not found in snapshot")

// resolveNodes looks up every path, listing each distinct parent folder only once.
func (s *Server) resolveNodes(ctx context.Context, req snapshotRequest, paths []string) ([]backend.Node, error) {
	listed := map[string][]backend.Node{}
	out := make([]backend.Node, 0, len(paths))
	for _, p := range paths {
		if p == "/" {
			out = append(out, backend.Node{Type: "dir", Path: "/"})
			continue
		}
		parent := path.Dir(p)
		children, done := listed[parent]
		if !done {
			var err error
			if children, err = s.Explorer.List(ctx, req.Ref, req.Snap.ID, parent); err != nil {
				return nil, err
			}
			listed[parent] = children
		}
		found := false
		for _, n := range children {
			if n.Path == p {
				out = append(out, n)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: %s", errNotInSnapshot, p)
		}
	}
	return out, nil
}

func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, req snapshotRequest, n backend.Node) {
	setDownloadHeaders(w, n.Name)
	w.Header().Set("Content-Type", "application/octet-stream")
	if n.Size != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*n.Size, 10))
	}
	if err := s.Explorer.Dump(r.Context(), req.Ref, req.Snap.ID, n.Path, false, w); err != nil && r.Context().Err() == nil {
		s.Log.Error("file download failed", "path", n.Path, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func setDownloadHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
}

func archiveName(repo, shortID string, nodes []backend.Node, format string) string {
	ext := "." + format
	switch {
	case len(nodes) == 1 && nodes[0].Path == "/":
		return fmt.Sprintf("%s-%s%s", repo, shortID, ext)
	case len(nodes) == 1:
		return nodes[0].Name + ext
	}
	return fmt.Sprintf("%s-%s-selection%s", repo, shortID, ext)
}

// ---- image preview ----

const maxPreview = 25 << 20 // bytes

var previewTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif", ".webp": "image/webp",
}

// previewType returns the image type of a file name, or "" when it cannot be previewed.
// SVG is deliberately excluded: it can carry scripts.
func previewType(name string) string { return previewTypes[strings.ToLower(path.Ext(name))] }

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	node, err := s.Explorer.Node(r.Context(), req.Ref, req.Snap.ID, r.URL.Query().Get("path"))
	ct := previewType(node.Name)
	if err != nil || ct == "" || node.IsDir() || node.Size == nil || *node.Size > maxPreview {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.FormatInt(*node.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if err := s.Explorer.Dump(r.Context(), req.Ref, req.Snap.ID, node.Path, false, w); err != nil && r.Context().Err() == nil {
		s.Log.Error("preview failed", "path", node.Path, "err", err)
		panic(http.ErrAbortHandler)
	}
}
