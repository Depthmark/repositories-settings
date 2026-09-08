package ghclient

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// conditionalGetTransport adds If-None-Match to GETs and substitutes the
// cached body when the server replies 304 Not Modified. Replaces the
// stand-alone ETag wrapper from the TS code.
//
// A successful write invalidates every cached read on a related path:
// the write target itself, anything beneath it, and the collection a
// written item belongs to. See etagCache.InvalidatePath.
type conditionalGetTransport struct {
	next  http.RoundTripper
	cache *etagCache
}

func newConditionalGetTransport(next http.RoundTripper, cache *etagCache) *conditionalGetTransport {
	if cache == nil {
		cache = newETagCache(0)
	}
	return &conditionalGetTransport{next: next, cache: cache}
}

func (t *conditionalGetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		resp, err := t.next.RoundTrip(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			t.cache.InvalidatePath(req.URL.Path)
		}
		return resp, err
	}

	key := req.URL.String()
	if cached, ok := t.cache.Get(key); ok {
		req.Header.Set("If-None-Match", cached.etag)
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusNotModified {
		cached, ok := t.cache.Get(key)
		if !ok {
			// Server says 304 but we have nothing cached. Return as-is.
			return resp, nil
		}
		_ = resp.Body.Close()
		fake := &http.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Proto:      resp.Proto,
			ProtoMajor: resp.ProtoMajor,
			ProtoMinor: resp.ProtoMinor,
			Header:     resp.Header.Clone(),
			Body:       io.NopCloser(bytes.NewReader(cached.body)),
			Request:    resp.Request,
		}
		return fake, nil
	}

	if resp.StatusCode == http.StatusOK {
		etag := strings.TrimSpace(resp.Header.Get("ETag"))
		if etag != "" {
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				return nil, err
			}
			t.cache.Set(key, etagEntry{etag: etag, body: body, path: req.URL.Path})
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return resp, nil
}
