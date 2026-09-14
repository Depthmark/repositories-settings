// Package applier hosts the per-resource diff/apply logic. Each applier
// has the same shape:
//
//  1. GetLive — read the current state from GitHub.
//  2. Plan    — compare against desired and emit diff.Diff entries.
//  3. Apply   — execute the plan, returning per-mutation ApplyResult.
//
// Apply uses bounded concurrency (conc.DefaultApplierConcurrency) so a
// 100-item diff doesn't fire 100 simultaneous requests.
package applier

import (
	"context"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Action is what a lane did, or would do, to one resource.
//
// Skipped and Pending are both non-failures, and they mean different
// things:
//
//	Skipped — the operator disabled this resource on this deployment.
//	Pending — the configuration is correct and there is simply nothing
//	          for this service to do yet, because the value lives in
//	          another system. A declared secret whose value has not been
//	          uploaded is the canonical case: that is the normal steady
//	          state of every repo using the feature, so reporting it as
//	          an error turned those repos' PR checks permanently red.
type Action string

const (
	Created Action = "created"
	Updated Action = "updated"
	Deleted Action = "deleted"
	Skipped Action = "skipped"
	Pending Action = "pending"
)

// Result is one mutation outcome.
//
// Error carries the explanation for any non-applied outcome, not only
// failures: it is also where a Skipped lane's policy reason and a
// Pending resource's "waiting on" message live, so every surface
// renders them the same way.
type Result struct {
	Resource     string `json:"resource"`
	Action       Action `json:"action"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
	APICallsUsed int    `json:"api_calls_used"`
}

// Lane describes one applier in a form the reconciler can call uniformly.
// We can't use a parameterized interface in a `[]Applier` slice, so each
// resource exposes a Lane closure that captures its config and state.
type Lane struct {
	Resource string
	Run      func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error)
}

// success is a small helper for building successful Result values.
func success(resource string, a Action, calls int) Result {
	return Result{Resource: resource, Action: a, Success: true, APICallsUsed: calls}
}

// pending records a resource this service cannot act on yet. It is a
// success: nothing is wrong, and nothing is expected to change until
// the external value appears.
func pending(resource, reason string) Result {
	return Result{Resource: resource, Action: Pending, Success: true, Error: reason}
}

func failure(resource string, a Action, err error, calls int) Result {
	return Result{Resource: resource, Action: a, Success: false, Error: err.Error(), APICallsUsed: calls}
}

// actionFromDiff maps a diff.Action to an apply Action.
func actionFromDiff(a diff.Action) Action {
	switch a {
	case diff.Create:
		return Created
	case diff.Update:
		return Updated
	case diff.Delete:
		return Deleted
	default:
		return Skipped
	}
}
