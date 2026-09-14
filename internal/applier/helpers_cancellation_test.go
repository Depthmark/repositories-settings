package applier

import (
	"context"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/diff"
)

func TestNamedListRun_CancellationProducesExplicitFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	desired := []map[string]any{
		{"slug": "admin", "permission": "admin"},
		{"slug": "readers", "permission": "pull"},
	}
	mutations := 0
	_, results, err := namedListRun(
		ctx,
		"teams",
		"slug",
		func(context.Context) ([]map[string]any, error) { return nil, nil },
		desired,
		func(context.Context, diff.Diff) (Action, error) {
			mutations++
			return Created, nil
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mutations != 0 {
		t.Fatalf("mutations = %d, want 0 after cancellation", mutations)
	}
	if len(results) != len(desired) {
		t.Fatalf("results = %d, want %d explicit cancellation failures", len(results), len(desired))
	}

	for i, result := range results {
		if result.Resource == "" {
			t.Errorf("result %d has an empty resource: %+v", i, result)
		}
		if result.Action != Created || result.Success || result.Error != context.Canceled.Error() {
			t.Errorf("result %d = %+v, want failed create with context canceled", i, result)
		}
		if result.APICallsUsed != 0 {
			t.Errorf("result %d API calls = %d, want 0", i, result.APICallsUsed)
		}
	}
}
