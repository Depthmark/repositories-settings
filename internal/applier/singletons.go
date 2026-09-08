package applier

import (
	"context"
	"net/http"
	"sort"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Actions settings span three endpoints.
//
// .../actions/permissions carries only `enabled` and `allowed_actions`.
// The allow-list lives on .../permissions/selected-actions and the
// default GITHUB_TOKEN permissions on .../permissions/workflow. Sending
// everything to the first endpoint left five settings — including
// `default_workflow_permissions`, which decides whether GITHUB_TOKEN is
// read-only by default — silently unapplied.
func NewActionsLane(cfg *config.ActionsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "actions",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				return readActions(ctx, cl, repo, cfg)
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				return Updated, writeActions(ctx, cl, repo, cfg, d)
			}
			return singleResourceRun(ctx, "actions", fetch, desired, mutate, dryRun)
		},
	}
}

// readActions gathers live state from whichever endpoints the
// configuration actually engages, so unmanaged settings cost no call.
func readActions(ctx context.Context, cl *ghclient.Client, repo config.Repo, cfg *config.ActionsConfig) (map[string]any, error) {
	live := map[string]any{}

	if cfg.Enabled != nil || cfg.AllowedActions != nil {
		perms, _, err := cl.GH().Repositories.GetActionsPermissions(
			readCtx(ctx, repo, ghapi.RouteActionsPermissions), repo.Owner, repo.Name)
		if err != nil {
			return nil, err
		}
		for k, v := range ghapi.DecodeActionsPermissions(perms) {
			live[k] = v
		}
	}

	if cfg.GithubOwnedAllowed != nil || cfg.VerifiedAllowed != nil || cfg.PatternsAllowed != nil {
		allowed, _, err := cl.GH().Repositories.GetActionsAllowed(
			readCtx(ctx, repo, ghapi.RouteActionsSelectedActions), repo.Owner, repo.Name)
		if err != nil {
			// GitHub answers 409 while allowed_actions is not yet
			// "selected". That is the create case, not a failure: the
			// allow-list simply does not exist until the mode is set.
			if !isNotFound(err) && !isConflict(err) {
				return nil, err
			}
		} else {
			for k, v := range ghapi.DecodeActionsAllowed(allowed) {
				live[k] = v
			}
		}
	}

	if cfg.DefaultWorkflowPermissions != nil || cfg.CanApprovePullRequestReviews != nil {
		wf, _, err := cl.GH().Repositories.GetDefaultWorkflowPermissions(
			readCtx(ctx, repo, ghapi.RouteActionsWorkflowDefaults), repo.Owner, repo.Name)
		if err != nil {
			return nil, err
		}
		for k, v := range ghapi.DecodeWorkflowDefaults(wf) {
			live[k] = v
		}
	}
	return live, nil
}

// writeActions dispatches each changed field to the endpoint that owns
// it. Order matters: the allow-list can only be set once allowed_actions
// is "selected", so the permissions call goes first.
func writeActions(ctx context.Context, cl *ghclient.Client, repo config.Repo, cfg *config.ActionsConfig, d diff.Diff) error {
	var base, allowList, workflow bool
	for _, c := range d.Changes {
		switch {
		case ghapi.ActionsAllowListFields[c.Path]:
			allowList = true
		case ghapi.ActionsWorkflowFields[c.Path]:
			workflow = true
		default:
			base = true
		}
	}

	if base {
		if _, _, err := cl.GH().Repositories.UpdateActionsPermissions(
			writeCtx(ctx, repo, ghapi.RouteActionsPermissions), repo.Owner, repo.Name,
			*ghapi.EncodeActionsPermissions(*cfg)); err != nil {
			return err
		}
	}
	if allowList {
		if _, _, err := cl.GH().Repositories.EditActionsAllowed(
			writeCtx(ctx, repo, ghapi.RouteActionsSelectedActions), repo.Owner, repo.Name,
			ghapi.EncodeActionsAllowed(*cfg)); err != nil {
			return err
		}
	}
	if workflow {
		if _, _, err := cl.GH().Repositories.UpdateDefaultWorkflowPermissions(
			writeCtx(ctx, repo, ghapi.RouteActionsWorkflowDefaults), repo.Owner, repo.Name,
			ghapi.EncodeWorkflowDefaults(*cfg)); err != nil {
			return err
		}
	}
	return nil
}

// Security spans four endpoints, not one.
//
// Only secret_scanning and secret_scanning_push_protection live in the
// repository's security_and_analysis block. Vulnerability alerts,
// automated security fixes and private vulnerability reporting each have
// their own endpoint. Writing all five into security_and_analysis — as
// this lane used to — meant GitHub silently ignored three of them, while
// the live read never reported them back, so they showed as a pending
// change on every single reconcile and never actually applied.
func NewSecurityLane(cfg *config.SecurityConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "security",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				return readSecurity(ctx, cl, repo, cfg)
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				return Updated, writeSecurity(ctx, cl, repo, d)
			}
			return singleResourceRun(ctx, "security", fetch, desired, mutate, dryRun)
		},
	}
}

