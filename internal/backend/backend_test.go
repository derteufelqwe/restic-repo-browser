package backend

import (
	"context"
	"errors"
	"testing"

	"restic-repo-browser/internal/domain"
)

func TestCleanPath(t *testing.T) {
	cases := map[string]string{"": "/", "/": "/", "a/b": "/a/b", "/a//b/": "/a/b", `C:\x`: "/C:/x", "/a/../b": "/b"}
	for in, want := range cases {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// countingBackend counts List calls; the other methods are not needed here.
type countingBackend struct {
	Backend
	lists int
	err   error
}

func (c *countingBackend) List(_ context.Context, _ Ref, _, dir string) ([]Node, error) {
	c.lists++
	if c.err != nil {
		return nil, c.err
	}
	return []Node{{Name: "a", Path: dir + "/a"}, {Name: "b", Path: dir + "/b"}}, nil
}

func TestListingCache(t *testing.T) {
	ctx := context.Background()
	ref := Ref{Kind: domain.BackendLocal, Location: "/r"}
	inner := &countingBackend{}
	c := NewListingCache(inner, 7) // room for two folders of two entries (3 slots each), not three

	for i := 0; i < 3; i++ {
		if _, err := c.List(ctx, ref, "snap", "/x"); err != nil {
			t.Fatal(err)
		}
	}
	if inner.lists != 1 {
		t.Errorf("repeated List asked the backend %d times, want 1", inner.lists)
	}

	// Node comes from the listing of its parent folder: no further call.
	if n, err := c.Node(ctx, ref, "snap", "/x/b"); err != nil || n.Name != "b" || inner.lists != 1 {
		t.Errorf("Node = %+v, %v after %d backend calls", n, err, inner.lists)
	}
	if _, err := c.Node(ctx, ref, "snap", "/x/missing"); err == nil {
		t.Error("a missing entry must be an error")
	}

	// Another folder fits; a third evicts the least recently used one (/x).
	c.List(ctx, ref, "snap", "/y")
	c.List(ctx, ref, "snap", "/z")
	before := inner.lists
	c.List(ctx, ref, "snap", "/x")
	if inner.lists != before+1 {
		t.Error("the least recently used folder should have been evicted")
	}

	// Errors are not remembered.
	inner.err = errors.New("boom")
	if _, err := c.List(ctx, ref, "snap", "/new"); err == nil {
		t.Fatal("expected the backend error")
	}
	inner.err = nil
	n := inner.lists
	if _, err := c.List(ctx, ref, "snap", "/new"); err != nil || inner.lists != n+1 {
		t.Errorf("a failed listing must not be cached: err = %v", err)
	}
}
