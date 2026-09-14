package config

import (
	"fmt"
	"reflect"
	"strings"
)

var permRank = map[TeamPermission]int{
	PermPull:     0,
	PermTriage:   1,
	PermPush:     2,
	PermMaintain: 3,
	PermAdmin:    4,
}

// ValidatePolicy runs the resolved settings against an org admin policy
// and returns any violations. `repoCfg` is the repo-level (pre-merge)
// settings; needed so we can detect which fields the repo is *trying* to
// override (vs. inheriting unchanged from the org).
//
// Mirrors src/core/policy-engine.ts.
func ValidatePolicy(resolved *Settings, repoCfg *Settings, p AdminPolicy) []PolicyViolation {
	var v []PolicyViolation
	if p == (AdminPolicy{}) {
		return v
	}

	// Repo locks
	if p.Repo != nil && len(p.Repo.Locked) > 0 && repoCfg != nil && repoCfg.Repo != nil {
		set := repoFieldSet(repoCfg.Repo)
		for _, f := range p.Repo.Locked {
			if _, has := set[f]; has {
				v = append(v, PolicyViolation{
					Field:     "repository." + f,
					Message:   fmt.Sprintf("Field %q is locked by org policy and cannot be overridden", f),
					Severity:  SevError,
					OrgPolicy: "repo.locked",
				})
			}
		}
	}

	// Repo allowed overrides
	if p.Repo != nil && len(p.Repo.AllowedOverrides) > 0 && repoCfg != nil && repoCfg.Repo != nil {
		allowed := toSet(p.Repo.AllowedOverrides)
		locked := toSet(p.Repo.Locked)
		for f := range repoFieldSet(repoCfg.Repo) {
			if _, ok := allowed[f]; !ok {
				if _, isLocked := locked[f]; isLocked {
					continue // already reported
				}
				v = append(v, PolicyViolation{
					Field:     "repository." + f,
					Message:   fmt.Sprintf("Field %q is not in the allowed overrides list", f),
					Severity:  SevError,
					OrgPolicy: "repo.allowed_overrides",
				})
			}
		}
	}

	// Rulesets
	if p.Rulesets != nil {
		if p.Rulesets.AllowRepoLevel != nil && !*p.Rulesets.AllowRepoLevel &&
			repoCfg != nil && repoCfg.Rulesets != nil {
			v = append(v, PolicyViolation{
				Field:     "rulesets",
				Message:   "Repo-level rulesets are not allowed by org policy",
				Severity:  SevError,
				OrgPolicy: "rulesets.allow_repo_level",
			})
		}
		if p.Rulesets.MinRequiredApprovers != nil && resolved != nil && resolved.Rulesets != nil {
			min := *p.Rulesets.MinRequiredApprovers
			for _, r := range resolved.Rulesets.Rulesets {
				for _, rule := range r.Rules {
					if rule.Type == "pull_request" && rule.Parameters != nil {
						if cnt, ok := readInt(rule.Parameters["required_approving_review_count"]); ok && cnt < min {
							v = append(v, PolicyViolation{
								Field:     fmt.Sprintf("rulesets.%s.rules.pull_request.required_approving_review_count", r.Name),
								Message:   fmt.Sprintf("Required approvers (%d) is below org minimum (%d)", cnt, min),
								Severity:  SevError,
								OrgPolicy: "rulesets.min_required_approvers",
							})
						}
					}
				}
			}
		}
	}

	// Teams
	if p.Teams != nil {
		if p.Teams.AllowRepoLevel != nil && !*p.Teams.AllowRepoLevel &&
			repoCfg != nil && repoCfg.Teams != nil {
			v = append(v, PolicyViolation{
				Field:     "teams",
				Message:   "Repo-level team configuration is not allowed by org policy",
				Severity:  SevError,
				OrgPolicy: "teams.allow_repo_level",
			})
		}
		if p.Teams.MaxPermission != nil && resolved != nil && resolved.Teams != nil {
			maxR := permRank[*p.Teams.MaxPermission]
			for _, t := range resolved.Teams.Teams {
				if permRank[t.Permission] > maxR {
					v = append(v, PolicyViolation{
						Field:     fmt.Sprintf("teams.%s.permission", t.Slug),
						Message:   fmt.Sprintf("Team %q has permission %q which exceeds max %q", t.Slug, t.Permission, *p.Teams.MaxPermission),
						Severity:  SevError,
						OrgPolicy: "teams.max_permission",
					})
				}
			}
		}
		if len(p.Teams.RequiredTeams) > 0 && resolved != nil && resolved.Teams != nil {
			perms := map[string]TeamPermission{}
			for _, t := range resolved.Teams.Teams {
				perms[t.Slug] = t.Permission
			}
			for _, req := range p.Teams.RequiredTeams {
				actual, ok := perms[req.Slug]
				if !ok {
					v = append(v, PolicyViolation{
						Field:     "teams." + req.Slug,
						Message:   fmt.Sprintf("Required team %q is missing", req.Slug),
						Severity:  SevError,
						OrgPolicy: "teams.required_teams",
					})
					continue
				}
				if permRank[actual] < permRank[req.MinPermission] {
					v = append(v, PolicyViolation{
						Field:     fmt.Sprintf("teams.%s.permission", req.Slug),
						Message:   fmt.Sprintf("Required team %q has %q but needs at least %q", req.Slug, actual, req.MinPermission),
						Severity:  SevError,
						OrgPolicy: "teams.required_teams",
					})
				}
			}
		}
		if len(p.Teams.PermissionByPattern) > 0 && resolved != nil && resolved.Teams != nil {
			for _, t := range resolved.Teams.Teams {
				for _, rule := range p.Teams.PermissionByPattern {
					if globMatch(t.Slug, rule.Pattern) {
						if !permIn(t.Permission, rule.AllowedPermissions) {
							allowedStr := make([]string, len(rule.AllowedPermissions))
							for i, p := range rule.AllowedPermissions {
								allowedStr[i] = string(p)
							}
							v = append(v, PolicyViolation{
								Field:     fmt.Sprintf("teams.%s.permission", t.Slug),
								Message:   fmt.Sprintf("Team %q matches pattern %q and has permission %q which is not in [%s]", t.Slug, rule.Pattern, t.Permission, strings.Join(allowedStr, ", ")),
								Severity:  SevError,
								OrgPolicy: "teams.permission_by_pattern",
							})
						}
					}
				}
			}
		}
	}

	// Security locked: repo cannot weaken any security setting
	if p.Security != nil && p.Security.Locked != nil && *p.Security.Locked &&
		repoCfg != nil && repoCfg.Security != nil {
		secMap := boolFieldsFalse(repoCfg.Security)
		for k := range secMap {
			v = append(v, PolicyViolation{
				Field:     "security." + k,
				Message:   fmt.Sprintf("Cannot disable %q — security settings are locked by org policy", k),
				Severity:  SevError,
				OrgPolicy: "security.locked",
			})
		}
	}

	// Actions
	if p.Actions != nil && p.Actions.AllowRepoLevel != nil && !*p.Actions.AllowRepoLevel &&
		repoCfg != nil && repoCfg.Actions != nil {
		v = append(v, PolicyViolation{
			Field:     "actions",
			Message:   "Repo-level Actions configuration is not allowed by org policy",
			Severity:  SevError,
			OrgPolicy: "actions.allow_repo_level",
		})
	}

	// Environments
	if p.Environments != nil {
		if p.Environments.AllowRepoLevel != nil && !*p.Environments.AllowRepoLevel &&
			repoCfg != nil && repoCfg.Environments != nil {
			v = append(v, PolicyViolation{
				Field:     "environments",
				Message:   "Repo-level environment configuration is not allowed by org policy",
				Severity:  SevError,
				OrgPolicy: "environments.allow_repo_level",
			})
		}
		if p.Environments.ProductionRequiresReviewers != nil && *p.Environments.ProductionRequiresReviewers &&
			resolved != nil && resolved.Environments != nil {
			min := 1
			if p.Environments.ProductionMinReviewers != nil {
				min = *p.Environments.ProductionMinReviewers
			}
			for _, e := range resolved.Environments.Environments {
				if strings.EqualFold(e.Name, "production") {
					if len(e.Reviewers) < min {
						v = append(v, PolicyViolation{
							Field:     "environments.production.reviewers",
							Message:   fmt.Sprintf("Production environment requires at least %d reviewer(s), but has %d", min, len(e.Reviewers)),
							Severity:  SevError,
							OrgPolicy: "environments.production_requires_reviewers",
						})
					}
				}
			}
		}
	}

	return v
}

