package restic

import (
	"path"
	"strings"
)

// NodePath converts a backup source path as recorded in a snapshot
// ("C:\Users\me", "/srv/data") to the absolute path restic uses for it inside
// the snapshot's file tree ("/C/Users/me", "/srv/data").
func NodePath(native string) string {
	p := strings.ReplaceAll(native, "\\", "/")
	if len(p) >= 2 && p[1] == ':' { // Windows drive letter
		p = "/" + p[:1] + p[2:]
	}
	return path.Clean("/" + p)
}

// Within reports whether p is dir itself or lies below it. Both must be clean absolute paths.
func Within(p, dir string) bool {
	if dir == "/" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}
