package backend

import (
	"container/list"
	"context"
	"path"
	"sync"
)

// Lister is the part of a Backend that NodeVia needs.
type Lister interface {
	List(ctx context.Context, ref Ref, snapshotID, dir string) ([]Node, error)
}

// NodeVia implements Backend.Node on top of List: it lists the parent folder
// and picks the entry. Backends without a cheaper lookup use it.
func NodeVia(ctx context.Context, l Lister, ref Ref, snapshotID, p string) (Node, error) {
	p = CleanPath(p)
	if p == "/" {
		return Node{Type: "dir", Path: "/"}, nil
	}
	nodes, err := l.List(ctx, ref, snapshotID, path.Dir(p))
	if err != nil {
		return Node{}, err
	}
	for _, n := range nodes {
		if n.Path == p {
			return n, nil
		}
	}
	return Node{}, &Error{Kind: KindOther, Message: "path not found in snapshot: " + p}
}

// NewListingCache wraps b so that folder listings of snapshots are remembered in
// memory (and only there). A snapshot never changes, so a cached listing is
// never stale; browsing a slow repository (cloud storage) is then slow only the
// first time a folder is opened. Up to maxNodes entries are kept, the least
// recently used folders go first. Errors are not cached.
//
// List returns a shared slice: callers must not modify it.
func NewListingCache(b Backend, maxNodes int) Backend {
	return &listingCache{Backend: b, max: maxNodes, lru: list.New(), items: map[string]*list.Element{}}
}

type listingCache struct {
	Backend
	max int

	mu    sync.Mutex
	nodes int                      // entries held in total
	lru   *list.List               // of *cachedListing, most recently used first
	items map[string]*list.Element // by cache key
}

type cachedListing struct {
	key   string
	nodes []Node
}

// listingKey identifies one folder of one snapshot. The password is left out on
// purpose: it does not change what a snapshot contains.
func listingKey(ref Ref, snapshotID, dir string) string {
	return string(ref.Kind) + "\x00" + ref.Location + "\x00" + snapshotID + "\x00" + dir
}

func (c *listingCache) List(ctx context.Context, ref Ref, snapshotID, dir string) ([]Node, error) {
	dir = CleanPath(dir)
	key := listingKey(ref, snapshotID, dir)
	if nodes, ok := c.get(key); ok {
		return nodes, nil
	}
	nodes, err := c.Backend.List(ctx, ref, snapshotID, dir)
	if err != nil {
		return nil, err
	}
	c.put(key, nodes)
	return nodes, nil
}

// Node is answered from the (cached) listing of the parent folder.
func (c *listingCache) Node(ctx context.Context, ref Ref, snapshotID, p string) (Node, error) {
	return NodeVia(ctx, c, ref, snapshotID, p)
}

func (c *listingCache) get(key string) ([]Node, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*cachedListing).nodes, true
}

func (c *listingCache) put(key string, nodes []Node) {
	size := len(nodes) + 1 // an empty folder still takes an entry
	if size > c.max {
		return // too big to keep
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok { // a concurrent request got there first
		c.lru.MoveToFront(el)
		return
	}
	c.items[key] = c.lru.PushFront(&cachedListing{key: key, nodes: nodes})
	c.nodes += size
	for c.nodes > c.max {
		oldest := c.lru.Back()
		old := oldest.Value.(*cachedListing)
		c.lru.Remove(oldest)
		delete(c.items, old.key)
		c.nodes -= len(old.nodes) + 1
	}
}
