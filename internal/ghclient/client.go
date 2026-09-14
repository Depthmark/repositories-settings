// Package ghclient is the GitHub HTTP client. It provides:
//
//   - App authentication and installation-token caching.
//   - A two-pool, priority-queued, bounded-concurrency rate limiter.
//   - Conditional GETs via http.RoundTripper middleware (replaces the
//     stand-alone ETag cache used in the TS code).
//   - Link-header pagination + GraphQL helpers.
//
// Construct a Client at startup; call DoREST and DoGraphQL from appliers.
package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
)

// Client owns the GitHub connection: the transport chain (rate limit →
// conditional GET → auth → metrics), a go-github client mounted on top
// of it, and the per-repo lock.
//
// Callers reach GitHub through GH(), the go-github client. Every request
// it makes inherits the transport chain, so the SDK gets the two-pool
// rate limiter, the ETag cache and installation-token auth without
// knowing they exist. Annotate the context with WithCall first so those
// layers know the priority, the owner and the route template.
type Client struct {
	auth     *AppAuth
	apiURL   string
	limiter  *RateLimiter
	cache    *etagCache
	core     *http.Client
	gh       *github.Client
	logger   *slog.Logger
	repoLock *RepoLock
}

// Config holds construction parameters for Client.
type Config struct {
	Auth       *AppAuth
	APIURL     string
	Limiter    *RateLimiter
	HTTPClient *http.Client
	Logger     *slog.Logger
	CacheCap   int
}

func New(cfg Config) *Client {
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = defaultAPIURL
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}
	base := httpc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	cache := newETagCache(cfg.CacheCap)

	// Outermost first. Auth sits below the cache so a cached body costs
	// nothing in token minting, and above metrics so the counter sees
	// the request that actually went out.
	chain := &rateLimitTransport{
		rl: cfg.Limiter,
		next: newConditionalGetTransport(
			&authTransport{
				auth: cfg.Auth,
				next: &metricsTransport{next: base},
			},
			cache,
		),
	}
	httpc = &http.Client{Transport: chain, Timeout: httpc.Timeout}

	c := &Client{
		auth:     cfg.Auth,
		apiURL:   strings.TrimRight(apiURL, "/"),
		limiter:  cfg.Limiter,
		cache:    cache,
		core:     httpc,
		logger:   cfg.Logger,
		repoLock: NewRepoLock(),
	}
	c.gh = newGitHubClient(httpc, c.apiURL)
	return c
}

const defaultAPIURL = "https://api.github.com"

// newGitHubClient mounts go-github on our transport chain.
//
// The base URL is taken literally from GITHUB_API_URL rather than going
// through WithEnterpriseURLs, which derives the API root by appending
// /api/v3/ to a server root. Deriving it would make the SDK and the raw
// client disagree about where the API lives — the raw path just
// concatenates GITHUB_API_URL — and two different notions of the base
// URL in one process is a debugging problem nobody should inherit.
// Enterprise operators point GITHUB_API_URL at the full API root, the
// same value the raw path already expects.
func newGitHubClient(httpc *http.Client, apiURL string) *github.Client {
	gh := github.NewClient(httpc)
	// go-github resolves paths against BaseURL, which must end in a slash.
	base, err := url.Parse(strings.TrimRight(apiURL, "/") + "/")
	if err != nil {
		// Only a malformed GITHUB_API_URL reaches here; failing calls
		// against the default host is a better outcome at startup than
		// a nil client.
		return gh
	}
	gh.BaseURL = base
	gh.UploadURL = base
	return gh
}

// GH returns the go-github client. Annotate ctx with WithCall before
// using it so the transport chain can schedule, authenticate and label
// the request.
func (c *Client) GH() *github.Client { return c.gh }

// RepoLock returns the per-repo serializer.
func (c *Client) RepoLock() *RepoLock { return c.repoLock }

// Limiter returns the rate limiter (mostly for tests / metrics handlers).
func (c *Client) Limiter() *RateLimiter { return c.limiter }

// Call describes one REST request.
//
// Route is the low-cardinality path template
// ("/repos/{owner}/{repo}/hooks/{hook_id}") used as the `route` metric
// label. It is carried separately from Path because deriving it by
// pattern-matching the concrete URL is guesswork: IDs, branch patterns,
// environment names and variable names are all path segments, and any
// one of them leaking into a label multiplies the time series by the
// number of distinct values. Callers that do not supply a Route are
// bucketed under RouteOther rather than being labelled with raw path.
type Call struct {
	Prio   Priority
	Owner  string
	Method string
	Path   string
	Route  string
	Body   any
	Out    any
}