// readSecurity gathers live state from every endpoint the configuration
// actually engages, so an unmanaged setting costs no extra call.
func readSecurity(ctx context.Context, cl *ghclient.Client, repo config.Repo, cfg *config.SecurityConfig) (map[string]any, error) {
	live := map[string]any{}

	if cfg.SecretScanning != nil || cfg.SecretScanningPushProtection != nil {
		r, _, err := cl.GH().Repositories.Get(readCtx(ctx, repo, ghapi.RouteRepo), repo.Owner, repo.Name)
		if err != nil {
			return nil, err
		}
		for k, v := range ghapi.DecodeSecurity(r) {
			live[k] = v
		}
	}
	if cfg.VulnerabilityAlerts != nil {
		on, _, err := cl.GH().Repositories.GetVulnerabilityAlerts(
			readCtx(ctx, repo, ghapi.RouteVulnerabilityAlerts), repo.Owner, repo.Name)
		if err != nil {
			return nil, err
		}
		live["vulnerability_alerts"] = on
	}
	if cfg.AutomatedSecurityFixes != nil {
		fixes, _, err := cl.GH().Repositories.GetAutomatedSecurityFixes(
			readCtx(ctx, repo, ghapi.RouteAutomatedSecurityFixes), repo.Owner, repo.Name)
		if err != nil {
			return nil, err
		}
		live["automated_security_fixes"] = fixes.GetEnabled()
	}
	if cfg.PrivateVulnerabilityReporting != nil {
		// go-github can enable and disable this but has no getter, so
		// it goes through the raw client. Without a read there would be
		// nothing to diff against, and the setting would be rewritten
		// on every run.
		var state struct {
			Enabled bool `json:"enabled"`
		}
		_, err := cl.Do(ctx, ghclient.Call{
			Prio: ghclient.PriorityCronReconcile, Owner: repo.Owner, Method: http.MethodGet,
			Path: ghapi.PrivateVulnerabilityReporting(repo), Route: ghapi.RoutePrivateVulnReporting,
			Out: &state,
		})
		if err != nil {
			return nil, err
		}
		live["private_vulnerability_reporting"] = state.Enabled
	}
	return live, nil
}

// writeSecurity dispatches each changed field to the endpoint that owns
// it, batching the two security_and_analysis toggles into one PATCH.
func writeSecurity(ctx context.Context, cl *ghclient.Client, repo config.Repo, d diff.Diff) error {
	analysis := map[string]bool{}
	for _, c := range d.Changes {
		on, _ := c.To.(bool)
		if ghapi.SecurityAndAnalysisFields[c.Path] {
			analysis[c.Path] = on
			continue
		}
		var err error
		switch c.Path {
		case "vulnerability_alerts":
			wctx := writeCtx(ctx, repo, ghapi.RouteVulnerabilityAlerts)
			if on {
				_, err = cl.GH().Repositories.EnableVulnerabilityAlerts(wctx, repo.Owner, repo.Name)
			} else {
				_, err = cl.GH().Repositories.DisableVulnerabilityAlerts(wctx, repo.Owner, repo.Name)
			}
		case "automated_security_fixes":
			wctx := writeCtx(ctx, repo, ghapi.RouteAutomatedSecurityFixes)
			if on {
				_, err = cl.GH().Repositories.EnableAutomatedSecurityFixes(wctx, repo.Owner, repo.Name)
			} else {
				_, err = cl.GH().Repositories.DisableAutomatedSecurityFixes(wctx, repo.Owner, repo.Name)
			}
		case "private_vulnerability_reporting":
			wctx := writeCtx(ctx, repo, ghapi.RoutePrivateVulnReporting)
			if on {
				_, err = cl.GH().Repositories.EnablePrivateReporting(wctx, repo.Owner, repo.Name)
			} else {
				_, err = cl.GH().Repositories.DisablePrivateReporting(wctx, repo.Owner, repo.Name)
			}
		}
		if err != nil {
			return err
		}
	}
	if len(analysis) > 0 {
		_, _, err := cl.GH().Repositories.Edit(
			writeCtx(ctx, repo, ghapi.RouteRepo), repo.Owner, repo.Name, ghapi.EncodeSecurity(analysis))
		return err
	}
	return nil
}

func NewPagesLane(cfg *config.PagesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "pages",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				pages, _, err := cl.GH().Repositories.GetPagesInfo(
					readCtx(ctx, repo, ghapi.RoutePages), repo.Owner, repo.Name)
				if err != nil {
					if isNotFound(err) {
						return nil, nil // Pages not enabled yet
					}
					return nil, err
				}
				return ghapi.DecodePages(pages), nil
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				wctx := writeCtx(ctx, repo, ghapi.RoutePages)
				if d.Action == diff.Create {
					_, _, err := cl.GH().Repositories.EnablePages(wctx, repo.Owner, repo.Name, ghapi.EncodePagesCreate(*cfg))
					return Created, err
				}
				_, err := cl.GH().Repositories.UpdatePages(wctx, repo.Owner, repo.Name, ghapi.EncodePagesUpdate(*cfg))
				return Updated, err
			}
			return singleResourceRun(ctx, "pages", fetch, desired, mutate, dryRun)
		},
	}
}

func NewCustomPropertiesLane(cfg *config.CustomPropertiesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "custom_properties",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				values, _, err := cl.GH().Repositories.GetAllCustomPropertyValues(
					readCtx(ctx, repo, ghapi.RoutePropertyValues), repo.Owner, repo.Name)
				if err != nil {
					return nil, err
				}
				return ghapi.DecodeCustomProperties(values), nil
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				// Sorted so the request body — and any recording of it in
				// a test or an audit log — is deterministic.
				names := make([]string, 0, len(cfg.Properties))
				for k := range cfg.Properties {
					names = append(names, k)
				}
				sort.Strings(names)
				_, err := cl.GH().Repositories.CreateOrUpdateCustomProperties(
					writeCtx(ctx, repo, ghapi.RoutePropertyValues), repo.Owner, repo.Name,
					ghapi.EncodeCustomProperties(cfg.Properties, names))
				return Updated, err
			}
			return singleResourceRun(ctx, "custom_properties", fetch, cfg.Properties, mutate, dryRun)
		},
	}
}
