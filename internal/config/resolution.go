package config

// Resolution is everything one reconcile needs to know about a
// repository's desired state: what the layers agreed on, what the
// repository itself asked for, and whether org policy permits it.
//
// RepoLayer is kept separate from Settings because policy validation has
// to distinguish a value the repository is trying to set from one it
// merely inherited. Locking `repository.visibility` at the org level has
// to reject a repo that overrides it while leaving every repo that
// inherits it alone, and after the merge both look identical.
type Resolution struct {
	// Settings is the org -> suborg -> repo merge that the appliers act on.
	Settings *Settings
	// RepoLayer is the repository's own configuration, pre-merge.
	RepoLayer *Settings
	// Policy is the org admin policy the resolution was checked against.
	// The zero value means no policy is configured.
	Policy AdminPolicy
	// Provenance records which layer supplied each resource.
	Provenance Provenance
	// Violations is every policy breach found, error and warning alike.
	Violations []PolicyViolation
}

// RepoOnly wraps repository-level settings with no org layer and no
// policy. It is the resolution used by deployments that have not
// configured an admin repository, and by callers that already hold
// settings from somewhere else.
func RepoOnly(s *Settings) *Resolution {
	if s == nil {
		s = &Settings{}
	}
	return &Resolution{Settings: s, RepoLayer: s}
}

// Blocking returns the violations that must stop an apply. Warnings are
// reported but do not block: they exist so an org can announce a policy
// before enforcing it.
func (r *Resolution) Blocking() []PolicyViolation {
	if r == nil {
		return nil
	}
	var out []PolicyViolation
	for _, v := range r.Violations {
		if v.Severity == SevError {
			out = append(out, v)
		}
	}
	return out
}

// Desired returns the merged settings, tolerating a nil Resolution so
// callers do not have to nil-check before every field access.
func (r *Resolution) Desired() *Settings {
	if r == nil || r.Settings == nil {
		return &Settings{}
	}
	return r.Settings
}

// Filtering narrows the desired settings to keys, leaving everything
// else about the resolution untouched. Selective reconcile decides which
// resources to apply; it must not decide which policy violations count.
// A repository that overrides a locked field stays blocked even when the
// push that triggered the run only touched an unrelated file.
func (r *Resolution) Filtering(keys map[string]struct{}) *Resolution {
	if r == nil || keys == nil {
		return r
	}
	out := *r
	out.Settings = Filter(r.Settings, keys)
	return &out
}

// ResolveFor layers an admin configuration over a repository's own and
// validates the result against the org policy. admin may be nil, in
// which case the repository layer stands alone and no policy applies.
//
// rc supplies the attributes suborg matching tests against. A suborg
// whose match criteria reference data rc does not carry simply does not
// match; see SuborgMatch.Matches.
func ResolveFor(admin *AdminLayer, repoLayer *Settings, rc RepoContext) *Resolution {
	if repoLayer == nil {
		repoLayer = &Settings{}
	}
	if admin == nil {
		res := RepoOnly(repoLayer)
		_, res.Provenance = Resolve(Layer{Source: "repo", Settings: repoLayer})
		return res
	}

	layers := make([]Layer, 0, 2+len(admin.Suborgs))
	if admin.Settings != nil {
		layers = append(layers, Layer{Source: "org", Settings: admin.Settings})
	}
	for _, so := range admin.Suborgs {
		if so.Settings == nil || !so.Match.Matches(rc) {
			continue
		}
		layers = append(layers, Layer{Source: "suborg:" + so.Name, Settings: so.Settings})
	}
	layers = append(layers, Layer{Source: "repo", Settings: repoLayer})

	merged, prov := Resolve(layers...)
	return &Resolution{
		Settings:   merged,
		RepoLayer:  repoLayer,
		Policy:     admin.Policy,
		Provenance: prov,
		Violations: ValidatePolicy(merged, repoLayer, admin.Policy),
	}
}
