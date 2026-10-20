package ghapi

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
)

var repo = config.Repo{Owner: "acme", Name: "svc"}

// AllRoutes is the complete value set of the `route` metric label. Its
// whole purpose is to be bounded and enumerable, so anything that looks
// like a concrete id, name or branch in a template is a bug that would
// multiply the time series by the number of distinct values.
func TestAllRoutesAreTemplates(t *testing.T) {
	placeholder := regexp.MustCompile(`\{[a-z_]+\}`)
	seen := map[string]bool{}
	for _, r := range AllRoutes {
		if seen[r] {
			t.Errorf("duplicate route %q", r)
		}
		seen[r] = true

		if !strings.HasPrefix(r, "/") {
			t.Errorf("route %q must be a path", r)
		}
		for _, segment := range strings.Split(strings.TrimPrefix(r, "/"), "/") {
			if strings.ContainsAny(segment, "{}") && !placeholder.MatchString(segment) {
				t.Errorf("route %q has a malformed placeholder in %q", r, segment)
			}
		}
	}
}

// The builders that survive are the ones still on the raw client; each
// must render into its declared template.
func TestRawPathBuildersMatchRoutes(t *testing.T) {
	cases := map[string]string{
		Repo(repo): RouteRepo,
		RepoContents(repo, ".github/settings/repo.yml", "abc123"): RouteRepoContents,
		Rulesets(repo):                      RouteRulesets,
		RulesetsID(repo, 42):                RouteRulesetsID,
		PrivateVulnerabilityReporting(repo): RoutePrivateVulnReporting,
	}
	for path, route := range cases {
		if !templateMatches(route, path) {
			t.Errorf("path %q does not match its route template %q", path, route)
		}
		if !declared(route) {
			t.Errorf("route %q is missing from AllRoutes", route)
		}
	}
}

// A username or environment name must not be able to add path segments.
func TestPathEscaping(t *testing.T) {
	if got := Repo(config.Repo{Owner: "a/b", Name: "c"}); strings.Contains(got, "a/b") {
		t.Errorf("owner must not be able to add path segments, got %q", got)
	}
}

func declared(route string) bool {
	for _, r := range AllRoutes {
		if r == route {
			return true
		}
	}
	return false
}

// templateMatches checks a concrete path against a template by turning
// each {placeholder} into a wildcard.
func templateMatches(template, path string) bool {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	pattern := regexp.QuoteMeta(template)
	pattern = regexp.MustCompile(`\\\{[a-z_]+\\\}`).ReplaceAllString(pattern, `[^/]+`)
	// {path} and {branch} are the placeholders that span separators.
	pattern = strings.ReplaceAll(pattern, `contents/[^/]+`, `contents/.+`)
	pattern = strings.ReplaceAll(pattern, `branches/[^/]+`, `branches/.+`)
	return regexp.MustCompile("^" + pattern + "$").MatchString(path)
}
