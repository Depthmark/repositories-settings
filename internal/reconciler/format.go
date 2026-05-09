package reconciler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/diff"
)

// FormatReportMarkdown renders a Report into the full PR-comment /
// /api/check `summary` body. Order favours visibility of the things
// that change behaviour silently:
//
//  1. Verdict line — H2 with human status + counts
//  2. ⚠️ Disabled by operator policy — TOP, open by default, so a
//     user whose secrets/pages YAML is being silently ignored sees
//     the warning before they scroll into the diff
//  3. Errors / Not allowed — open by default (action required)
//  4. What's changing — the per-resource diff
//  5. Apply hint — one-liner pointing at the bot's PR commands
//
// Every section is wrapped in <details> so a reviewer can collapse
// any of them; the warning and error sections default to open since
// they require action, the diff parent is open, and the per-action
// sub-sections inside the diff (Remove/Modify/Add/Unchanged) are
// each collapsible too — collapsed by default for Unchanged because
// it can grow long when most config is steady-state.
//
// This formatter is shared between /api/check and the bot's sticky
// PR comment, so both surfaces look identical.
func FormatReportMarkdown(rep *Report) string {
	if rep == nil {
		return ""
	}
	blocked := blockedFromApplied(rep.Applied)
	skipped := skippedFromApplied(rep.Applied)
	createN, updateN, deleteN, _ := countDiffs(rep.Diffs)
	totalChanges := createN + updateN + deleteN

	var b strings.Builder

	// 1. Verdict — first line a reviewer reads.
	writeVerdict(&b, rep, blocked, skipped, totalChanges, createN, updateN, deleteN)

	// 2. Operator-disabled lanes — TOP, with warning emoji. Promoted
	// from "informational footer" to "top warning" because silently
	// ignoring a configured section is exactly the failure mode this
	// section exists to surface — burying it lost the point.
	if len(skipped) > 0 {
		fmt.Fprintf(&b, "<details open>\n<summary>:warning: %d section(s) disabled by operator policy</summary>\n\n", len(skipped))
		b.WriteString("These sections are configured in this repo but the deployment operator has disabled them. The YAML stays in place; reconciliation will resume if the operator re-enables the lane.\n\n")
		writeSkippedTable(&b, skipped)
		b.WriteString("\n</details>\n\n")
	}

	// 3. Errors — open by default, these block the apply and require action.
	if len(blocked) > 0 {
		fmt.Fprintf(&b, "<details open>\n<summary>:no_entry: %d error(s) — these block the apply</summary>\n\n", len(blocked))
		b.WriteString("Resolve the underlying issue before merging. Common causes: missing GitHub App permission, locked branch protection, or a value rejected by the GitHub API.\n\n")
		writeBlockedTable(&b, blocked)
		b.WriteString("\n</details>\n\n")
	}

	// 4. The diff — the actual changes the reviewer is approving.
	// Wrapped in <details open> so it can be collapsed for runs where
	// the user only cares about the verdict + warnings above.
	b.WriteString("<details open>\n<summary>What's changing</summary>\n\n")
	b.WriteString(diff.FormatMarkdown(rep.Diffs))
	b.WriteString("\n</details>\n\n")

	// 5. Apply hint — only meaningful when there's something to apply
	// and nothing's blocking. Errors take priority; in that state the
	// hint would be misleading ("apply now" → would re-fail).
	if rep.DryRun && totalChanges > 0 && len(blocked) == 0 {
		b.WriteString("> Comment `apply` to apply now, `recheck` to refresh.\n")
	}

	return b.String()
}

// writeVerdict emits the H2 line summarising the entire run. The
// phrasing depends on three axes — DryRun, has-errors, has-changes —
// and chooses the verb tense and adjective accordingly. Subtotals
// follow on the next line so the reader has full breakdown without
// scrolling.
func writeVerdict(b *strings.Builder, rep *Report, blocked, skipped []blockedEntry, totalChanges, createN, updateN, deleteN int) {
	switch {
	case len(blocked) > 0:
		fmt.Fprintf(b, "## :rotating_light: repo-settings — blocked (%d error(s))\n\n", len(blocked))
	case totalChanges == 0 && rep.DryRun:
		b.WriteString("## :white_check_mark: repo-settings — no changes\n\n")
	case totalChanges == 0:
		b.WriteString("## :white_check_mark: repo-settings — applied (no changes)\n\n")
	case rep.DryRun:
		fmt.Fprintf(b, "## :large_blue_circle: repo-settings — %d change(s) ready to apply\n\n", totalChanges)
	default:
		fmt.Fprintf(b, "## :white_check_mark: repo-settings — applied %d change(s)\n\n", totalChanges)
	}

	// Subtotal line: only printed when there's something to count.
	// Skipping when zero keeps the no-changes verdict from being
	// followed by a meaningless "0 add · 0 modify · 0 remove".
	if totalChanges > 0 {
		fmt.Fprintf(b, "**%d add · %d modify · %d remove**", createN, updateN, deleteN)
		if len(skipped) > 0 {
			fmt.Fprintf(b, "  ·  %d skipped by operator policy", len(skipped))
		}
		b.WriteString("\n\n")
	} else if len(skipped) > 0 {
		fmt.Fprintf(b, "**%d skipped by operator policy**\n\n", len(skipped))
	}
}

func countDiffs(diffs []diff.Diff) (creates, updates, deletes, noops int) {
	for _, d := range diffs {
		switch d.Action {
		case diff.Create:
			creates++
		case diff.Update:
			updates++
		case diff.Delete:
			deletes++
		case diff.Noop:
			noops++
		}
	}
	return
}

// blockedEntry summarises one failed lane Result for the "Errors"
// section. Sorted by Resource so reruns produce stable output.
type blockedEntry struct {
	Resource string
	Action   applier.Action
	Reason   string
}

// blockedFromApplied collects every Result whose Success is false.
// Operator-disabled lanes are emitted as Skipped+Success=true, so
// they don't appear here — they get their own section.
func blockedFromApplied(rs []applier.Result) []blockedEntry {
	out := make([]blockedEntry, 0, len(rs))
	for _, r := range rs {
		if r.Success {
			continue
		}
		out = append(out, blockedEntry{
			Resource: r.Resource,
			Action:   r.Action,
			Reason:   firstLine(r.Error),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource != out[j].Resource {
			return out[i].Resource < out[j].Resource
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

// skippedFromApplied collects every Skipped/Success=true Result.
func skippedFromApplied(rs []applier.Result) []blockedEntry {
	out := make([]blockedEntry, 0, len(rs))
	for _, r := range rs {
		if r.Action != applier.Skipped || !r.Success {
			continue
		}
		out = append(out, blockedEntry{
			Resource: r.Resource,
			Action:   r.Action,
			Reason:   firstLine(r.Error),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out
}

func writeSkippedTable(b *strings.Builder, entries []blockedEntry) {
	b.WriteString("| Resource | Reason |\n")
	b.WriteString("|----------|--------|\n")
	for _, e := range entries {
		fmt.Fprintf(b, "| `%s` | %s |\n", e.Resource, escapePipe(e.Reason))
	}
}

func writeBlockedTable(b *strings.Builder, entries []blockedEntry) {
	b.WriteString("| Resource | Attempted | Reason |\n")
	b.WriteString("|----------|-----------|--------|\n")
	for _, e := range entries {
		fmt.Fprintf(b, "| `%s` | %s | %s |\n", e.Resource, e.Action, escapePipe(e.Reason))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
