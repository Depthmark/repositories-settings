package ghclient

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardW{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

type discardW struct{}

func (discardW) Write(p []byte) (int, error) { return len(p), nil }

func TestRateLimiter_RunsScheduledFn(t *testing.T) {
	rl := NewRateLimiter(discard(), Options{Concurrency: 2})
	defer rl.Stop()

	got, err := Schedule(context.Background(), rl, PriorityPRCheck, PoolREST, func() (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Fatalf("got %d", got)
	}
}

func TestRateLimiter_PriorityOrdering(t *testing.T) {
	// Force concurrency=1 so ordering is observable.
	rl := NewRateLimiter(discard(), Options{Concurrency: 1})
	defer rl.Stop()

	var (
		mu    sync.Mutex
		order []Priority
		wg    sync.WaitGroup
	)

	// Block the worker with a slow first call so the next 3 queue up.
	released := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = Schedule(context.Background(), rl, PriorityCronReconcile, PoolREST, func() (struct{}, error) {
			<-released
			return struct{}{}, nil
		})
	}()
	// Give the dispatcher a moment to pick up the first item.
	time.Sleep(20 * time.Millisecond)

	prios := []Priority{PriorityCronReconcile, PriorityDriftRevert, PriorityMergeApply, PriorityPRCheck}
	for _, p := range prios {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Schedule(context.Background(), rl, p, PoolREST, func() (struct{}, error) {
				mu.Lock()
				order = append(order, p)
				mu.Unlock()
				return struct{}{}, nil
			})
		}()
	}
	// Let the queue settle with all items enqueued.
	time.Sleep(50 * time.Millisecond)
	close(released)
	wg.Wait()

	// Highest priority (lowest number) should run first.
	if len(order) != 4 {
		t.Fatalf("wanted 4 items, got %v", order)
	}
	if order[0] != PriorityPRCheck {
		t.Fatalf("first dispatched = %d, want %d (PR_CHECK)", order[0], PriorityPRCheck)
	}
	if order[len(order)-1] != PriorityCronReconcile {
		t.Fatalf("last dispatched = %d, want %d (CRON)", order[len(order)-1], PriorityCronReconcile)
	}
}

func TestRateLimiter_UpdateFromResponse(t *testing.T) {
	rl := NewRateLimiter(discard(), Options{})
	defer rl.Stop()
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "42")
	h.Set("X-RateLimit-Reset", "9999999999")
	rl.UpdateFromResponse(PoolREST, h)
	if rem, _ := rl.Snapshot(PoolREST); rem != 42 {
		t.Fatalf("remaining = %d, want 42", rem)
	}
}
