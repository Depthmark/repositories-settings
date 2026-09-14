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
	if !strings.Contains(out, "No resources to inspect") {
		t.Fatalf("got %q", out)
	}
}

// All-noop input produces only the inspected-counts line + the
// collapsed Unchanged details block. The action sections (Add /
// Modify / Remove) are skipped entirely since their counts are zero —
// reviewers see "0 add · 0 modify · 0 remove" once on the counts
// line and don't need three repeating "_None._" subsections below.
func TestFormatMarkdown_AllNoopShowsOnlyUnchangedDetails(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.x", Action: Noop},
		{Resource: "rulesets.y", Action: Noop},
	})
	for _, want := range []string{
		"2 resource(s) inspected",
		"0 add",
		"0 modify",
		"0 remove",
		"2 unchanged",
		"<details>",
		"teams.x",
		"rulesets.y",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"<summary>➕ Add",
		"<summary>✏️ Modify",
		"<summary>➖ Remove",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("zero-count section should be omitted but found %q:\n%s", unwanted, out)
		}
	}
}

// Section ordering is risk-descending (Remove → Modify → Add) so a
// reviewer scanning a long diff sees destructive changes first.
// Unchanged stays last, collapsed.
func TestFormatMarkdown_SectionOrderRiskDescending(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.alpha", Action: Update,
			Changes: []FieldChange{{Path: "permission", From: "pull", To: "push"}}},
		{Resource: "teams.zeta", Action: Create,
			Changes: []FieldChange{{Path: "permission", From: nil, To: "push"}}},
		{Resource: "teams.beta", Action: Delete},
		{Resource: "rulesets.gamma", Action: Noop},
	})
	for _, want := range []string{
		"4 resource(s) inspected",
		"1 add · 1 modify · 1 remove · 1 unchanged",
		"<summary>➖ Remove (1)",
		"<summary>✏️ Modify (1)",
		"<summary>➕ Add (1)",
		"teams.zeta",
		"teams.alpha",
		"teams.beta",
		"rulesets.gamma",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	r := strings.Index(out, "<summary>➖ Remove")
	m := strings.Index(out, "<summary>✏️ Modify")
	a := strings.Index(out, "<summary>➕ Add")
	u := strings.Index(out, "<details>") // unchanged is wrapped
	if r >= m || m >= a || a >= u {
		t.Fatalf("section order broken (Remove<Modify<Add<Unchanged): r=%d m=%d a=%d u=%d", r, m, a, u)
	}
}

// Empty sections render no header — the inspected-counts line up top
// already reports zero. Eliminates the "_None._" boilerplate that
// dominated the sticky comment when most actions were zero.
func TestFormatMarkdown_EmptySectionsAreOmitted(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.x", Action: Create, Changes: []FieldChange{{Path: "permission", From: nil, To: "push"}}},
	})
	if !strings.Contains(out, "<summary>➕ Add (1)") {
		t.Errorf("Add section missing:\n%s", out)
	}
	for _, unwanted := range []string{
		"<summary>✏️ Modify",
		"<summary>➖ Remove",
		"_None._",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("zero-count section/marker should not render: %q\n%s", unwanted, out)
		}
	}
}

// Resources within a section appear in lexical order regardless of
// input ordering — keeps reruns stable so the PR comment update
// doesn't shuffle for cosmetic reasons.
func TestFormatMarkdown_StableOrderingWithinSection(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.zeta", Action: Create},
		{Resource: "teams.alpha", Action: Create},
		{Resource: "teams.beta", Action: Create},
	})
	a := strings.Index(out, "teams.alpha")
	b := strings.Index(out, "teams.beta")
	z := strings.Index(out, "teams.zeta")
	if a >= b || b >= z {
		t.Fatalf("expected alphabetical order within Add section, got:\n%s", out)
	}
}

// No-change entries are wrapped in <details> so a long unchanged
// list (50 teams that all match) doesn't drown the comment.
func TestFormatMarkdown_NoChangeWrappedInDetails(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.x", Action: Noop},
	})
	if !strings.Contains(out, "<details>") || !strings.Contains(out, "</details>") {
		t.Fatalf("expected <details> wrapper around No-change entries:\n%s", out)
	}
	if !strings.Contains(out, "1 unchanged") {
		t.Fatalf("expected summary count in details:\n%s", out)
	}
}

// HTML escaping in JSON values (`\u003c`, `\u003e`, `\u0026`) was the
// `INC-<num>` rendering bug — encoding/json escapes those
// chars by default for HTML safety, but our output goes inside
// backticks where escaping is harmful.
func TestFormatMarkdown_StringValuesAreNotHTMLEscaped(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "autolinks.INC-", Action: Create,
			Changes: []FieldChange{
				{Path: "url_template", From: nil, To: "https://example.com/INC-<num>"},
			}},
	})
	// The fix: encoding/json's default SetEscapeHTML(true) would
	// produce `INC-<num>` which renders as raw escapes
	// inside backticks. With SetEscapeHTML(false) the angle brackets
	// land verbatim and read normally.
	if !strings.Contains(out, "INC-<num>") {
		t.Fatalf("expected literal <num>, got escaped sequence:\n%s", out)
	}
	if strings.Contains(out, `\u003c`) || strings.Contains(out, `\u003e`) || strings.Contains(out, `\u0026`) {
		t.Fatalf("JSON unicode-escape sequences leaked into output:\n%s", out)
	}
}

