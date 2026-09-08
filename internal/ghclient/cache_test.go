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

// InvalidatePath drops the write target, everything beneath it, and the
// collection a written item belongs to — and nothing else. Entries are
// stored the way the transport stores them: keyed by absolute URL, with
// the request path carried on the entry.
func TestETagCache_InvalidatePath(t *testing.T) {
	const host = "https://api.github.com"
	set := func(c *etagCache, path string) {
		c.Set(host+path, etagEntry{etag: `"v"`, body: []byte("b"), path: path})
	}
	seed := func() *etagCache {
		c := newETagCache(0) // default cap
		set(c, "/repos/x/y")
		set(c, "/repos/x/y/topics")
		set(c, "/repos/x/y/rulesets")
		set(c, "/repos/x/yz")
		set(c, "/repos/other/z")
		return c
	}

	t.Run("write above the read", func(t *testing.T) {
		c := seed()
		c.InvalidatePath("/repos/x/y")
		for _, gone := range []string{"/repos/x/y", "/repos/x/y/topics", "/repos/x/y/rulesets"} {
			if _, ok := c.Get(host + gone); ok {
				t.Errorf("%s should be invalidated", gone)
			}
		}
		for _, kept := range []string{"/repos/x/yz", "/repos/other/z"} {
			if _, ok := c.Get(host + kept); !ok {
				t.Errorf("%s must survive: it is not a path ancestor or descendant", kept)
			}
		}
	})

	t.Run("write below the read", func(t *testing.T) {
		c := seed()
		// Creating one ruleset changes what the collection returns.
		c.InvalidatePath("/repos/x/y/rulesets/7")
		if _, ok := c.Get(host + "/repos/x/y/rulesets"); ok {
			t.Error("the collection read must be invalidated by an item write")
		}
		if _, ok := c.Get(host + "/repos/x/y/topics"); !ok {
			t.Error("a sibling collection is unaffected by a ruleset write")
		}
	})

	t.Run("query strings share a path", func(t *testing.T) {
		c := newETagCache(0)
		c.Set(host+"/repos/x/y/rulesets?page=2",
			etagEntry{etag: `"v"`, body: []byte("b"), path: "/repos/x/y/rulesets"})
		c.InvalidatePath("/repos/x/y/rulesets")
		if _, ok := c.Get(host + "/repos/x/y/rulesets?page=2"); ok {
			t.Error("a paginated read of the same path must be invalidated")
		}
	})
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
