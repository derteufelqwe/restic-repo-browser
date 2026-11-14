package backend

import "testing"

func TestCleanPath(t *testing.T) {
	cases := map[string]string{"": "/", "/": "/", "a/b": "/a/b", "/a//b/": "/a/b", `C:\x`: "/C:/x", "/a/../b": "/b"}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}
