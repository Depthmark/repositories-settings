// Package ghapi is the typed boundary between the appliers and the
// GitHub REST API. It owns two things the appliers should not:
//
//   - Route templates. Every path a lane can call has a low-cardinality
//     template ("/repos/{owner}/{repo}/hooks/{hook_id}") that travels
//     with the request and becomes the `route` metric label. Deriving
//     that label from the concrete URL is guesswork — hook IDs, branch
//     patterns, environment names and variable names are all path
//     segments — and guessing wrong multiplies the time series by the
//     number of distinct values.
//
//   - Request bodies. The desired-state document carries exactly what
//     the operator manages; a GitHub request body additionally has to
//     satisfy each endpoint's required-field rules. Those are different
//     documents, and conflating them is what produced the branch
//     protection, rulesets and webhook 422s. Encoding lives here, in
//     one testable function per endpoint, instead of being open-coded
//     inside each lane's mutate closure.
package ghapi

import (
	"fmt"
	"net/url"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// Route templates. These are the complete set of `route` label values;
// anything not listed here is bucketed as ghclient.RouteOther.
const (
	RouteRepo         = "/repos/{owner}/{repo}"
	RouteRepoTopics   = "/repos/{owner}/{repo}/topics"
	RouteRepoContents = "/repos/{owner}/{repo}/contents/{path}"

	RouteRepoTeams   = "/repos/{owner}/{repo}/teams"
	RouteOrgTeamRepo = "/orgs/{org}/teams/{team_slug}/repos/{owner}/{repo}"

	RouteRulesets   = "/repos/{owner}/{repo}/rulesets"
	RouteRulesetsID = "/repos/{owner}/{repo}/rulesets/{ruleset_id}"

	RouteEnvironments     = "/repos/{owner}/{repo}/environments"
	RouteEnvironmentsName = "/repos/{owner}/{repo}/environments/{environment_name}"

	RouteHooks   = "/repos/{owner}/{repo}/hooks"
	RouteHooksID = "/repos/{owner}/{repo}/hooks/{hook_id}"

	RouteAutolinks   = "/repos/{owner}/{repo}/autolinks"
	RouteAutolinksID = "/repos/{owner}/{repo}/autolinks/{autolink_id}"

	RouteActionsPermissions      = "/repos/{owner}/{repo}/actions/permissions"
	RouteActionsSelectedActions  = "/repos/{owner}/{repo}/actions/permissions/selected-actions"
	RouteActionsWorkflowDefaults = "/repos/{owner}/{repo}/actions/permissions/workflow"

	RoutePages = "/repos/{owner}/{repo}/pages"

	RouteVulnerabilityAlerts    = "/repos/{owner}/{repo}/vulnerability-alerts"
	RouteAutomatedSecurityFixes = "/repos/{owner}/{repo}/automated-security-fixes"
	RoutePrivateVulnReporting   = "/repos/{owner}/{repo}/private-vulnerability-reporting"

	RouteSecrets     = "/repos/{owner}/{repo}/actions/secrets"
	RouteSecretsName = "/repos/{owner}/{repo}/actions/secrets/{secret_name}"

	RouteVariables     = "/repos/{owner}/{repo}/actions/variables"
	RouteVariablesName = "/repos/{owner}/{repo}/actions/variables/{name}"

	RouteKeys   = "/repos/{owner}/{repo}/keys"
	RouteKeysID = "/repos/{owner}/{repo}/keys/{key_id}"

	RoutePropertyValues = "/repos/{owner}/{repo}/properties/values"

	RouteCollaborators     = "/repos/{owner}/{repo}/collaborators"
	RouteCollaboratorsUser = "/repos/{owner}/{repo}/collaborators/{username}"

	RouteBranchProtection = "/repos/{owner}/{repo}/branches/{branch}/protection"
	RouteBranchSignatures = "/repos/{owner}/{repo}/branches/{branch}/protection/required_signatures"

	RouteIssueComments   = "/repos/{owner}/{repo}/issues/{issue_number}/comments"
	RouteIssueCommentsID = "/repos/{owner}/{repo}/issues/comments/{comment_id}"

	RouteGraphQL = "/graphql"
)

// AllRoutes is the source of truth for the `route` label's value set.
// Tests assert that every path builder below renders a template that
// appears here, so a new endpoint cannot quietly widen the label.
var AllRoutes = []string{
	RouteRepo, RouteRepoTopics, RouteRepoContents,
	RouteRepoTeams, RouteOrgTeamRepo,
	RouteRulesets, RouteRulesetsID,
	RouteEnvironments, RouteEnvironmentsName,
	RouteHooks, RouteHooksID,
	RouteAutolinks, RouteAutolinksID,
	RouteActionsPermissions, RouteActionsSelectedActions, RouteActionsWorkflowDefaults,
	RoutePages,
	RouteVulnerabilityAlerts, RouteAutomatedSecurityFixes, RoutePrivateVulnReporting,
	RouteSecrets, RouteSecretsName,
	RouteVariables, RouteVariablesName,
	RouteKeys, RouteKeysID,
	RoutePropertyValues,
	RouteCollaborators, RouteCollaboratorsUser,
	RouteBranchProtection, RouteBranchSignatures,
	RouteIssueComments, RouteIssueCommentsID,
	RouteGraphQL,
}

// Path builders exist only for the endpoints still on the raw client:
// rulesets, private vulnerability reporting, and repository contents.
// Everywhere else go-github builds the URL, and the constants above
// serve purely as metric labels.
//
// Segments that can carry user-supplied text are escaped — an
// environment name with a space or a username containing a slash would
// otherwise produce a malformed or, worse, a differently-targeted URL.

func Repo(r config.Repo) string { return "/repos/" + seg(r.Owner) + "/" + seg(r.Name) }

func RepoContents(r config.Repo, path, ref string) string {
	p := Repo(r) + "/contents/" + path
	if ref != "" {
		p += "?ref=" + url.QueryEscape(ref)
	}
	return p
}

func Rulesets(r config.Repo) string           { return Repo(r) + "/rulesets" }
func RulesetsID(r config.Repo, id int) string { return fmt.Sprintf("%s/rulesets/%d", Repo(r), id) }

// PrivateVulnerabilityReporting is read through the raw client:
// go-github can enable and disable it but ships no getter.
func PrivateVulnerabilityReporting(r config.Repo) string {
	return Repo(r) + "/private-vulnerability-reporting"
}

// seg escapes one path segment.
func seg(s string) string { return url.PathEscape(s) }