// repoFieldSet returns the set of repo fields the operator explicitly set
// (i.e., non-nil pointers in the typed struct).
func repoFieldSet(r *RepoConfig) map[string]struct{} {
	out := map[string]struct{}{}
	if r == nil {
		return out
	}
	rv := reflect.ValueOf(r).Elem()
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if f.Kind() == reflect.Ptr && !f.IsNil() {
			tag := rt.Field(i).Tag.Get("yaml")
			if tag == "" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name == "-" {
				continue
			}
			out[name] = struct{}{}
		}
	}
	return out
}

// boolFieldsFalse returns the YAML names of bool pointers that are
// explicitly false. Used by security-locked policy: repos cannot disable
// any security setting if locked.
func boolFieldsFalse(s *SecurityConfig) map[string]struct{} {
	out := map[string]struct{}{}
	if s == nil {
		return out
	}
	rv := reflect.ValueOf(s).Elem()
	rt := rv.Type()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Field(i)
		if f.Kind() == reflect.Ptr && !f.IsNil() && f.Elem().Kind() == reflect.Bool && !f.Elem().Bool() {
			tag := rt.Field(i).Tag.Get("yaml")
			name := strings.Split(tag, ",")[0]
			out[name] = struct{}{}
		}
	}
	return out
}

func toSet(items []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, s := range items {
		out[s] = struct{}{}
	}
	return out
}

func readInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func permIn(p TeamPermission, allowed []TeamPermission) bool {
	for _, a := range allowed {
		if p == a {
			return true
		}
	}
	return false
}
