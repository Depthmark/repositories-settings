package diff

import (
	"strings"
	"testing"
)

func TestFieldsIgnoresUnmanaged(t *testing.T) {
	cur := map[string]any{"a": 1, "b": 2}
	des := map[string]any{"a": 1}
	changes := Fields(cur, des, "")
	if len(changes) != 0 {
		t.Fatalf("expected no changes (b unmanaged), got %+v", changes)
	}
}

func TestFieldsCreatesMissing(t *testing.T) {
	cur := map[string]any{}
	des := map[string]any{"a": 1}
	changes := Fields(cur, des, "")
	if len(changes) != 1 || changes[0].Path != "a" || changes[0].To != 1 {
		t.Fatalf("got %+v", changes)
	}
}

func TestFieldsRecursesObjects(t *testing.T) {
	cur := map[string]any{"x": map[string]any{"y": 1}}
	des := map[string]any{"x": map[string]any{"y": 2}}
	changes := Fields(cur, des, "")
	if len(changes) != 1 || changes[0].Path != "x.y" {
		t.Fatalf("got %+v", changes)
	}
}

func TestFieldsArraysEqualNoChange(t *testing.T) {
	cur := map[string]any{"l": []any{"a", "b"}}
	des := map[string]any{"l": []any{"a", "b"}}
	if changes := Fields(cur, des, ""); len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestFieldsArraysOrderedDifference(t *testing.T) {
	cur := map[string]any{"l": []any{"a", "b"}}
	des := map[string]any{"l": []any{"b", "a"}}
	if changes := Fields(cur, des, ""); len(changes) != 1 {
		t.Fatalf("expected 1 change for reordering, got %+v", changes)
	}
}

func TestNamedListCreateUpdateDeleteNoop(t *testing.T) {
	cur := []map[string]any{
		{"slug": "alpha", "permission": "push"},
		{"slug": "beta", "permission": "pull"},
		{"slug": "gone", "permission": "admin"},
	}
	des := []map[string]any{
		{"slug": "alpha", "permission": "push"},  // noop
		{"slug": "beta", "permission": "admin"},  // update
		{"slug": "newone", "permission": "pull"}, // create
	}
	diffs := NamedList("teams", cur, des, "slug")

	got := map[string]Action{}
	for _, d := range diffs {
		got[d.Resource] = d.Action
	}
	want := map[string]Action{
		"teams.alpha":  Noop,
		"teams.beta":   Update,
		"teams.newone": Create,
		"teams.gone":   Delete,
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: got %s want %s", k, got[k], v)
		}
	}
}

func TestSingleResourceFourCases(t *testing.T) {
	if d := SingleResource("r", nil, nil); d.Action != Noop {
		t.Fatalf("nil/nil: got %s", d.Action)
	}
	if d := SingleResource("r", nil, map[string]any{"a": 1}); d.Action != Create {
		t.Fatalf("create: got %s", d.Action)
	}
	if d := SingleResource("r", map[string]any{"a": 1}, nil); d.Action != Delete {
		t.Fatalf("delete: got %s", d.Action)
	}
	if d := SingleResource("r", map[string]any{"a": 1}, map[string]any{"a": 2}); d.Action != Update {
		t.Fatalf("update: got %s", d.Action)
	}
	if d := SingleResource("r", map[string]any{"a": 1}, map[string]any{"a": 1}); d.Action != Noop {
		t.Fatalf("noop: got %s", d.Action)
	}
}

func TestFormatMarkdownEmpty(t *testing.T) {
	out := FormatMarkdown(nil)
	if !strings.Contains(out, "No changes detected") {
		t.Fatalf("got %q", out)
	}
}

func TestFormatMarkdownWithChanges(t *testing.T) {
	d := Diff{
		Resource: "teams.alpha",
		Action:   Update,
		Changes:  []FieldChange{{Path: "permission", From: "pull", To: "push"}},
	}
	out := FormatMarkdown([]Diff{d})
	for _, want := range []string{"1 change(s) planned", "teams.alpha", "permission", "pull", "push"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestToMapStruct(t *testing.T) {
	type T struct {
		A int    `json:"a"`
		B string `json:"b,omitempty"`
	}
	m, err := ToMap(T{A: 1, B: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m["a"].(float64) != 1 || m["b"] != "x" {
		t.Fatalf("got %+v", m)
	}
}