// Add rows whose only signal is "name field equals the resource
// sub-key" duplicate the heading and inflate every Add table. Drop
// them — the heading already names the resource.
func TestFormatMarkdown_DropsIdentityRowsOnAdd(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "variables.STS_URL", Action: Create,
			Changes: []FieldChange{
				{Path: "name", From: nil, To: "STS_URL"},                      // duplicate of subkey — drop
				{Path: "value", From: nil, To: "https://github-sts.test.com"}, // keep
			}},
		{Resource: "teams.platform", Action: Create,
			Changes: []FieldChange{
				{Path: "slug", From: nil, To: "platform"},    // drop
				{Path: "permission", From: nil, To: "admin"}, // keep
			}},
	})
	// Identity rows must not appear.
	if strings.Contains(out, "| `name` |") {
		t.Errorf("name row should be filtered out when it duplicates the subkey:\n%s", out)
	}
	if strings.Contains(out, "| `slug` |") {
		t.Errorf("slug row should be filtered out when it duplicates the subkey:\n%s", out)
	}
	// Real rows must remain.
	if !strings.Contains(out, "| `value` |") {
		t.Errorf("value row should be kept:\n%s", out)
	}
	if !strings.Contains(out, "| `permission` |") {
		t.Errorf("permission row should be kept:\n%s", out)
	}
}

// Identity rows are kept on Modify — a name change is real signal,
// even if it'd be "renaming the resource to itself" (which wouldn't
// emit a row anyway since no diff). The filter is Add-only.
func TestFormatMarkdown_KeepsIdentityRowsOnModify(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "teams.alpha", Action: Update,
			Changes: []FieldChange{
				{Path: "slug", From: "old-alpha", To: "alpha"},
			}},
	})
	if !strings.Contains(out, "| `slug` |") {
		t.Errorf("slug row should be kept on Modify (real change):\n%s", out)
	}
}

// 2-col layout: per-row "Change" cell uses `from → to` for Modify,
// just `to` for Add, just `from` for Remove. The arrow makes the
// before/after axis read top-to-bottom instead of forcing eye
// triangulation across three columns.
func TestFormatMarkdown_TwoColumnLayoutWithArrows(t *testing.T) {
	out := FormatMarkdown([]Diff{
		{Resource: "repository", Action: Update,
			Changes: []FieldChange{{Path: "has_issues", From: true, To: false}}},
		{Resource: "teams.x", Action: Create,
			Changes: []FieldChange{{Path: "permission", From: nil, To: "push"}}},
	})
	if !strings.Contains(out, "| Field | Change |") {
		t.Fatalf("expected 2-col table header:\n%s", out)
	}
	if !strings.Contains(out, "`true` → `false`") {
		t.Errorf("expected arrow on Modify row:\n%s", out)
	}
	// Add: only the destination value, no arrow.
	if !strings.Contains(out, "| `permission` | `\"push\"` |") {
		t.Errorf("expected Add row to show only the destination value:\n%s", out)
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

// A JSON round-trip widens every number to float64, so an int desired
// value and a float64 live value describe the same setting. Everything
// else keeps its type: string "true" is not boolean true, and string "1"
// is not the number 1. Coercing those was how a repository whose live
// state had drifted to a string reported "no changes".
func TestFields_TypedDriftIsNotCoerced(t *testing.T) {
	t.Run("numeric widening is equal", func(t *testing.T) {
		changes := Fields(
			map[string]any{"required_approvals": float64(2), "days": int64(7)},
			map[string]any{"required_approvals": 2, "days": 7.0},
			"",
		)
		if len(changes) != 0 {
			t.Fatalf("numeric widening should not be drift, got %+v", changes)
		}
	})

	t.Run("large adjacent integers remain different", func(t *testing.T) {
		changes := Fields(
			map[string]any{"id": int64(9007199254740993)},
			map[string]any{"id": uint64(9007199254740992)},
			"",
		)
		if len(changes) != 1 {
			t.Fatalf("numeric normalization hid large-integer drift: %+v", changes)
		}
	})

	for _, tc := range []struct {
		name       string
		live, want any
	}{
		{"string vs bool", "true", true},
		{"string vs number", "1", 1},
		{"number vs bool", 1, true},
		{"bool vs string", false, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := Fields(
				map[string]any{"k": tc.live},
				map[string]any{"k": tc.want},
				"",
			)
			if len(changes) != 1 {
				t.Fatalf("expected drift between %#v and %#v, got %+v", tc.live, tc.want, changes)
			}
		})
	}
}
