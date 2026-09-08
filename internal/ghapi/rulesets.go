package ghapi

// Rulesets are the one resource that stays on the raw REST path rather
// than moving to go-github.
//
// The SDK models a ruleset's rules as a typed union — one Go field per
// rule kind it knows about. Our schema models them as GitHub documents
// them: a `type` string plus a free-form `parameters` map. That is the
// more useful model here, because a repo can adopt a rule type the day
// GitHub ships it, without waiting for an SDK release and a redeploy.
// Round-tripping through the typed union would quietly drop any rule the
// installed SDK version does not recognise, which for a policy-enforcement
// tool is the worst possible failure: the dry-run would report the rule as
// absent and the apply would delete it.
//
// The trade is that this file owns the request shape, so the rules GitHub
// requires to be arrays are normalized here.

// Body is a request body under construction, used by the raw-path
// resources. A key the operator did not manage is left out entirely
// rather than sent as an explicit null: GitHub answers `"rules": null`
// with a 422, and on a PATCH an explicit null clears the field.
type Body map[string]any

func NewBody() Body { return Body{} }

func (b Body) Set(k string, v any) Body { b[k] = v; return b }

// Copy takes each key from src only if src actually carries it.
func (b Body) Copy(src map[string]any, keys ...string) Body {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			b[k] = v
		}
	}
	return b
}

// Default assigns only when the key is still absent.
func (b Body) Default(k string, v any) Body {
	if _, ok := b[k]; !ok {
		b[k] = v
	}
	return b
}

// Nullable ensures the key exists, using nil when absent — for endpoints
// that require a key to be present and read null as "off".
func (b Body) Nullable(k string) Body {
	if _, ok := b[k]; !ok {
		b[k] = nil
	}
	return b
}

// RulesetBody encodes POST /rulesets and PUT /rulesets/{id}.
//
// `rules` is required and must be an array — null is a 422 — and the same
// holds for the include/exclude lists under `conditions.ref_name`.
// config.Normalize already guarantees this on the desired document so the
// diff stays stable against GitHub's `[]`; these are the belt to that
// braces, for bodies built from any other path.
func RulesetBody(desired any) Body {
	src := asMap(desired)
	b := NewBody().Copy(src, "name", "target", "enforcement", "conditions", "bypass_actors", "rules")
	b.Default("rules", []any{})
	if cond := asMap(b["conditions"]); cond != nil {
		if rn := asMap(cond["ref_name"]); rn != nil {
			if rn["include"] == nil {
				rn["include"] = []any{}
			}
			if rn["exclude"] == nil {
				rn["exclude"] = []any{}
			}
		}
	}
	return b
}
