package ghclient

import (
	"strings"
	"sync"
)

// RepoLock serializes per-repo work so concurrent reconciles for the same
// (owner, repo) don't race against each other. Different repos run
// concurrently. Mirrors src/github/repo-lock.ts.
type RepoLock struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewRepoLock() *RepoLock {
	return &RepoLock{locks: map[string]*sync.Mutex{}}
}

// With acquires the lock for owner/repo, runs fn, releases.
func (r *RepoLock) With(owner, name string, fn func()) {
	key := strings.ToLower(owner + "/" + name)
	r.mu.Lock()
	m, ok := r.locks[key]
	if !ok {
		m = &sync.Mutex{}
		r.locks[key] = m
	}
	r.mu.Unlock()

	m.Lock()
	defer m.Unlock()
	fn()
}
