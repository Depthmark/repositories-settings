package reconciler

import (
	"strings"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/diff"
)

// A clean dry-run with no diffs and no failed lanes produces the
// "no changes" verdict — no Errors header, no apply hint.
func TestFormatReportMarkdown_NoChanges(t *testing.T) {
	out := FormatReportMarkdown(&Report{DryRun: true})
	for _, want := range []string{
		"repo-settings — no changes",
		"<summary>What's changing</summary>",
		"No resources to inspect",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"Errors",
		"## :rotating_light:",
		"Comment `apply`",
		"add ·",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("no-changes output should not contain %q:\n%s", unwanted, out)
		}
	}
}

// Verdict line for a dry-run with planned changes uses "ready to
// apply" with the total count, then a subtotal line on its own row.
func TestFormatReportMarkdown_VerdictDryRunReadyToApply(t *testing.T) {
	rep := &Report{
		DryRun: true,
		Diffs: []diff.Diff{
			{Resource: "teams.alpha", Action: diff.Create},
			{Resource: "repository", Action: diff.Update},
			{Resource: "rulesets.x", Action: diff.Delete},
		},
	}
	out := FormatReportMarkdown(rep)
	for _, want := range []string{
		"## :large_blue_circle: repo-settings — 3 change(s) ready to apply",
		"**1 add · 1 modify · 1 remove**",
		"<summary>What's changing</summary>",
		"Comment `apply`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rotating_light") {
		t.Errorf("no errors expected in this run:\n%s", out)
	}
}

// Verdict for an actual apply (DryRun=false) uses "applied N
// change(s)" — different verb tense from the dry-run wording.
func TestFormatReportMarkdown_VerdictAppliedSuccess(t *testing.T) {
	rep := &Report{
		DryRun: false,
		Diffs: []diff.Diff{
			{Resource: "teams.alpha", Action: diff.Create},
		},
	}
	out := FormatReportMarkdown(rep)
	if !strings.Contains(out, "applied 1 change(s)") {
		t.Errorf("expected applied verdict:\n%s", out)
	}
	if strings.Contains(out, "Comment `apply`") {
		t.Errorf("apply hint should be omitted on a real apply:\n%s", out)
	}
}

