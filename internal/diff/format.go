package diff

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatMarkdown renders an actionable diff list for PR check output.
func FormatMarkdown(diffs []Diff) string {
	actionable := make([]Diff, 0, len(diffs))
	for _, d := range diffs {
		if d.Action != Noop {
			actionable = append(actionable, d)
		}
	}
	if len(actionable) == 0 {
		return "No changes detected. Current settings match the desired configuration."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "### %d change(s) planned\n\n", len(actionable))
	for _, d := range actionable {
		icon := "~"
		switch d.Action {
		case Create:
			icon = "+"
		case Delete:
			icon = "-"
		}
		fmt.Fprintf(&b, "#### %s `%s` — %s\n\n", icon, d.Resource, d.Action)
		if len(d.Changes) > 0 {
			b.WriteString("| Field | Current | Desired |\n")
			b.WriteString("|-------|---------|---------|\n")
			for _, c := range d.Changes {
				fmt.Fprintf(&b, "| `%s` | %s | %s |\n", c.Path, fmtVal(c.From), fmtVal(c.To))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func fmtVal(v any) string {
	if v == nil {
		return "_(unset)_"
	}
	j, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("`%v`", v)
	}
	return "`" + string(j) + "`"
}
