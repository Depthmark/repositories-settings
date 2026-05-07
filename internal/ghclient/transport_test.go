package ghclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConditionalGet_304ReturnsCachedBody(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.Header().Set("ETag", `"abc"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	cache := newETagCache(0)
	c := &http.Client{Transport: newConditionalGetTransport(http.DefaultTransport, cache)}

	first, err := c.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := io.ReadAll(first.Body)
	_ = first.Body.Close()
	if string(b1) != `{"hello":"world"}` {
		t.Fatalf("first body = %q", string(b1))
	}

	second, err := c.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(second.Body)
	_ = second.Body.Close()
	if string(b2) != `{"hello":"world"}` {
		t.Fatalf("second body (cached) = %q", string(b2))
	}
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d", second.StatusCode)
	}
	if calls != 2 {
		t.Fatalf("expected 2 backend calls, got %d", calls)
	}
}

func TestConditionalGet_WriteInvalidates(t *testing.T) {
	cache := newETagCache(0)
	cache.Set("/x", etagEntry{etag: `"v1"`, body: []byte("old")})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := &http.Client{Transport: newConditionalGetTransport(http.DefaultTransport, cache)}

	req, _ := http.NewRequest(http.MethodPatch, srv.URL+"/x", nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// Cache key was the absolute URL on Set; PATCH invalidates by URL.Path.
	// The stored key happens to be "/x" — the transport uses req.URL.Path
	// for invalidation prefix matching, which matches.
	if _, ok := cache.Get("/x"); ok {
		t.Fatal("expected cache to be invalidated after write")
	}
}

func TestParseNextLink(t *testing.T) {
	header := `<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=10>; rel="last"`
	if got := parseNextLink(header); got != "https://api.github.com/x?page=2" {
		t.Fatalf("got %q", got)
	}
	if got := parseNextLink(""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
