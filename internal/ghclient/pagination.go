package ghclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// Paginate walks a Link-header paginated REST endpoint and decodes each
// page into a fresh slice of T, appending to out. Mirrors paginateList in
// src/github/api/pagination.ts.
func Paginate[T any](ctx context.Context, c *Client, prio Priority, owner, path string) ([]T, error) {
	if !hasQueryParam(path, "per_page") {
		path = appendQuery(path, "per_page", "100")
	}
	var out []T
	next := path
	for next != "" {
		var raw json.RawMessage
		resp, err := c.DoREST(ctx, prio, owner, http.MethodGet, next, nil, &raw)
		if err != nil {
			return nil, err
		}
		var page []T
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decoding page: %w", err)
		}
		out = append(out, page...)
		next = parseNextLink(resp.Header.Get("Link"))
		if next != "" {
			// Strip the API base if absolute.
			if u, err := url.Parse(next); err == nil {
				next = u.RequestURI()
			}
		}
	}
	return out, nil
}

var linkNextRE = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func parseNextLink(link string) string {
	m := linkNextRE.FindStringSubmatch(link)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func hasQueryParam(path, key string) bool {
	u, err := url.Parse(path)
	if err != nil {
		return false
	}
	return u.Query().Get(key) != ""
}

func appendQuery(path, key, val string) string {
	if !contains(path, "?") {
		return path + "?" + key + "=" + val
	}
	return path + "&" + key + "=" + val
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
