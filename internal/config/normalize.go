package config

// Normalize is the layer-1 → layer-2 step: it turns the parsed YAML
// into the desired-state document the differ compares against live
// GitHub state.
//
// It exists to keep two things true at once:
//
//   - A field the operator never mentioned stays unmanaged. Normalize
//     never invents a value just because GitHub has a default.
//   - A field the operator DID engage stays diffable. Where GitHub
//     always reports a value (an empty array rather than null, a
//     sub-object rather than an absent key), the desired document has
//     to say the same thing or the diff reports a change on every run.
//
// The distinction is per-container: `conditions.ref_name` only gets
// `include: []` because the operator wrote `conditions:` at all.
//
// Normalize is idempotent and safe to call on a partially populated
// Settings.
func (s *Settings) Normalize() {
	if s == nil {
		return
	}
	if s.Rulesets != nil {
		for i := range s.Rulesets.Rulesets {
			s.Rulesets.Rulesets[i].normalize()
		}
	}
	if s.Branches != nil {
		for i := range s.Branches.Branches {
			s.Branches.Branches[i].normalize()
		}
	}
}

func (r *Ruleset) normalize() {
	// GitHub rejects `rules: null` and always reports `[]`.
	if r.Rules == nil {
		r.Rules = []RulesetRule{}
	}
	if r.Conditions != nil {
		if r.Conditions.RefName.Include == nil {
			r.Conditions.RefName.Include = []string{}
		}
		if r.Conditions.RefName.Exclude == nil {
			r.Conditions.RefName.Exclude = []string{}
		}
	}
}

// normalize completes a branch-protection record.
//
// Because PUT .../protection is a whole-object replace (see the type
// doc), the optional booleans GitHub defaults to false on write are
// defaulted to false here too. Without that, the dry-run would stay
// silent about settings the apply is about to turn off.
func (b *BranchProtection) normalize() {
	for _, f := range []**bool{
		&b.EnforceAdmins,
		&b.RequiredLinearHistory,
		&b.AllowForcePushes,
		&b.AllowDeletions,
		&b.RequiredConversationResolution,
	} {
		if *f == nil {
			*f = Ptr(false)
		}
	}
	if c := b.RequiredStatusChecks; c != nil {
		if c.Strict == nil {
			c.Strict = Ptr(false)
		}
		if c.Contexts == nil {
			c.Contexts = []string{}
		}
	}
	if r := b.Restrictions; r != nil {
		if r.Users == nil {
			r.Users = []string{}
		}
		if r.Teams == nil {
			r.Teams = []string{}
		}
	}
}
