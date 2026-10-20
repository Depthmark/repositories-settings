package oidc

import (
	"fmt"
	"slices"
	"strings"
)

// GitHubActionsIssuer is the issuer for GitHub.com Actions. GitHub
// Enterprise Server deployments use their own issuer and do not offer
// GitHub.com's immutable subject format.
const GitHubActionsIssuer = "https://token.actions.githubusercontent.com"

// GitHubIdentity is the repository a verified Actions token speaks for.
type GitHubIdentity struct {
	Issuer string
	// Subject is the raw sub claim, kept for audit logging.
	Subject string
	// Repository is "owner/name" — the only thing authorization needs.
	Repository        string
	RepositoryOwner   string
	RepositoryName    string
	RepositoryID      string
	RepositoryOwnerID string
	// ImmutableSubject is true when the subject carried numeric IDs and
	// they matched the signed ID claims.
	ImmutableSubject bool
	// Ref and EventName distinguish default-branch runs from untrusted
	// branch and pull-request runs at the API authorization boundary.
	Ref       string
	EventName string
	Workflow  string
	RunID     string
}

// GitHubIdentityError names why a token's identity claims were rejected,
// without echoing claim values into an HTTP response.
type GitHubIdentityError struct {
	Reason string
	Claim  string
}

func (e *GitHubIdentityError) Error() string {
	if e.Claim == "" {
		return "GitHub identity rejected: " + e.Reason
	}
	return fmt.Sprintf("GitHub identity rejected: %s (%s)", e.Reason, e.Claim)
}

func identityError(reason, claim string) error {
	return &GitHubIdentityError{Reason: reason, Claim: claim}
}

// ParseGitHubIdentity extracts and cross-checks the repository identity
// in a verified GitHub Actions token. It returns (nil, nil) for an
// issuer outside githubIssuers, so a deployment that trusts several
// issuers can ask without branching first. A nil githubIssuers means
// GitHub.com.
//
// The repository is established from three independent signed claims —
// repository, repository_owner and the subject — and all three have to
// agree. Any one of them alone has been a source of confusion attacks in
// OIDC deployments: subject formats are templated per-repository and can
// be made to contain a `repo:` segment naming somebody else.
//
// requireImmutableSubject controls only whether the subject must carry
// the numeric IDs. The signed repository_id and repository_owner_id
// claims are required either way.
func ParseGitHubIdentity(claims Claims, githubIssuers []string, requireImmutableSubject bool) (*GitHubIdentity, error) {
	if len(githubIssuers) == 0 {
		githubIssuers = []string{GitHubActionsIssuer}
	}
	issuer, _ := claims["iss"].(string)
	if !slices.Contains(githubIssuers, issuer) {
		return nil, nil
	}

	subject, err := requiredClaim(claims, "sub")
	if err != nil {
		return nil, err
	}
	repository, err := requiredClaim(claims, "repository")
	if err != nil {
		return nil, err
	}
	owner, err := requiredClaim(claims, "repository_owner")
	if err != nil {
		return nil, err
	}
	repositoryID, err := requiredClaim(claims, "repository_id")
	if err != nil {
		return nil, err
	}
	ownerID, err := requiredClaim(claims, "repository_owner_id")
	if err != nil {
		return nil, err
	}
	if !numericID(repositoryID) {
		return nil, identityError("claim is not a positive integer", "repository_id")
	}
	if !numericID(ownerID) {
		return nil, identityError("claim is not a positive integer", "repository_owner_id")
	}

	claimOwner, claimName, ok := splitRepository(repository)
	if !ok {
		return nil, identityError("claim is not owner/name", "repository")
	}
	if !strings.EqualFold(claimOwner, owner) {
		return nil, identityError("does not agree with the repository claim", "repository_owner")
	}

	segments := subjectRepoSegments(subject)
	if len(segments) == 0 {
		return nil, identityError("carries no repo: segment", "sub")
	}

	// A subject template can legitimately produce more than one repo:
	// segment. Exactly one must resolve to this repository: zero means
	// the subject describes something else, and more than one means the
	// subject is ambiguous about who it speaks for.
	var matches []bool
	sawParseable, sawLegacyMatch := false, false
	for _, seg := range segments {
		segOwner, segOwnerID, segName, segRepoID, immutable, ok := splitSubjectRepo(seg)
		if !ok {
			continue
		}
		sawParseable = true
		if !strings.EqualFold(segOwner, claimOwner) || !strings.EqualFold(segName, claimName) {
			continue
		}
		if immutable {
			if segOwnerID != ownerID || segRepoID != repositoryID {
				continue
			}
		} else if requireImmutableSubject {
			sawLegacyMatch = true
			continue
		}
		matches = append(matches, immutable)
	}

	switch {
	case len(matches) > 1:
		return nil, identityError("names this repository more than once", "sub")
	case len(matches) == 0 && sawLegacyMatch:
		return nil, identityError("does not carry the immutable owner and repository IDs", "sub")
	case len(matches) == 0 && sawParseable:
		return nil, identityError("does not agree with the repository claim", "sub")
	case len(matches) == 0:
		return nil, identityError("has no usable repo: segment", "sub")
	}

	id := &GitHubIdentity{
		Issuer:            issuer,
		Subject:           subject,
		Repository:        claimOwner + "/" + claimName,
		RepositoryOwner:   owner,
		RepositoryName:    claimName,
		RepositoryID:      repositoryID,
		RepositoryOwnerID: ownerID,
		ImmutableSubject:  matches[0],
	}
	id.Ref, _ = claims["ref"].(string)
	id.EventName, _ = claims["event_name"].(string)
	id.Workflow, _ = claims["workflow"].(string)
	id.RunID, _ = claims["run_id"].(string)
	return id, nil
}

