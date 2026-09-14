package conc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMapPreservesOrder(t *testing.T) {
	in := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	out, errs := Map(context.Background(), in, 3, func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	})
	for i, v := range out {
		if v != in[i]*2 {
			t.Fatalf("idx %d: got %d, want %d", i, v, in[i]*2)
		}
		if errs[i] != nil {
			t.Fatalf("idx %d: unexpected err %v", i, errs[i])
		}
	}
}

func TestMapBoundsConcurrency(t *testing.T) {
	const n = 4
	var inFlight, peak atomic.Int32
	in := make([]int, 20)
	_, _ = Map(context.Background(), in, n, func(_ context.Context, _ int) (struct{}, error) {
		cur := inFlight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return struct{}{}, nil
	})
	if peak.Load() > n {
		t.Fatalf("peak concurrency %d exceeded cap %d", peak.Load(), n)
	}
}

func TestMapEmptyInput(t *testing.T) {
	out, errs := Map(context.Background(), []int{}, 5, func(_ context.Context, v int) (int, error) {
		return v, nil
	})
	if len(out) != 0 || len(errs) != 0 {
		t.Fatalf("expected empty slices, got %v / %v", out, errs)
	}
}

func TestMapPropagatesErrors(t *testing.T) {
	sentinel := errors.New("boom")
	in := []int{1, 2, 3}
	_, errs := Map(context.Background(), in, 2, func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, sentinel
		}
		return v, nil
	})
	if !errors.Is(errs[1], sentinel) {
		t.Fatalf("idx 1 err = %v, want sentinel", errs[1])
	}
	if errs[0] != nil || errs[2] != nil {
		t.Fatalf("non-failed slots have errors: %v / %v", errs[0], errs[2])
	}
}

func TestMapHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := []int{1, 2, 3}
	_, errs := Map(ctx, in, 2, func(_ context.Context, v int) (int, error) {
		return v, nil
	})
	for i, e := range errs {
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("idx %d: err = %v, want context.Canceled", i, e)
		}
	}
}
