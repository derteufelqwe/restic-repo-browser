// Package archive packs parts of a snapshot into a zip or tar.gz stream.
// A backend can only dump a raw file or a tar archive of a folder, so downloads
// in other shapes are re-packed on the fly; nothing is staged on disk.
package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"restic-repo-browser/internal/backend"
)

// The supported archive formats.
const (
	Zip   = "zip"
	TarGz = "tar.gz"
)

// DumpFunc writes one snapshot path to w: a tar stream for a folder (asTar),
// the raw bytes for a file. backend.Backend.Dump has this shape.
type DumpFunc func(p string, asTar bool, w io.Writer) error

// Write packs the given nodes into an archive of the given format written to w.
func Write(ctx context.Context, w io.Writer, format string, dump DumpFunc, nodes []backend.Node) error {
	var sink sink
	switch format {
	case TarGz:
		sink = newTarGzSink(w)
	case Zip:
		sink = newZipSink(w)
	default:
		return fmt.Errorf("archive: unknown format %q", format)
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

// packFile adds one file. A dump of a file holds only its bytes, so the entry
// header is built from the metadata of its snapshot node.
func packFile(sink sink, dump DumpFunc, n backend.Node) error {
	if n.Size == nil {
		return fmt.Errorf("archive: size of %s is unknown", n.Path)
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(dump(n.Path, false, pw)) }()
	defer pr.Close()
	h := &tar.Header{Typeflag: tar.TypeReg, Name: n.Name, Size: *n.Size, Mode: 0o644, ModTime: n.MTime}
	return sink.Add(h, n.Name, pr)
}

// packDir runs one directory dump and copies its tar entries into sink, naming
// them relative to the parent of p so that a downloaded folder keeps its own name.
func packDir(ctx context.Context, sink sink, dump DumpFunc, p string) error {
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
			return fmt.Errorf("archive: read dump of %s: %w", p, err)
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

// sink receives entries read from a tar stream.
type sink interface {
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
