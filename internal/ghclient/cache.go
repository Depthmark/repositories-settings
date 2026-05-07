package ghclient

import (
	"container/list"
	"strings"
	"sync"
)

// etagEntry holds the response body cached against the ETag value.
type etagEntry struct {
	etag string
	body []byte
}

// etagCache is a small LRU keyed by request URL. It's used by
// conditionalGetTransport to substitute a cached body when GitHub returns
// 304 Not Modified.
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

// InvalidatePrefix removes every entry whose key starts with prefix.
// Used by writes to clear matching read caches (e.g., a PATCH on
// /repos/x/y invalidates GET cache for /repos/x/y).
func (c *etagCache) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, el := range c.entries {
		if strings.HasPrefix(key, prefix) {
			c.order.Remove(el)
			delete(c.entries, key)
		}
	}
}
