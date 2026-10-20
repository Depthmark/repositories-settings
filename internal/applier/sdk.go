package applier

import (
	"context"
	stderrors "errors"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Helpers shared by every go-github-backed lane.

// readCtx annotates a context for a read: cron priority, so an
// interactive PR check is never queued behind a background sweep.
func readCtx(ctx context.Context, repo config.Repo, route string) context.Context {
	return ghclient.WithCall(ctx, ghclient.PriorityCronReconcile, repo.Owner, route)
}

// writeCtx annotates a context for a mutation.
func writeCtx(ctx context.Context, repo config.Repo, route string) context.Context {
	return ghclient.WithCall(ctx, ghclient.PriorityMergeApply, repo.Owner, route)
}

// listAll walks every page of a go-github list endpoint.
//
// go-github returns pagination state on the Response rather than
// iterating for you, so each caller would otherwise repeat this loop.
// One route template covers the whole walk, keeping a 40-page listing to
// a single metric series.
func listAll[T any](
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	route string,
	page func(context.Context, *github.ListOptions) ([]T, *github.Response, error),
) ([]T, error) {
	opts := &github.ListOptions{PerPage: 100}
	var out []T
	for {
		items, resp, err := page(readCtx(ctx, repo, route), opts)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// mapAll converts a page of SDK values into desired-document shape.
func mapAll[T any](items []T, decode func(T) map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, decode(it))
	}
	return out
}

// keyOf returns the value of the diff's key field, preferring the
// desired side and falling back to live state for deletions.
func keyOf(d diff.Diff, field string) string {
	if v := stringField(d.Desired, field); v != "" {
		return v
	}
	return stringField(d.Current, field)
}

// isNotFound reports a 404 from either the SDK or the raw client, so
// lanes can treat "this resource does not exist yet" uniformly.
func isNotFound(err error) bool {
	if ghclient.IsNotFound(err) {
		return true
	}
	var ge *github.ErrorResponse
	if errorsAs(err, &ge) {
		return ge.Response != nil && ge.Response.StatusCode == 404
	}
	return false
}

// isConflict recognises a 409. GitHub uses it for "this sub-resource
// does not apply in the current mode" — reading the Actions allow-list
// before allowed_actions is "selected", for instance.
func isConflict(err error) bool {
	var ge *github.ErrorResponse
	if !errorsAs(err, &ge) || ge.Response == nil {
		return false
	}
	return ge.Response.StatusCode == 409
}

// errorsAs is errors.As, wrapped so this file keeps one import block.
func errorsAs(err error, target any) bool { return stderrors.As(err, target) }

// stringField reads a string out of a diff side, tolerating a nil or
// non-map value so callers do not each repeat the type assertion.
func stringField(v any, key string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// intField reads a numeric field that may be an int (set directly from a
// typed value) or a float64 (round-tripped through JSON). Returns 0 when
// the key is missing. Used by lanes that address resources by GitHub's
// numeric ID.
func intField(v any, key string) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch n := m[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	}
	return 0
}
