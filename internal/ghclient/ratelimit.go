package ghclient

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Depthmark/repositories-settings/internal/metrics"
)

// Pool selects which GitHub rate-limit budget a request should draw from.
type Pool string

const (
	PoolREST    Pool = "rest"
	PoolGraphQL Pool = "graphql"
)

// Priority controls dispatch order within the queue. Lower number = higher
// priority. Mirrors the TS Priority enum.
type Priority int

const (
	PriorityPRCheck       Priority = 0
	PriorityMergeApply    Priority = 1
	PriorityDriftRevert   Priority = 2
	PriorityCronReconcile Priority = 3
)

const (
	minBackoff = 250 * time.Millisecond
	maxBackoff = 30 * time.Second
)

type poolState struct {
	remaining       int
	resetAt         time.Time
	secondaryPoints int
}

type request struct {
	priority Priority
	pool     Pool
	enqueued time.Time
	run      func()
	done     chan struct{}
}

// RateLimiter schedules GitHub HTTP work across two budget pools (REST,
// GraphQL) with priority-FIFO ordering, bounded concurrency, and
// exponential-backoff + jitter when both pools are exhausted.
//
// Mirrors src/github/rate-limiter.ts. Dispatch is owned by a single
// goroutine; worker slots are a buffered channel acting as a semaphore.
type RateLimiter struct {
	pools       map[Pool]*poolState
	concurrency int
	logger      *slog.Logger
	now         func() time.Time
	sleep       func(time.Duration)

	mu       sync.Mutex
	queue    []*request
	streak   int
	wake     chan struct{}
	stopCh   chan struct{}
	stopOnce sync.Once

	active chan struct{} // semaphore
}

type Options struct {
	RESTMax     int
	GraphQLMax  int
	Concurrency int
}

func NewRateLimiter(logger *slog.Logger, opts Options) *RateLimiter {
	if opts.RESTMax == 0 {
		opts.RESTMax = 5000
	}
	if opts.GraphQLMax == 0 {
		opts.GraphQLMax = 5000
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 10
	}
	rl := &RateLimiter{
		pools: map[Pool]*poolState{
			PoolREST: {
				remaining:       opts.RESTMax,
				secondaryPoints: 900,
			},
			PoolGraphQL: {
				remaining:       opts.GraphQLMax,
				secondaryPoints: 2000,
			},
		},
		concurrency: opts.Concurrency,
		logger:      logger,
		now:         time.Now,
		sleep:       time.Sleep,
		wake:        make(chan struct{}, 1),
		stopCh:      make(chan struct{}),
		active:      make(chan struct{}, opts.Concurrency),
	}
	go rl.run()
	return rl
}

// Stop releases the dispatcher goroutine. Pending requests already
// scheduled will be cancelled with context.Canceled.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stopCh) })
}

// Schedule enqueues fn to run on the given pool with the given priority.
// Blocks until fn has run (or until ctx is cancelled).
//
// The function returns whatever fn returns; on dispatch the caller is
// inside the scheduler's "active" budget — fn should call
// UpdateFromResponse after each round-trip so the budget stays current.
func Schedule[T any](ctx context.Context, rl *RateLimiter, prio Priority, pool Pool, fn func() (T, error)) (T, error) {
	var zero T
	done := make(chan struct{})
	var (
		result T
		fnErr  error
	)
	r := &request{
		priority: prio,
		pool:     pool,
		enqueued: rl.now(),
		done:     done,
		run: func() {
			result, fnErr = fn()
		},
	}

	rl.mu.Lock()
	rl.queue = append(rl.queue, r)
	sort.SliceStable(rl.queue, func(i, j int) bool {
		if rl.queue[i].priority != rl.queue[j].priority {
			return rl.queue[i].priority < rl.queue[j].priority
		}
		return rl.queue[i].enqueued.Before(rl.queue[j].enqueued)
	})
	rl.updateQueueMetricsLocked()
	rl.mu.Unlock()
	rl.kick()

	select {
	case <-done:
		return result, fnErr
	case <-ctx.Done():
		// We can't easily revoke; the request will run and its result is
		// dropped. fn should be quick enough that this is acceptable.
		return zero, ctx.Err()
	case <-rl.stopCh:
		return zero, errors.New("rate limiter stopped")
	}
}

func (rl *RateLimiter) kick() {
	select {
	case rl.wake <- struct{}{}:
	default:
	}
}

