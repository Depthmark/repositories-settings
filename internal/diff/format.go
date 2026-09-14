package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FormatMarkdown renders an actionable diff list as a sectioned report.
//
// Section order is risk-descending — Remove first, Add last — so a
// reviewer scanning a long diff sees destructive changes before
// additive ones. Empty sections are dropped (no "0 remove" header) so
// the comment doesn't grow boilerplate; the inspected-counts line at
// the top still tells the reader nothing was missed.
//
//	**N change(s) planned** — A add · B modify · C remove · D unchanged
//
//	### ➖ Remove (C)        only when C > 0
//	### ✏️ Modify (B)       only when B > 0
//	### ➕ Add (A)          only when A > 0
//	### ✅ Unchanged (D)    collapsed under <details>; only when D > 0
//
// Per-resource entries use a 2-column "Field | Change" table where
// "Change" is `from → to` for Modify, `to` for Add, and `from` for
// Remove. Add rows whose only signal is "the resource sub-key
// matches the value of a name/slug/url field" are dropped — they
// duplicate the resource heading.
func FormatMarkdown(diffs []Diff) string {
	creates, updates, deletes, noops := splitByAction(diffs)
	total := len(creates) + len(updates) + len(deletes) + len(noops)
	if total == 0 {
		return "_No resources to inspect. Configuration is empty or all sections are unmanaged._"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**%d resource(s) inspected** — %d add · %d modify · %d remove · %d unchanged\n\n",
		total, len(creates), len(updates), len(deletes), len(noops))

	writeActionSection(&b, "➖ Remove", deletes, Delete)
	writeActionSection(&b, "✏️ Modify", updates, Update)
	writeActionSection(&b, "➕ Add", creates, Create)
	writeNoopSection(&b, noops)
	return b.String()
}

func splitByAction(diffs []Diff) (creates, updates, deletes, noops []Diff) {
	for _, d := range diffs {
		switch d.Action {
		case Create:
			creates = append(creates, d)
		case Update:
			updates = append(updates, d)
		case Delete:
			deletes = append(deletes, d)
		case Noop:
			noops = append(noops, d)
		}
	}
	sortByResource(creates)
	sortByResource(updates)
	sortByResource(deletes)
	sortByResource(noops)
	return
}

func sortByResource(ds []Diff) {
	sort.Slice(ds, func(i, j int) bool { return ds[i].Resource < ds[j].Resource })
}

// writeActionSection renders one section header + its entries.
// Wrapped in <details open> so the reviewer can collapse a section
// they've already read; collapse-only-when-empty stays the rule —
// rendering "Remove (0)" with empty contents was the #1 readability
// complaint.
func writeActionSection(b *strings.Builder, label string, ds []Diff, action Action) {
	if len(ds) == 0 {
		return
	}
	fmt.Fprintf(b, "<details open>\n<summary>%s (%d)</summary>\n\n", label, len(ds))
	writeDiffEntries(b, ds, action)
	b.WriteString("</details>\n\n")
}

// writeNoopSection wraps unchanged resources in <details> so a long
// noop list (typical when most config is steady-state) doesn't drown
// the actionable changes above it.
func writeNoopSection(b *strings.Builder, ds []Diff) {
	if len(ds) == 0 {
		return
	}
	fmt.Fprintf(b, "<details>\n<summary>✅ %d unchanged</summary>\n\n", len(ds))
	for _, d := range ds {
		fmt.Fprintf(b, "- `%s`\n", d.Resource)
	}
	b.WriteString("\n</details>\n\n")
}

// writeDiffEntries renders each diff as a `<resource>` heading plus
// a 2-col table when there are field-level changes. Rows whose
// signal is purely "the field value equals the resource sub-key"
// (typical Add boilerplate: `name=STS_URL` for `variables.STS_URL`)
// are filtered out — they duplicate the heading and inflate every
// Add table.
func writeDiffEntries(b *strings.Builder, ds []Diff, action Action) {
	for _, d := range ds {
		fmt.Fprintf(b, "- `%s`\n", d.Resource)
		rows := visibleChanges(d)
		if len(rows) > 0 {
			b.WriteString("  | Field | Change |\n")
			b.WriteString("  |-------|--------|\n")
			for _, c := range rows {
				fmt.Fprintf(b, "  | `%s` | %s |\n", c.Path, formatChange(c, action))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
}

// visibleChanges drops field rows that just echo the resource
// sub-key (`name`, `slug`, `username`, `url`, `key_prefix`) — those
// rows say nothing the resource heading didn't. Applied only when
// From is unset (Add boilerplate); on real updates we keep the row
// even if value matches sub-key, since "renaming back to itself" is
// not a thing we'd ever emit but rule-of-least-surprise leaves it.
func visibleChanges(d Diff) []FieldChange {
	subkey := resourceSubkey(d.Resource)
	out := make([]FieldChange, 0, len(d.Changes))
	for _, c := range d.Changes {
		if c.From == nil && isIdentityField(c.Path) && stringValue(c.To) == subkey && subkey != "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// resourceSubkey returns the part after the first '.' in a resource
// id ("variables.STS_URL" → "STS_URL"). Returns "" for resource ids
// with no sub-key (singletons like "actions").
func resourceSubkey(resource string) string {
	if i := strings.IndexByte(resource, '.'); i >= 0 {
		return resource[i+1:]
	}
	return ""
}

// isIdentityField reports whether a field path commonly duplicates
// the resource sub-key. Conservative list — adding fields here just
// hides more boilerplate, never the reverse.
func isIdentityField(path string) bool {
	switch path {
	case "name", "slug", "username", "url", "key_prefix":
		return true
	}
	return false
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

// formatChange renders one row's "Change" cell. Add shows the
// destination value alone (the source is by definition unset); Remove
// shows the source alone; Modify shows `from → to`. The arrow is
// load-bearing — it makes the 2-col table read top-to-bottom like
// inline diffs, instead of forcing the eye to triangulate three
// columns.
func formatChange(c FieldChange, action Action) string {
	switch action {
	case Create:
		return fmtVal(c.To)
	case Delete:
		return fmtVal(c.From)
	default:
		return fmtVal(c.From) + " → " + fmtVal(c.To)
	}
}

// fmtVal serializes one value into a backtick-fenced JSON literal.
// SetEscapeHTML(false) is the fix for the `INC-<num>`
// rendering bug: by default encoding/json escapes `<>&` for HTML
// safety, which made every URL template containing `<placeholder>`
// look like raw escapes in the PR comment. The output of this
// function lands inside backticks (no HTML interpretation), so
// escaping is unnecessary and actively harmful to readability.
func fmtVal(v any) string {
	if v == nil {
		return "—"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("`%v`", v)
	}
	s := strings.TrimSuffix(buf.String(), "\n")
	return "`" + s + "`"
}