// subjectRepoSegments returns the value following each "repo:" in the
// subject. GitHub's default subject starts with one; a custom template
// can place one anywhere.
func subjectRepoSegments(subject string) []string {
	var out []string
	if strings.HasPrefix(subject, "repo:") {
		out = append(out, segmentValue(subject, len("repo:")))
	}
	for offset := 0; offset < len(subject); {
		i := strings.Index(subject[offset:], ":repo:")
		if i < 0 {
			break
		}
		i += offset
		out = append(out, segmentValue(subject, i+len(":repo:")))
		// Advance a single byte so an adjacent ":repo:repo:..." is seen
		// twice: "repo" is a legal value in a custom template.
		offset = i + 1
	}
	return out
}

func segmentValue(subject string, start int) string {
	if end := strings.IndexByte(subject[start:], ':'); end >= 0 {
		return subject[start : start+end]
	}
	return subject[start:]
}

func requiredClaim(claims Claims, name string) (string, error) {
	v, ok := claims[name]
	if !ok {
		return "", identityError("claim is missing", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", identityError("claim is not a string", name)
	}
	if s == "" {
		return "", identityError("claim is empty", name)
	}
	return s, nil
}

func splitRepository(repository string) (owner, name string, ok bool) {
	if strings.Count(repository, "/") != 1 {
		return "", "", false
	}
	owner, name, _ = strings.Cut(repository, "/")
	if owner == "" || name == "" {
		return "", "", false
	}
	return owner, name, true
}

// splitSubjectRepo parses one repo: segment, in either the legacy
// "owner/name" form or the immutable "owner@id/name@id" form.
func splitSubjectRepo(segment string) (owner, ownerID, name, repoID string, immutable, ok bool) {
	ownerPart, namePart, valid := splitRepository(segment)
	if !valid {
		return "", "", "", "", false, false
	}
	ownerAts := strings.Count(ownerPart, "@")
	nameAts := strings.Count(namePart, "@")
	if ownerAts == 0 && nameAts == 0 {
		return ownerPart, "", namePart, "", false, true
	}
	if ownerAts != 1 || nameAts != 1 {
		return "", "", "", "", false, false
	}
	owner, ownerID, _ = strings.Cut(ownerPart, "@")
	name, repoID, _ = strings.Cut(namePart, "@")
	if owner == "" || name == "" || !numericID(ownerID) || !numericID(repoID) {
		return "", "", "", "", false, false
	}
	return owner, ownerID, name, repoID, true, true
}

// numericID accepts a positive decimal integer with no sign, spaces, or
// leading-zero-only value.
func numericID(id string) bool {
	if id == "" {
		return false
	}
	nonZero := false
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
		if c != '0' {
			nonZero = true
		}
	}
	return nonZero
}
