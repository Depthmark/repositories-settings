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

// The cache is populated through a real GET so the entry carries the
// production key shape (absolute URL) rather than a hand-written path.
// The previous version of this test stored the key "/x" directly, which
// happened to match the path the transport invalidated on and therefore
// passed while production never invalidated anything.
func TestConditionalGet_WriteInvalidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"n":1}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cache := newETagCache(0)
	c := &http.Client{Transport: newConditionalGetTransport(http.DefaultTransport, cache)}

	readURL := srv.URL + "/repos/o/r/rulesets"
	resp, err := c.Get(readURL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if _, ok := cache.Get(readURL); !ok {
		t.Fatalf("GET did not populate the cache under key %q", readURL)
	}

	// Writing one item of the collection must drop the cached collection.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/repos/o/r/rulesets/7", nil)
	wresp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = wresp.Body.Close()

	if _, ok := cache.Get(readURL); ok {
		t.Fatal("writing an item left the collection read cached")
	}
}

// A failed write leaves live state untouched, so the cached read is
// still accurate and must not be thrown away.
func TestConditionalGet_FailedWriteKeepsCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"n":1}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	cache := newETagCache(0)
	c := &http.Client{Transport: newConditionalGetTransport(http.DefaultTransport, cache)}
	readURL := srv.URL + "/repos/o/r/rulesets"
	resp, err := c.Get(readURL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/repos/o/r/rulesets/7", nil)
	wresp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = wresp.Body.Close()

	if _, ok := cache.Get(readURL); !ok {
		t.Fatal("a rejected write must not invalidate the cache")
	}
}
