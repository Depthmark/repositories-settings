package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

// fakeGH builds a minimal GitHub API stand-in for sticky-comment tests.
// existingBody is what the comment-list endpoint returns; if it
// contains stickyMarker the upsert should PATCH, otherwise POST.
func fakeGH(t *testing.T, existingBody string) (*httptest.Server, *int32, *int32, *int32) {
	t.Helper()
	var lists, posts, patches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments") &&
			strings.Contains(r.URL.Path, "/issues/"):
			atomic.AddInt32(&lists, 1)
			if existingBody == "" {
				_ = json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 9000, "body": existingBody, "user": map[string]any{"login": "repo-settings[bot]", "type": "Bot"}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			atomic.AddInt32(&posts, 1)
			body, _ := io.ReadAll(r.Body)
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1234, "body": v["body"]})
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/issues/comments/"):
			atomic.AddInt32(&patches, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &lists, &posts, &patches
}

func newClient(srv *httptest.Server) (*ghclient.Client, *ghclient.RateLimiter) {
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 4})
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	return cl, rl
}

// First time: no sticky comment exists, so we POST.
func TestUpsertStickyComment_CreatesWhenAbsent(t *testing.T) {
	srv, _, posts, patches := fakeGH(t, "")
	defer srv.Close()
	cl, rl := newClient(srv)
	defer rl.Stop()

	id, err := upsertStickyComment(context.Background(), cl,
		config.Repo{Owner: "o", Name: "r"}, 7, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id != 1234 {
		t.Fatalf("returned id = %d, want 1234", id)
	}
	if atomic.LoadInt32(posts) != 1 {
		t.Fatalf("posts = %d, want 1", atomic.LoadInt32(posts))
	}
	if atomic.LoadInt32(patches) != 0 {
		t.Fatalf("patches = %d, want 0 on first run", atomic.LoadInt32(patches))
	}
}

// Second time: a marker-tagged comment already exists, so we PATCH the
// same row instead of stacking another comment.
func TestUpsertStickyComment_UpdatesWhenPresent(t *testing.T) {
	srv, _, posts, patches := fakeGH(t, stickyMarker+"\nold body")
	defer srv.Close()
	cl, rl := newClient(srv)
	defer rl.Stop()

	id, err := upsertStickyComment(context.Background(), cl,
		config.Repo{Owner: "o", Name: "r"}, 7, "new body")
	if err != nil {
		t.Fatal(err)
	}
	if id != 9000 {
		t.Fatalf("returned id = %d, want 9000 (existing comment)", id)
	}
	if atomic.LoadInt32(posts) != 0 {
		t.Fatalf("posts = %d, want 0 when sticky exists", atomic.LoadInt32(posts))
	}
	if atomic.LoadInt32(patches) != 1 {
		t.Fatalf("patches = %d, want 1", atomic.LoadInt32(patches))
	}
}

// findStickyComment must ignore non-marker comments left by users.
func TestFindStickyComment_IgnoresUnrelated(t *testing.T) {
	srv, _, _, _ := fakeGH(t, "ordinary review comment")
	defer srv.Close()
	cl, rl := newClient(srv)
	defer rl.Stop()

	got, err := findStickyComment(context.Background(), cl,
		config.Repo{Owner: "o", Name: "r"}, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected no sticky match, got id=%d", got.ID)
	}
}
