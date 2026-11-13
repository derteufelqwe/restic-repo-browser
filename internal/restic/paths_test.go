package restic

import "testing"

func TestNodePath(t *testing.T) {
	cases := map[string]string{
		`C:\Users\arne`:    "/C/Users/arne",
		`D:\`:              "/D",
		"/volume1/photos":  "/volume1/photos",
		"/volume1/photos/": "/volume1/photos",
		"relative/dir":     "/relative/dir",
	}
	for in, want := range cases {
		if got := NodePath(in); got != want {
			t.Errorf("NodePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithin(t *testing.T) {
	if !Within("/a/b", "/a") || !Within("/a", "/a") || !Within("/x", "/") {
		t.Error("expected within")
	}
	if Within("/ab", "/a") || Within("/a", "/a/b") {
		t.Error("expected not within")
	}
}
