package web

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"restic-repo-browser/internal/restic"
)

// Downloads stream straight from `restic dump`; nothing is staged on disk.
// restic can only produce a raw file or a tar archive, so zip and tar.gz
// downloads re-pack the tar stream on the fly.

const (
	formatZip   = "zip"
	formatTarGz = "tar.gz"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	paths := make([]string, 0, len(r.Form["path"]))
	for _, p := range r.Form["path"] {
		paths = append(paths, restic.CleanPath(p))
	}
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	archiveFormat := formatZip
	if r.FormValue("format") == formatTarGz {
		archiveFormat = formatTarGz
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	whole := r.FormValue("whole") == "1"
	nodes, err := s.resolveNodes(r.Context(), rr, snap.ID, paths)
	if err != nil && whole {
		// The recorded source folders are not in the tree (backup made with relative
		// paths): the whole snapshot is then the whole tree.
		nodes, err = s.resolveNodes(r.Context(), rr, snap.ID, []string{"/"})
	}
	if err != nil {
		http.Error(w, restic.UserMessage(err), http.StatusBadGateway)
		return
	}

	// A single file is sent as it is; everything else becomes an archive.
	if len(nodes) == 1 && !nodes[0].IsDir() {
		s.sendFile(w, r, rr, snap.ID, nodes[0])
		return
	}
	name := archiveName(sum.Repo.Name, snap.ShortID, nodes, archiveFormat, whole)
	setDownloadHeaders(w, name)
	if archiveFormat == formatZip {
		w.Header().Set("Content-Type", "application/zip")
	} else {
		w.Header().Set("Content-Type", "application/gzip")
	}
	err = s.writeArchive(r.Context(), w, archiveFormat, func(p string, asTar bool, w io.Writer) error {
		return s.Explorer.Dump(r.Context(), rr, snap.ID, p, asTar, w)
	}, nodes)
	if err != nil && r.Context().Err() == nil {
		s.Log.Error("download failed", "repo", sum.Repo.ID, "snapshot", snap.ShortID, "err", err)
		// The response is already under way; abort so the client sees a failed
		// download instead of a silently truncated archive.
		panic(http.ErrAbortHandler)
	}
}

// resolveNodes looks up every path, running one `restic ls` per distinct parent folder.
func (s *Server) resolveNodes(ctx context.Context, rr restic.Repo, snapID string, paths []string) ([]restic.Node, error) {
	listed := map[string][]restic.Node{}
	out := make([]restic.Node, 0, len(paths))
	for _, p := range paths {
		if p == "/" {
			out = append(out, restic.Node{Type: "dir", Path: "/"})
			continue
		}
		parent := path.Dir(p)
		children, done := listed[parent]
		if !done {
			var err error
			if children, err = s.Explorer.List(ctx, rr, snapID, parent); err != nil {
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
			return nil, &restic.Error{Kind: restic.KindOther, Message: "not found in snapshot: " + p}
		}
	}
	return out, nil
}

func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, rr restic.Repo, snapID string, n restic.Node) {
	setDownloadHeaders(w, n.Name)
	w.Header().Set("Content-Type", "application/octet-stream")
	if n.Size != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(*n.Size, 10))
	}
	if err := s.Explorer.Dump(r.Context(), rr, snapID, n.Path, false, w); err != nil && r.Context().Err() == nil {
		s.Log.Error("file download failed", "path", n.Path, "err", err)
		panic(http.ErrAbortHandler)
	}
}

func setDownloadHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
}

func archiveName(repo, shortID string, nodes []restic.Node, archiveFormat string, whole bool) string {
	ext := "." + archiveFormat
	switch {
	case whole || len(nodes) == 1 && nodes[0].Path == "/":
		return fmt.Sprintf("%s-%s%s", repo, shortID, ext)
	case len(nodes) == 1:
		return nodes[0].Name + ext
	}
	return fmt.Sprintf("%s-%s-selection%s", repo, shortID, ext)
}

// dumpFunc writes one snapshot path to w: a tar stream for a folder (asTar),
// the raw bytes for a file.
type dumpFunc func(p string, asTar bool, w io.Writer) error

