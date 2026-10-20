package ghclient

import "context"

// Per-call metadata travels in the request context.
//
// Once GitHub calls go through go-github, the call site no longer builds
// the *http.Request — the SDK does. Anything our transports need has to
// reach them some other way, and context is the only channel the SDK
// forwards untouched. Three things need to travel:
//
//	priority — which queue slot the rate limiter should give this call,
//	           so a PR check never waits behind a cron sweep.
//	owner    — whose installation token to mint. It is usually derivable
//	           from the URL, but not for /graphql, and parsing paths to
//	           decide which credential to present is not something to get
//	           subtly wrong.
//	route    — the low-cardinality path template for the `route` metric
//	           label. The SDK knows the concrete URL; only the caller
//	           knows the template.
type callInfo struct {
	prio  Priority
	pool  Pool
	owner string
	route string
}

type callInfoKey struct{}

// WithCall annotates ctx for one REST call. Pass the route template from
// the ghapi package.
func WithCall(ctx context.Context, prio Priority, owner, route string) context.Context {
	return context.WithValue(ctx, callInfoKey{}, callInfo{
		prio: prio, pool: PoolREST, owner: owner, route: route,
	})
}

// WithGraphQLCall is WithCall for calls that draw on the GraphQL budget,
// which GitHub meters separately from REST.
func WithGraphQLCall(ctx context.Context, prio Priority, owner string) context.Context {
	return context.WithValue(ctx, callInfoKey{}, callInfo{
		prio: prio, pool: PoolGraphQL, owner: owner, route: "/graphql",
	})
}

// callFrom returns the annotation, or a safe default. An un-annotated
// call is treated as the lowest priority and bucketed under RouteOther
// rather than being labelled with its raw URL — an unbounded `route`
// label is how a single counter turns into tens of thousands of series.
func callFrom(ctx context.Context) callInfo {
	if ci, ok := ctx.Value(callInfoKey{}).(callInfo); ok {
		if ci.pool == "" {
			ci.pool = PoolREST
		}
		if ci.route == "" {
			ci.route = RouteOther
		}
		return ci
	}
	return callInfo{prio: PriorityCronReconcile, pool: PoolREST, route: RouteOther}
}

// RouteFromContext exposes the declared route template. Tests use it to
// verify that the label a call site declares actually describes the URL
// the request went to — a mismatch is invisible in production, it just
// files the call under the wrong metric series.
func RouteFromContext(ctx context.Context) string { return callFrom(ctx).route }
