package config

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// AdminCacheTTL is how long a fetched org layer is reused. The admin
// repository changes on a human timescale while a busy org can trigger
// hundreds of reconciles a minute, so re-reading it per reconcile would
// spend the whole rate-limit budget on files that did not change.
const AdminCacheTTL = 5 * time.Minute

// AdminCache resolves and caches the org admin layer per owner.
//
// A nil *AdminCache, or one built with an empty repository reference, is
// valid and always returns a nil layer: that is a deployment with no
// admin repository, where a repository's own configuration stands alone.
type AdminCache struct {
	fetcher AdminFetcher
	// ref is the operator's ORG_ADMIN_REPO value: "owner/name" pins one
	// repository, a bare "name" resolves inside each target's own owner.
	ref    string
	logger *slog.Logger
	ttl    time.Duration

	mu      sync.Mutex
	entries map[string]*adminEntry
}

type adminEntry struct {
	// once guards the fetch so a burst of webhooks for one org issues a
	// single read of the admin repository rather than one per event.
	once    sync.Once
	layer   *AdminLayer
	err     error
	fetched time.Time
}

// NewAdminCache builds a cache for the given ORG_ADMIN_REPO reference.
// An empty reference disables the org layer entirely.
func NewAdminCache(f AdminFetcher, ref string, logger *slog.Logger) *AdminCache {
	return &AdminCache{
		fetcher: f,
		ref:     strings.TrimSpace(ref),
		logger:  logger,
		ttl:     AdminCacheTTL,
		entries: map[string]*adminEntry{},
	}
}

// For returns the org layer that governs target, fetching it at most
// once per TTL per owner.
func (c *AdminCache) For(ctx context.Context, target Repo) (*AdminLayer, error) {
	if c == nil || c.ref == "" {
		return nil, nil
	}
	if err := ValidateAdminRepoRef(c.ref); err != nil {
		return nil, err
	}
	adminRepo, ok := c.adminRepoFor(target)
	if !ok {
		return nil, nil
	}

	key := adminRepo.String()
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok || time.Since(entry.fetched) > c.ttl {
		entry = &adminEntry{fetched: time.Now()}
		c.entries[key] = entry
	}
	c.mu.Unlock()

	entry.once.Do(func() {
		entry.layer, entry.err = LoadAdmin(ctx, c.fetcher, adminRepo, "")
		if entry.err == nil {
			return
		}
		if c.logger != nil {
			c.logger.Error("loading org admin layer", "admin_repo", key, "error", entry.err)
		}
		// Drop the failed entry so the next caller retries. Caching the
		// error would let one transient read failure refuse every
		// reconcile in the org for a full TTL — and the caller fails
		// closed on this error, so that outage would be total.
		c.mu.Lock()
		if c.entries[key] == entry {
			delete(c.entries, key)
		}
		c.mu.Unlock()
	})
	return entry.layer, entry.err
}

// ValidateAdminRepoRef rejects malformed operator references before any
// repository path can be resolved. An empty value disables the org layer.
func ValidateAdminRepoRef(ref string) error {
	if ref == "" {
		return nil
	}
	parts := strings.Split(ref, "/")
	if len(parts) > 2 {
		return fmt.Errorf("ORG_ADMIN_REPO must be name or owner/name")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, " \\?%#\t\r\n") {
			return fmt.Errorf("ORG_ADMIN_REPO contains an invalid repository path segment")
		}
	}
	return nil
}

// adminRepoFor expands the configured reference against a target repo. A
// repository must never be governed by its own admin file, so an admin
// reference that resolves to the target itself is ignored — otherwise a
// repository could grant itself the policy that is supposed to bind it.
func (c *AdminCache) adminRepoFor(target Repo) (Repo, bool) {
	owner, name, hasOwner := strings.Cut(c.ref, "/")
	admin := Repo{Owner: owner, Name: name}
	if !hasOwner {
		admin = Repo{Owner: target.Owner, Name: c.ref}
	}
	if admin.Owner == "" || admin.Name == "" {
		return Repo{}, false
	}
	if strings.EqualFold(admin.Owner, target.Owner) && strings.EqualFold(admin.Name, target.Name) {
		return Repo{}, false
	}
	return admin, true
}
