package ghclient

import (
	"net/http"
	"testing"
	"time"
)

// newDrainedLimiter returns a RateLimiter whose dispatcher goroutine is
// stopped, so tests can drive pickNext / probeReset / pool state
// directly without races against the run loop.
func newDrainedLimiter(t *testing.T) *RateLimiter {
	t.Helper()
	rl := NewRateLimiter(discard(), Options{Concurrency: 1})
	rl.Stop()
	// Give the goroutine a tick to exit on stopCh.
	time.Sleep(5 * time.Millisecond)
	return rl
}

// exhaustPools zeroes the budgets and puts the reset window in the
// future so pickNext is forced down the backoff path.
func exhaustPools(rl *RateLimiter, resetIn time.Duration) {
	now := rl.now()
	for _, p := range rl.pools {
		p.remaining = 0
		p.resetAt = now.Add(resetIn)
		p.secondaryPoints = 0
	}
}

func enqueueDummy(rl *RateLimiter) {
	rl.mu.Lock()
	rl.queue = append(rl.queue, &request{
		priority: PriorityCronReconcile,
		pool:     PoolREST,
		enqueued: rl.now(),
		done:     make(chan struct{}),
		run:      func() {},
	})
	rl.mu.Unlock()
}

// Backoff doubles per consecutive exhausted pick: 250ms · 2^streak,
// capped at 30s. Jitter adds 0–25 % on top, so the observed wait must
// fall within [base, base · 1.25] when timeToReset is shorter than the
// computed backoff (otherwise wait clamps to timeToReset+jitter).
func TestPickNext_BackoffStreakAndCap(t *testing.T) {
	rl := newDrainedLimiter(t)
	// Drain pools but keep resetAt close to "now" so timeToReset is
	// always much smaller than backoff — backoff dominates the max().
	// poolHasBudgetLocked treats elapsed resetAt as a full reset, so
	// keep the window in the future for the duration of the test.
	exhaustPools(rl, 50*time.Millisecond)

	cases := []struct {
		streakBefore int
		minWant      time.Duration
		maxWant      time.Duration
	}{
		{0, 250 * time.Millisecond, time.Duration(float64(250*time.Millisecond) * 1.25)},
		{1, 500 * time.Millisecond, time.Duration(float64(500*time.Millisecond) * 1.25)},
		{2, 1 * time.Second, time.Duration(float64(time.Second) * 1.25)},
		{6, 16 * time.Second, time.Duration(float64(16*time.Second) * 1.25)},
		// Cap: streak >= 7 collapses to 30s base + jitter.
		{8, 30 * time.Second, time.Duration(float64(30*time.Second) * 1.25)},
	}
	for _, tc := range cases {
		// Reset state between iterations.
		exhaustPools(rl, 50*time.Millisecond)
		rl.mu.Lock()
		rl.queue = nil
		rl.streak = tc.streakBefore
		rl.mu.Unlock()
		enqueueDummy(rl)

		_, wait, ok := rl.pickNext()
		if !ok {
			t.Fatalf("streak=%d: pickNext returned ok=false", tc.streakBefore)
		}
		if wait < tc.minWant || wait > tc.maxWant {
			t.Fatalf("streak=%d: wait=%v, want in [%v, %v]",
				tc.streakBefore, wait, tc.minWant, tc.maxWant)
		}
	}
}

// Jitter must be strictly below 25 % of the base; running the pick
// many times we should sample multiple distinct waits and never exceed
// the cap.
func TestPickNext_JitterRangeBounded(t *testing.T) {
	rl := newDrainedLimiter(t)

	const base = 250 * time.Millisecond
	const ceiling = time.Duration(float64(base) * 1.25)
	uniques := map[time.Duration]struct{}{}
	for i := 0; i < 32; i++ {
		exhaustPools(rl, 50*time.Millisecond) // keep resetAt in future
		rl.mu.Lock()
		rl.queue = nil
		rl.streak = 0
		rl.mu.Unlock()
		enqueueDummy(rl)
		_, wait, ok := rl.pickNext()
		if !ok {
			t.Fatal("pickNext ok=false")
		}
		if wait < base || wait > ceiling {
			t.Fatalf("wait %v outside [%v, %v]", wait, base, ceiling)
		}
		uniques[wait] = struct{}{}
	}
	if len(uniques) < 2 {
		t.Fatalf("expected jitter to vary; saw only %d distinct wait values", len(uniques))
	}
}

// probeReset is the safety valve when both pools are exhausted *and*
// the reset window has elapsed. Instead of declaring full budget it
// admits exactly one request — the probe — to confirm GitHub has lifted
// the limit before we resume normal scheduling.
func TestProbeReset_AdmitsExactlyOneAfterReset(t *testing.T) {
	rl := newDrainedLimiter(t)
	now := rl.now()
	for _, p := range rl.pools {
		p.remaining = 0
		p.resetAt = now.Add(-1 * time.Second) // already elapsed
	}
	rl.probeReset()
	for name, p := range rl.pools {
		if p.remaining != 1 {
			t.Fatalf("pool %s remaining = %d after probeReset, want 1", name, p.remaining)
		}
	}
}

// probeReset must be a no-op when reset hasn't elapsed yet — otherwise
// we'd repeatedly fire probes against a still-cold limit window.
func TestProbeReset_NoopBeforeResetWindow(t *testing.T) {
	rl := newDrainedLimiter(t)
	now := rl.now()
	for _, p := range rl.pools {
		p.remaining = 0
		p.resetAt = now.Add(1 * time.Hour)
	}
	rl.probeReset()
	for name, p := range rl.pools {
		if p.remaining != 0 {
			t.Fatalf("pool %s remaining = %d before reset, want 0", name, p.remaining)
		}
	}
}

// Retry-After flips the pool to remaining=0 with resetAt in the future,
// so subsequent picks must enter backoff regardless of any prior
// X-RateLimit-Remaining count.
func TestUpdateFromResponse_RetryAfterForcesBackoff(t *testing.T) {
	rl := newDrainedLimiter(t)
	h := http.Header{}
	h.Set("Retry-After", "120")
	rl.UpdateFromResponse(PoolREST, h)

	rem, reset := rl.Snapshot(PoolREST)
	if rem != 0 {
		t.Fatalf("remaining = %d after Retry-After, want 0", rem)
	}
	if !reset.After(rl.now()) {
		t.Fatalf("resetAt %v not in future", reset)
	}
}