// When at least one lane fails, the verdict is "blocked" with the
// error count, and the Errors section comes before the diff. Apply
// hint is suppressed because it would be misleading (apply would
// re-fail).
func TestFormatReportMarkdown_VerdictBlockedShowsErrorsFirst(t *testing.T) {
	rep := &Report{
		DryRun: true,
		Diffs: []diff.Diff{
			{Resource: "teams.alpha", Action: diff.Create},
		},
		Applied: []applier.Result{
			{Resource: "secrets.MY_TOKEN", Action: applier.Created, Success: false, Error: "github 422: validation failed"},
			{Resource: "pages", Action: applier.Updated, Success: false, Error: "permission denied"},
			{Resource: "teams.alpha", Action: applier.Created, Success: true},
		},
	}
	out := FormatReportMarkdown(rep)
	for _, want := range []string{
		"## :rotating_light: repo-settings — blocked (2 error(s))",
		"<summary>:no_entry: 2 error(s) — these block the apply</summary>",
		"`secrets.MY_TOKEN`",
		"`pages`",
		"permission denied",
		"<summary>What's changing</summary>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Errors must precede diff.
	errIdx := strings.Index(out, "<summary>:no_entry: 2 error(s) — these block the apply</summary>")
	diffIdx := strings.Index(out, "<summary>What's changing</summary>")
	if errIdx <= 0 || errIdx >= diffIdx {
		t.Fatalf("Errors must precede diff: errIdx=%d diffIdx=%d\n%s", errIdx, diffIdx, out)
	}

	// Apply hint suppressed when there are errors.
	if strings.Contains(out, "Comment `apply`") {
		t.Errorf("apply hint should be suppressed when errors are present:\n%s", out)
	}

	// Successful entries must not appear in the Errors table.
	errorsBlock := out[errIdx:diffIdx]
	if strings.Contains(errorsBlock, "teams.alpha") {
		t.Errorf("successful result leaked into Errors section:\n%s", errorsBlock)
	}
}

func TestFormatReportMarkdown_TrimsMultilineErrors(t *testing.T) {
	rep := &Report{
		Applied: []applier.Result{
			{Resource: "rulesets.x", Action: applier.Updated, Success: false,
				Error: "github PUT /rulesets/3: 422\n{\n  \"message\": \"Validation Failed\"\n}"},
		},
	}
	out := FormatReportMarkdown(rep)
	if !strings.Contains(out, "github PUT /rulesets/3: 422") {
		t.Fatalf("expected first error line:\n%s", out)
	}
	if strings.Contains(out, "Validation Failed") {
		t.Fatalf("multi-line error body leaked:\n%s", out)
	}
}

func TestFormatReportMarkdown_EscapesPipeInError(t *testing.T) {
	rep := &Report{
		Applied: []applier.Result{
			{Resource: "x", Action: applier.Updated, Success: false, Error: "a | b | c"},
		},
	}
	out := FormatReportMarkdown(rep)
	if !strings.Contains(out, `a \| b \| c`) {
		t.Fatalf("expected pipes escaped:\n%s", out)
	}
}

func TestFormatReportMarkdown_NilSafe(t *testing.T) {
	if got := FormatReportMarkdown(nil); got != "" {
		t.Fatalf("expected empty for nil report, got %q", got)
	}
}

// Operator-disabled lanes render in a <details open> section ABOVE
// the diff. Silently ignoring a section the repo configured is
// exactly the failure this section exists to surface, so it must not
// compete for attention below the fold. The verdict line still
// carries the count.
func TestFormatReportMarkdown_SkippedAboveDiffAndOpen(t *testing.T) {
	rep := &Report{
		DryRun: true,
		Diffs: []diff.Diff{
			{Resource: "teams.alpha", Action: diff.Create},
		},
		Applied: []applier.Result{
			{Resource: "secrets", Action: applier.Skipped, Success: true, Error: "disabled by operator policy"},
			{Resource: "pages", Action: applier.Skipped, Success: true, Error: "disabled by operator policy"},
			{Resource: "teams.alpha", Action: applier.Created, Success: true},
		},
	}
	out := FormatReportMarkdown(rep)

	for _, want := range []string{
		"1 change(s) ready to apply",
		"2 skipped by operator policy",
		"<summary>What's changing</summary>",
		"<details open>",
		"2 section(s) disabled by operator policy",
		"`secrets`",
		"`pages`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Section ordering: verdict -> skipped -> diff.
	verdict := strings.Index(out, "ready to apply")
	skipped := strings.Index(out, "2 section(s) disabled by operator policy")
	diffIdx := strings.Index(out, "<summary>What's changing</summary>")
	if verdict >= skipped || skipped >= diffIdx {
		t.Fatalf("expected verdict<skipped<diff, got %d<%d<%d:\n%s", verdict, skipped, diffIdx, out)
	}

	// The block is expanded by default so the warning is readable
	// without a click.
	if !strings.Contains(out[:skipped], "<details open>") {
		t.Errorf("skipped section must be wrapped in <details open>:\n%s", out)
	}
}

// Skipped-only run (no diffs, no errors): verdict says "no changes",
// the subtotal line surfaces the skipped count, and the collapsed
// <details> block lists each skipped lane.
func TestFormatReportMarkdown_SkippedOnlyHasNoChangesVerdict(t *testing.T) {
	rep := &Report{
		DryRun: true,
		Applied: []applier.Result{
			{Resource: "secrets", Action: applier.Skipped, Success: true, Error: "disabled by operator policy"},
		},
	}
	out := FormatReportMarkdown(rep)
	for _, want := range []string{
		"no changes",
		"**1 skipped by operator policy**",
		"<details open>",
		"1 section(s) disabled by operator policy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "rotating_light") {
		t.Errorf("skipped-only run should not be marked as blocked:\n%s", out)
	}
}
