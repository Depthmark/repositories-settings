package ghclient

import (
	"container/list"
	"strings"
	"sync"
)

// etagEntry holds the response body cached against the ETag value.
//
// path is the request URL path the body came from. It is kept beside the
// body because the cache key is the full absolute URL — query string and
// all — while write invalidation has to match on path segments.
type etagEntry struct {
	etag string
	body []byte
	path string
}

// etagCache is a small LRU keyed by the absolute request URL. It's used
// by conditionalGetTransport to substitute a cached body when GitHub
// returns 304 Not Modified.
type etagCache struct {
	mu      sync.Mutex
	cap     int
	order   *list.List // front = most recent
	entries map[string]*list.Element
}

func newETagCache(capacity int) *etagCache {
	if capacity <= 0 {
		capacity = 10000
	}
	return &etagCache{
		cap:     capacity,
		order:   list.New(),
		entries: make(map[string]*list.Element),
	}
}

type cacheItem struct {
	key   string
	value etagEntry
}

func (c *etagCache) Get(key string) (etagEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*cacheItem).value, true
	}
	return etagEntry{}, false
}

func (c *etagCache) Set(key string, e etagEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		el.Value.(*cacheItem).value = e
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&cacheItem{key: key, value: e})
	c.entries[key] = el
	if c.order.Len() > c.cap {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheItem).key)
		}
	}
}

// InvalidatePath drops every cached read related to a path just written.
// Related means the two paths are equal ignoring the query string, or one
// is a segment-ancestor of the other. Both directions matter:
//
//	PATCH /repos/o/r            clears GET /repos/o/r/topics   (write above read)
//	PUT   /repos/o/r/rulesets/7 clears GET /repos/o/r/rulesets (read above write)
//	POST  /repos/o/r/rulesets   clears GET /repos/o/r/rulesets?page=2
//
// Matching on stored paths rather than on the cache key is what makes
// this work at all: keys are absolute URLs, so a raw prefix test against
// a path never matched and no write ever invalidated anything.
func (c *etagCache) InvalidatePath(path string) {
	path = canonicalPath(path)
	if path == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.entries {
		if pathsRelated(canonicalPath(el.Value.(*cacheItem).value.path), path) {
			c.order.Remove(el)
			delete(c.entries, key)
		}
	}
}

// canonicalPath strips a trailing slash so /repos/o/r/ and /repos/o/r are
// one path.
func canonicalPath(p string) string {
	if len(p) > 1 {
		return strings.TrimSuffix(p, "/")
	}
	return p
}

// pathsRelated reports whether a write to `written` should drop a read of
// `cached`. Comparison is segment-aware, so /repos/o/rules is unrelated
// to /repos/o/rulesets.
func pathsRelated(cached, written string) bool {
	if cached == "" {
		return false
	}
	return cached == written ||
		strings.HasPrefix(cached, written+"/") ||
		strings.HasPrefix(written, cached+"/")
}