// RouteOther is the catch-all metric label for calls made without a
// declared route template. Its presence in a dashboard means a call
// site still needs a route.
const RouteOther = "other"

// Do runs a REST request described by Call. It predates the go-github
// migration and remains for the paths the SDK does not cover — GraphQL
// and Link-header pagination over endpoints go-github does not model.
// Scheduling, auth and metrics all happen in the transport chain.
func (c *Client) Do(ctx context.Context, call Call) (*http.Response, error) {
	ctx = WithCall(ctx, call.Prio, call.Owner, call.Route)
	return c.execute(ctx, call, PoolREST)
}

// DoREST is the positional form of Do, kept for call sites that have no
// route template yet. Their traffic lands under RouteOther.
func (c *Client) DoREST(ctx context.Context, prio Priority, owner, method, path string, body any, out any) (*http.Response, error) {
	return c.Do(ctx, Call{Prio: prio, Owner: owner, Method: method, Path: path, Body: body, Out: out})
}

// DoGraphQL executes a GraphQL query under the GraphQL pool budget.
func (c *Client) DoGraphQL(ctx context.Context, prio Priority, owner, query string, vars map[string]any, out any) error {
	body := map[string]any{"query": query}
	if vars != nil {
		body["variables"] = vars
	}
	type wrapper struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	gctx := WithGraphQLCall(ctx, prio, owner)
	_, err := func() (*http.Response, error) {
		var w wrapper
		resp, err := c.execute(gctx, Call{
			Owner: owner, Method: http.MethodPost, Path: "/graphql", Route: "/graphql", Body: body, Out: &w,
		}, PoolGraphQL)
		if err != nil {
			return resp, err
		}
		if len(w.Errors) > 0 {
			msgs := make([]string, len(w.Errors))
			for i, e := range w.Errors {
				msgs[i] = e.Message
			}
			return resp, fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
		}
		if out != nil {
			if err := json.Unmarshal(w.Data, out); err != nil {
				return resp, fmt.Errorf("decoding graphql data: %w", err)
			}
		}
		return resp, nil
	}()
	return err
}

// execute is the inner request; it does NOT invoke the limiter (the caller
// did via Schedule). It does update remaining budget from the response
// headers and emit api_calls_total.
func (c *Client) execute(ctx context.Context, call Call, pool Pool) (*http.Response, error) {
	_ = pool // pool selection now travels in the call context
	method, path, body, out := call.Method, call.Path, call.Body, call.Out
	var bodyR io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encoding body: %w", err)
		}
		bodyR = bytes.NewReader(b)
	}
	full := c.apiURL + path
	req, err := http.NewRequestWithContext(ctx, method, full, bodyR)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.core.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(body)}
	}
	if out != nil {
		err = json.NewDecoder(resp.Body).Decode(out)
	}
	_ = resp.Body.Close()
	return resp, err
}

// APIError carries a non-2xx GitHub response.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github %s %s: %d %s", e.Method, e.Path, e.Status, e.Body)
}

// IsNotFound returns true if err is a wrapped 404 from GitHub.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// CheckRepository verifies access before loading optional admin files.
func (c *Client) CheckRepository(ctx context.Context, repo config.Repo) error {
	ctx = WithCall(ctx, PriorityCronReconcile, repo.Owner, ghapi.RouteRepo)
	_, _, err := c.GH().Repositories.Get(ctx, repo.Owner, repo.Name)
	return err
}

// GetFile implements config.FileFetcher: fetches a single file's content
// from the repo at the given ref. Returns config.ErrNotFound on 404.
func (c *Client) GetFile(ctx context.Context, repo config.Repo, path, ref string) ([]byte, error) {
	q := ""
	if ref != "" {
		q = "?ref=" + url.QueryEscape(ref)
	}
	_ = q
	var v struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	_, err := c.Do(ctx, Call{
		Prio: PriorityCronReconcile, Owner: repo.Owner, Method: http.MethodGet,
		Path: ghapi.RepoContents(repo, path, ref), Route: ghapi.RouteRepoContents, Out: &v,
	})
	if err != nil {
		if IsNotFound(err) {
			return nil, config.ErrNotFound
		}
		return nil, err
	}
	if v.Encoding != "base64" {
		return []byte(v.Content), nil
	}
	return decodeBase64Content(v.Content)
}
