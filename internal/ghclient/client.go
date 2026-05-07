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

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/metrics"
)

// Client is the high-level GitHub client.
type Client struct {
	auth     *AppAuth
	apiURL   string
	limiter  *RateLimiter
	cache    *etagCache
	core     *http.Client
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
		apiURL = "https://api.github.com"
	}
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = http.DefaultClient
	}
	cache := newETagCache(cfg.CacheCap)
	// Wrap base transport with conditional GET; rate limit observation
	// happens inside DoREST after the request returns.
	base := httpc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped := newConditionalGetTransport(base, cache)
	httpc = &http.Client{
		Transport: wrapped,
		Timeout:   httpc.Timeout,
	}
	return &Client{
		auth:     cfg.Auth,
		apiURL:   strings.TrimRight(apiURL, "/"),
		limiter:  cfg.Limiter,
		cache:    cache,
		core:     httpc,
		logger:   cfg.Logger,
		repoLock: NewRepoLock(),
	}
}

// RepoLock returns the per-repo serializer.
func (c *Client) RepoLock() *RepoLock { return c.repoLock }

// Limiter returns the rate limiter (mostly for tests / metrics handlers).
func (c *Client) Limiter() *RateLimiter { return c.limiter }

// DoREST runs an authenticated REST request through the rate limiter
// and decodes the response into out (pass nil to skip).
func (c *Client) DoREST(ctx context.Context, prio Priority, owner, method, path string, body any, out any) (*http.Response, error) {
	return Schedule(ctx, c.limiter, prio, PoolREST, func() (*http.Response, error) {
		return c.execute(ctx, owner, method, path, body, out, PoolREST)
	})
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
	_, err := Schedule(ctx, c.limiter, prio, PoolGraphQL, func() (*http.Response, error) {
		var w wrapper
		resp, err := c.execute(ctx, owner, http.MethodPost, "/graphql", body, &w, PoolGraphQL)
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
	})
	return err
}

// execute is the inner request; it does NOT invoke the limiter (the caller
// did via Schedule). It does update remaining budget from the response
// headers and emit api_calls_total.
func (c *Client) execute(ctx context.Context, owner, method, path string, body any, out any, pool Pool) (*http.Response, error) {
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
	if c.auth != nil && owner != "" {
		instID, err := c.auth.InstallationID(ctx, owner)
		if err != nil {
			return nil, err
		}
		tok, err := c.auth.InstallationToken(ctx, instID)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.core.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	c.limiter.UpdateFromResponse(pool, resp.Header)
	endpoint := scrubPath(path)
	statusBucket := fmt.Sprintf("%d", resp.StatusCode/100*100)
	metrics.APICallsTotal.WithLabelValues(method, endpoint, statusBucket).Inc()
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

// scrubPath replaces /<owner>/<repo>/ segments with placeholders so the
// metric label cardinality is bounded.
func scrubPath(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	parts := strings.Split(u.Path, "/")
	for i, p := range parts {
		// /repos/{owner}/{repo}/...
		if i > 0 && parts[i-1] == "repos" {
			parts[i] = "{owner}"
			if i+1 < len(parts) {
				parts[i+1] = "{repo}"
			}
		}
		if i > 0 && parts[i-1] == "orgs" {
			parts[i] = "{org}"
			break
		}
		_ = p
	}
	return strings.Join(parts, "/")
}

// GetFile implements config.FileFetcher: fetches a single file's content
// from the repo at the given ref. Returns config.ErrNotFound on 404.
func (c *Client) GetFile(ctx context.Context, repo config.Repo, path, ref string) ([]byte, error) {
	q := ""
	if ref != "" {
		q = "?ref=" + url.QueryEscape(ref)
	}
	url := fmt.Sprintf("/repos/%s/%s/contents/%s%s", repo.Owner, repo.Name, path, q)
	var v struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	_, err := c.DoREST(ctx, PriorityCronReconcile, repo.Owner, http.MethodGet, url, nil, &v)
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
