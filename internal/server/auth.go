package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/metrics"
)

// Credential kinds, used as the bounded metric label and in logs.
const (
	credOIDC      = "oidc"
	credToken     = "token"
	credAnonymous = "anonymous"
)

// caller is the authenticated identity behind a privileged API request.
//
// Repository is the heart of the OIDC mode. A GitHub Actions token names
// the repository whose workflow minted it, so a caller presenting one may
// only act on that repository. The static service token names nothing, so
// it can act on any repository the App is installed in — which is why it
// is an operator credential, not one to hand to a workflow.
type caller struct {
	Kind       string
	Repository string
	// Subject is the OIDC sub claim, recorded in the access log so an
	// apply can be traced back to a workflow run.
	Subject           string
	Ref               string
	EventName         string
	RepositoryID      string
	RepositoryOwnerID string
}

// scopedTo reports whether this caller may act on repo.
func (c caller) scopedTo(repo config.Repo) bool {
	if c.Repository == "" {
		return true // not a repository-scoped credential
	}
	return strings.EqualFold(c.Repository, repo.String())
}

// authorize authenticates a privileged API request.
//
// Order matters. A JWT is tried against the OIDC verifier first so a
// workflow token is never silently compared against the static secret;
// anything else falls through to the static token. When neither
// credential is configured the request is refused unless the operator
// explicitly opted into the development-only unauthenticated mode.
func authorize(r *http.Request, d Deps) (caller, bool) {
	bearer := bearerToken(r)

	if d.OIDC != nil && bearer != "" && looksLikeJWT(bearer) {
		c, ok := authorizeOIDC(r, d, bearer)
		metrics.AuthorizationsTotal.WithLabelValues(credOIDC, result(ok)).Inc()
		return c, ok
	}

	if d.APIToken != "" {
		ok := bearer != "" && constantTimeEqual(bearer, d.APIToken)
		metrics.AuthorizationsTotal.WithLabelValues(credToken, result(ok)).Inc()
		if !ok {
			return caller{}, false
		}
		return caller{Kind: credToken}, true
	}

	// No credential is configured for this route.
	ok := d.AllowUnauthenticated
	metrics.AuthorizationsTotal.WithLabelValues(credAnonymous, result(ok)).Inc()
	if !ok {
		return caller{}, false
	}
	return caller{Kind: credAnonymous}, true
}

func authorizeOIDC(r *http.Request, d Deps, bearer string) (caller, bool) {
	// A verified token that cannot be tied to a repository authorizes
	// nothing here: an unscoped OIDC caller would be the static token
	// with extra steps. VerifyGitHub refuses that case for us.
	identity, err := d.OIDC.VerifyGitHub(r.Context(), bearer)
	if err != nil {
		d.Logger.Warn("oidc token rejected", "error", err,
			"trace_id", TraceIDFromContext(r.Context()))
		return caller{}, false
	}
	return caller{
		Kind:              credOIDC,
		Repository:        identity.Repository,
		Subject:           identity.Subject,
		Ref:               identity.Ref,
		EventName:         identity.EventName,
		RepositoryID:      identity.RepositoryID,
		RepositoryOwnerID: identity.RepositoryOwnerID,
	}, true
}

// requireApply permits workflow writes only from a trusted event on the
// current default branch. Repository scope alone also includes pull requests
// and feature branches, whose tokens must remain limited to dry runs.
func requireApply(w http.ResponseWriter, r *http.Request, d Deps, c caller, repo config.Repo) bool {
	if c.Kind != credOIDC {
		return true
	}
	switch c.EventName {
	case "push", "workflow_dispatch", "schedule":
	default:
		http.Error(w, "forbidden: this workflow event may only request dry runs", http.StatusForbidden)
		return false
	}
	ctx := ghclient.WithCall(r.Context(), ghclient.PriorityMergeApply, repo.Owner, ghapi.RouteRepo)
	metadata, _, err := d.Client.GH().Repositories.Get(ctx, repo.Owner, repo.Name)
	if err != nil {
		d.Logger.Warn("cannot verify workflow apply scope", "repo", repo.String(), "error", err)
		http.Error(w, "cannot verify repository identity and default branch", http.StatusBadGateway)
		return false
	}
	if metadata.GetDefaultBranch() == "" || c.Ref != "refs/heads/"+metadata.GetDefaultBranch() ||
		c.RepositoryID != strconv.FormatInt(metadata.GetID(), 10) ||
		c.RepositoryOwnerID != strconv.FormatInt(metadata.GetOwner().GetID(), 10) {
		http.Error(w, "forbidden: apply requires the current repository identity and default branch", http.StatusForbidden)
		return false
	}
	return true
}

// requireCaller authenticates the request, answering 401 and returning
// false when it cannot. It runs before the body is read: an unknown
// caller should not reach the JSON decoder, and should not learn from
// the status code whether its payload would have parsed.
func requireCaller(w http.ResponseWriter, r *http.Request, d Deps) (caller, bool) {
	c, ok := authorize(r, d)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return caller{}, false
	}
	return c, true
}

// requireScope confirms an authenticated caller may act on repo,
// answering 403 and returning false when it may not.
//
// The two failures are deliberately distinct: 401 means "we do not know
// who you are", 403 means "we know, and this is not yours". Collapsing
// them would send a workflow chasing its credentials when the real
// problem is that it named someone else's repository.
func requireScope(w http.ResponseWriter, r *http.Request, d Deps, c caller, repo config.Repo) bool {
	if c.scopedTo(repo) {
		return true
	}
	d.Logger.Warn("caller is not scoped to the requested repository",
		"credential", c.Kind, "scope", c.Repository, "requested", repo.String(),
		"trace_id", TraceIDFromContext(r.Context()))
	metrics.AuthorizationsTotal.WithLabelValues(c.Kind, "wrong_repository").Inc()
	http.Error(w, "forbidden: this credential is scoped to "+c.Repository, http.StatusForbidden)
	return false
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

// looksLikeJWT is a routing hint, not a validation step: it decides
// which verifier sees the credential, and both of them reject anything
// they do not accept.
func looksLikeJWT(s string) bool {
	return strings.Count(s, ".") == 2
}

// constantTimeEqual compares through a digest so the comparison is
// independent of both content and length.
func constantTimeEqual(got, want string) bool {
	g := sha256.Sum256([]byte(got))
	w := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

func result(ok bool) string {
	if ok {
		return "allowed"
	}
	return "denied"
}