func (rl *RateLimiter) run() {
	for {
		// Acquire a worker slot first so that, when we pick the next
		// item, we don't strand a higher-priority arrival behind a
		// commit to a lower-priority pick that's blocked on the
		// semaphore.
		select {
		case <-rl.stopCh:
			return
		case rl.active <- struct{}{}:
		}

		r, waitFor, ok := rl.pickNext()
		if !ok {
			// Empty queue. Release slot and wait.
			<-rl.active
			select {
			case <-rl.wake:
			case <-rl.stopCh:
				return
			}
			continue
		}
		if waitFor > 0 {
			rl.logger.Warn("all pools exhausted, backing off",
				"wait_ms", waitFor.Milliseconds(),
				"streak", rl.streak)
			<-rl.active
			timer := time.NewTimer(waitFor)
			select {
			case <-timer.C:
			case <-rl.wake:
				timer.Stop()
			case <-rl.stopCh:
				timer.Stop()
				return
			}
			rl.probeReset()
			continue
		}
		go func(req *request) {
			defer func() {
				<-rl.active
				rl.kick()
				close(req.done)
			}()
			req.run()
		}(r)
	}
}

// pickNext returns either a runnable request (waitFor=0) or a duration
// the dispatcher should sleep before retrying. ok=false means the queue
// is empty.
func (rl *RateLimiter) pickNext() (*request, time.Duration, bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if len(rl.queue) == 0 {
		return nil, 0, false
	}
	for i, r := range rl.queue {
		if rl.poolHasBudgetLocked(r.pool) {
			rl.queue = append(rl.queue[:i], rl.queue[i+1:]...)
			rl.pools[r.pool].remaining--
			cost := 1
			if r.pool == PoolREST {
				cost = 5
			}
			rl.pools[r.pool].secondaryPoints -= cost
			rl.streak = 0
			rl.updateQueueMetricsLocked()
			return r, 0, true
		}
	}
	// All pools exhausted.
	now := rl.now()
	earliestReset := now
	for _, p := range rl.pools {
		if p.resetAt.IsZero() {
			continue
		}
		if earliestReset.Equal(now) || p.resetAt.Before(earliestReset) {
			earliestReset = p.resetAt
		}
	}
	timeToReset := earliestReset.Sub(now)
	if timeToReset < 0 {
		timeToReset = 0
	}
	streak := rl.streak
	if streak > 7 {
		streak = 7
	}
	backoff := minBackoff * time.Duration(1<<streak)
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	jitter := time.Duration(rand.Float64() * float64(backoff) * 0.25)
	wait := timeToReset
	if backoff > wait {
		wait = backoff
	}
	wait += jitter
	rl.streak++
	return nil, wait, true
}

func (rl *RateLimiter) probeReset() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	for _, p := range rl.pools {
		if !p.resetAt.IsZero() && now.After(p.resetAt) && p.remaining <= 0 {
			// Don't fabricate full budget; allow exactly one probe.
			p.remaining = 1
		}
	}
}

func (rl *RateLimiter) poolHasBudgetLocked(pool Pool) bool {
	p := rl.pools[pool]
	if p.remaining > 100 {
		return true
	}
	if !p.resetAt.IsZero() && rl.now().After(p.resetAt) {
		// Reset; refill to default.
		p.remaining = 5000
		return true
	}
	return p.remaining > 0
}

// UpdateFromResponse parses x-ratelimit-* headers and adjusts the matching
// pool's view. Call after every GitHub HTTP response.
func (rl *RateLimiter) UpdateFromResponse(pool Pool, h http.Header) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	p := rl.pools[pool]
	if v := h.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.remaining = n
			metrics.RateLimitRemaining.WithLabelValues(string(pool)).Set(float64(n))
		}
	}
	if v := h.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			p.resetAt = time.Unix(n, 0)
		}
	}
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			p.resetAt = rl.now().Add(time.Duration(n) * time.Second)
			p.remaining = 0
			rl.logger.Warn("rate limited, backing off", "pool", pool, "retry_after_s", n)
		}
	}
}

func (rl *RateLimiter) updateQueueMetricsLocked() {
	counts := map[string]float64{}
	for _, r := range rl.queue {
		key := strconv.Itoa(int(r.priority)) + "-" + string(r.pool)
		counts[key]++
	}
	for k, v := range counts {
		metrics.QueueSize.WithLabelValues(k).Set(v)
	}
}

// Snapshot exposes pool state for tests / debug endpoints.
func (rl *RateLimiter) Snapshot(pool Pool) (remaining int, resetAt time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	p := rl.pools[pool]
	return p.remaining, p.resetAt
}
