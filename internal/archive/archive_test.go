package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"reflect"
	"sort"
	"testing"

	"restic-repo-browser/internal/backend"
)

// fakeDump serves a folder /data/dir (holding f.txt) as a tar stream and /data/a.txt as raw bytes.
func fakeDump(p string, asTar bool, w io.Writer) error {
	if !asTar {
		_, err := io.WriteString(w, "hello")
		return err
	}
	tw := tar.NewWriter(w)
	for _, e := range []struct {
		name, body string
		dir        bool
	}{{"/data/dir", "", true}, {"/data/dir/f.txt", "12345", false}, {"/data/dir/../../escape", "x", false}} {
		h := &tar.Header{Name: e.name, Typeflag: tar.TypeReg, Size: int64(len(e.body)), Mode: 0o644}
		if e.dir {
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		io.WriteString(tw, e.body)
	}
	return tw.Close()
}

func TestWriteRepacksFilesAndFolders(t *testing.T) {
	size := int64(5)
	nodes := []backend.Node{
		{Name: "dir", Type: "dir", Path: "/data/dir"},
		{Name: "a.txt", Type: "file", Path: "/data/a.txt", Size: &size},
	}

	var zbuf bytes.Buffer
	if err := Write(context.Background(), &zbuf, Zip, fakeDump, nodes); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zbuf.Bytes()), int64(zbuf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var zipped []string
	for _, f := range zr.File {
		zipped = append(zipped, f.Name)
	}

	var tbuf bytes.Buffer
	if err := Write(context.Background(), &tbuf, TarGz, fakeDump, nodes); err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(&tbuf)
	if err != nil {
		t.Fatal(err)
	}
	var tarred []string
	for tr := tar.NewReader(gz); ; {
		h, err := tr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		tarred = append(tarred, h.Name)
	}

	// The folder keeps its own name, the file sits at the top, and the entry
	// that tries to leave the archive is dropped.
	want := []string{"a.txt", "dir/", "dir/f.txt"}
	sort.Strings(zipped)
	if !reflect.DeepEqual(zipped, want) {
		t.Errorf("zip entries = %v, want %v", zipped, want)
	}
	sort.Strings(tarred)
	if !reflect.DeepEqual(tarred, []string{"a.txt", "dir", "dir/f.txt"}) {
		t.Errorf("tar entries = %v", tarred)
	}
}
