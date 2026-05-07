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

// Action names match the TS ApplyResult shape: created/updated/deleted/skipped.
type Action string

const (
	Created Action = "created"
	Updated Action = "updated"
	Deleted Action = "deleted"
	Skipped Action = "skipped"
)

// Result is one mutation outcome.
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
