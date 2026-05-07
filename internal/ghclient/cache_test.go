package ghclient

import "testing"

// LRU eviction: with cap=2, inserting a third entry must evict the
// least-recently-used. A Get bumps recency.
func TestETagCache_LRUEvictsLeastRecent(t *testing.T) {
	c := newETagCache(2)
	c.Set("a", etagEntry{etag: `"1"`, body: []byte("A")})
	c.Set("b", etagEntry{etag: `"2"`, body: []byte("B")})
	// Touch "a" so "b" becomes the LRU.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a missing")
	}
	c.Set("c", etagEntry{etag: `"3"`, body: []byte("C")})
	if _, ok := c.Get("b"); ok {
		t.Fatal("expected b to be evicted")
	}
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a should still be present after touch")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatal("c should be present (most recent)")
	}
}

// Set on an existing key updates in place — no eviction, no duplicate.
func TestETagCache_SetUpdatesInPlace(t *testing.T) {
	c := newETagCache(2)
	c.Set("k", etagEntry{etag: `"v1"`, body: []byte("first")})
	c.Set("k", etagEntry{etag: `"v2"`, body: []byte("second")})
	got, ok := c.Get("k")
	if !ok {
		t.Fatal("k missing")
	}
	if got.etag != `"v2"` || string(got.body) != "second" {
		t.Fatalf("update lost; got etag=%q body=%q", got.etag, string(got.body))
	}
}

// InvalidatePrefix wipes every key sharing the prefix and leaves the
// rest intact.
func TestETagCache_InvalidatePrefix(t *testing.T) {
	c := newETagCache(0) // default cap
	c.Set("/repos/x/y", etagEntry{etag: `"a"`, body: []byte("repo")})
	c.Set("/repos/x/y/topics", etagEntry{etag: `"b"`, body: []byte("topics")})
	c.Set("/repos/other/z", etagEntry{etag: `"c"`, body: []byte("z")})

	c.InvalidatePrefix("/repos/x/y")

	if _, ok := c.Get("/repos/x/y"); ok {
		t.Fatal("/repos/x/y should be invalidated")
	}
	if _, ok := c.Get("/repos/x/y/topics"); ok {
		t.Fatal("/repos/x/y/topics should be invalidated (prefix match)")
	}
	if _, ok := c.Get("/repos/other/z"); !ok {
		t.Fatal("/repos/other/z must survive")
	}
}

// Default capacity: passing 0 falls back to 10 000 and the cache
// behaves as expected on simple insert/get.
func TestETagCache_DefaultCapacity(t *testing.T) {
	c := newETagCache(0)
	c.Set("k", etagEntry{etag: `"v"`, body: []byte("x")})
	if _, ok := c.Get("k"); !ok {
		t.Fatal("k missing")
	}
	if c.cap != 10000 {
		t.Fatalf("default cap = %d, want 10000", c.cap)
	}
}