// writeArchive packs the given nodes into a zip or tar.gz written to w.
func (s *Server) writeArchive(ctx context.Context, w io.Writer, archiveFormat string, dump dumpFunc, nodes []restic.Node) error {
	var sink archiveSink
	switch archiveFormat {
	case formatTarGz:
		sink = newTarGzSink(w)
	default:
		sink = newZipSink(w)
	}
	for _, n := range nodes {
		var err error
		if n.IsDir() {
			err = packDir(ctx, sink, dump, n.Path)
		} else {
			err = packFile(sink, dump, n)
		}
		if err != nil {
			return err
		}
	}
	return sink.Close()
}

// packFile adds one file. restic dumps only the raw bytes of a file, so the
// archive entry header is built from the metadata of its snapshot node.
func packFile(sink archiveSink, dump dumpFunc, n restic.Node) error {
	if n.Size == nil {
		return fmt.Errorf("size of %s is unknown", n.Path)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(dump(n.Path, false, pw)) }()
	defer pr.Close()
	h := &tar.Header{Typeflag: tar.TypeReg, Name: n.Name, Size: *n.Size, Mode: 0o644, ModTime: n.MTime}
	return sink.Add(h, n.Name, pr)
}

// packDir runs one directory dump and copies its tar entries into sink, naming
// them relative to the parent of p so that a downloaded folder keeps its own name.
func packDir(ctx context.Context, sink archiveSink, dump dumpFunc, p string) error {
	prefix := strings.TrimPrefix(path.Dir(p), "/")
	if prefix != "" {
		prefix += "/"
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(dump(p, true, pw)) }()
	defer pr.Close() // unblocks the dump goroutine if we stop early

	tr := tar.NewReader(pr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read dump of %s: %w", p, err)
		}
		name := strings.TrimPrefix(strings.TrimPrefix(h.Name, "/"), prefix)
		if name == "" || hasDotDot(name) {
			continue
		}
		if err := sink.Add(h, name, tr); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func hasDotDot(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// archiveSink receives entries read from a tar stream.
type archiveSink interface {
	Add(h *tar.Header, name string, body io.Reader) error
	Close() error
}

type zipSink struct{ zw *zip.Writer }

func newZipSink(w io.Writer) *zipSink { return &zipSink{zw: zip.NewWriter(w)} }

func (z *zipSink) Add(h *tar.Header, name string, body io.Reader) error {
	zh, err := zip.FileInfoHeader(h.FileInfo())
	if err != nil {
		return err
	}
	zh.Name = name
	zh.Method = zip.Deflate
	switch h.Typeflag {
	case tar.TypeDir:
		zh.Name = strings.TrimSuffix(name, "/") + "/"
		_, err = z.zw.CreateHeader(zh)
		return err
	case tar.TypeSymlink:
		fw, err := z.zw.CreateHeader(zh)
		if err != nil {
			return err
		}
		_, err = io.WriteString(fw, h.Linkname)
		return err
	case tar.TypeReg:
		fw, err := z.zw.CreateHeader(zh)
		if err != nil {
			return err
		}
		_, err = io.Copy(fw, body)
		return err
	}
	return nil // devices, fifos and hard links have no zip equivalent
}

func (z *zipSink) Close() error { return z.zw.Close() }

type tarGzSink struct {
	gz *gzip.Writer
	tw *tar.Writer
}

func newTarGzSink(w io.Writer) *tarGzSink {
	gz := gzip.NewWriter(w)
	return &tarGzSink{gz: gz, tw: tar.NewWriter(gz)}
}

func (t *tarGzSink) Add(h *tar.Header, name string, body io.Reader) error {
	nh := *h
	nh.Name = name
	if err := t.tw.WriteHeader(&nh); err != nil {
		return err
	}
	if h.Typeflag == tar.TypeReg {
		_, err := io.Copy(t.tw, body)
		return err
	}
	return nil
}

func (t *tarGzSink) Close() error {
	if err := t.tw.Close(); err != nil {
		return err
	}
	return t.gz.Close()
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
	sum, snap, ok := s.snapshotFromPath(w, r)
	if !ok {
		return
	}
	rr, err := s.resticRepo(r.Context(), sum.Repo)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	node, err := s.Explorer.Node(r.Context(), rr, snap.ID, r.URL.Query().Get("path"))
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
	if err := s.Explorer.Dump(r.Context(), rr, snap.ID, node.Path, false, w); err != nil && r.Context().Err() == nil {
		s.Log.Error("preview failed", "path", node.Path, "err", err)
		panic(http.ErrAbortHandler)
	}
}
