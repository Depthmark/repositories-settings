// Package conc provides bounded fan-out helpers used by appliers and the
// cron worker. Mirrors the TS mapWithConcurrency utility but uses Go
// channels and preserves input-order results.
package conc

import "context"

// DefaultApplierConcurrency caps per-applier mutation parallelism so a
// 100-item diff doesn't fire 100 simultaneous GitHub requests.
const DefaultApplierConcurrency = 5

// Map runs fn over items with at most n workers and returns results in the
// original index order. fn errors are returned in the same slot; callers
// that want all-or-nothing semantics should check the returned error
// slice.
//
// If ctx is cancelled, queued items are skipped and their slot will hold
// (zero, ctx.Err()).
func Map[T any, R any](ctx context.Context, items []T, n int, fn func(context.Context, T) (R, error)) ([]R, []error) {
	if n < 1 {
		n = 1
	}
	results := make([]R, len(items))
	errs := make([]error, len(items))
	if len(items) == 0 {
		return results, errs
	}

	type job struct {
		idx  int
		item T
	}
	jobs := make(chan job)
	done := make(chan struct{})

	workers := n
	if workers > len(items) {
		workers = len(items)
	}

	for w := 0; w < workers; w++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := range jobs {
				if ctx.Err() != nil {
					errs[j.idx] = ctx.Err()
					continue
				}
				r, err := fn(ctx, j.item)
				results[j.idx] = r
				errs[j.idx] = err
			}
		}()
	}

feeder:
	for i, it := range items {
		select {
		case <-ctx.Done():
			// Drain remaining slots with the cancellation error.
			for k := i; k < len(items); k++ {
				errs[k] = ctx.Err()
			}
			break feeder
		case jobs <- job{idx: i, item: it}:
		}
	}
	close(jobs)

	for w := 0; w < workers; w++ {
		<-done
	}
	return results, errs
}
