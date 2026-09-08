package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/oidc"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
	"github.com/golang-jwt/jwt/v5"
)

// fakeIssuer mints GitHub-Actions-shaped tokens and serves the matching
// discovery document and JWKS.
type fakeIssuer struct {
	*httptest.Server
	key *rsa.PrivateKey
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fi := &fakeIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": fi.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := key.Public().(*rsa.PublicKey)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{
			"kty": "RSA", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	fi.Server = httptest.NewTLSServer(mux)
	t.Cleanup(fi.Close)
	return fi
}

// tokenFor mints a token whose claims say the workflow runs in repo.
func (fi *fakeIssuer) tokenFor(t *testing.T, owner, name string) string {
	t.Helper()
	return fi.tokenWithClaims(t, owner, name, nil)
}

func (fi *fakeIssuer) tokenWithClaims(t *testing.T, owner, name string, overrides jwt.MapClaims) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":                 fi.URL,
		"aud":                 "repo-settings",
		"sub":                 "repo:" + owner + "/" + name + ":ref:refs/heads/main",
		"repository":          owner + "/" + name,
		"repository_owner":    owner,
		"repository_id":       "42",
		"repository_owner_id": "7",
		"ref":                 "refs/heads/main",
		"event_name":          "push",
		"exp":                 time.Now().Add(5 * time.Minute).Unix(),
		"iat":                 time.Now().Add(-time.Minute).Unix(),
	}
	for key, value := range overrides {
		claims[key] = value
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "k1"
	signed, err := tok.SignedString(fi.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestAPI_OIDCApplyRequiresTrustedDefaultBranchRun(t *testing.T) {
	fi := newFakeIssuer(t)
	for _, tc := range []struct {
		name           string
		claims         jwt.MapClaims
		metadata       string
		metadataStatus int
		dryRun         bool
		want           int
	}{
		{name: "default branch push", want: http.StatusOK},
		{name: "default branch manual run", claims: jwt.MapClaims{"event_name": "workflow_dispatch"}, want: http.StatusOK},
		{name: "default branch schedule", claims: jwt.MapClaims{"event_name": "schedule"}, want: http.StatusOK},
		{name: "feature branch push", claims: jwt.MapClaims{"ref": "refs/heads/feature/unsafe"}, want: http.StatusForbidden},
		{name: "pull request", claims: jwt.MapClaims{"event_name": "pull_request", "ref": "refs/pull/5/merge"}, want: http.StatusForbidden},
		{name: "pull request target on default ref", claims: jwt.MapClaims{"event_name": "pull_request_target"}, want: http.StatusForbidden},
		{name: "missing event", claims: jwt.MapClaims{"event_name": nil}, want: http.StatusForbidden},
		{name: "missing ref", claims: jwt.MapClaims{"ref": nil}, want: http.StatusForbidden},
		{name: "missing default branch", metadata: `{"id":42,"owner":{"id":7}}`, want: http.StatusForbidden},
		{name: "recreated repository", metadata: `{"id":43,"owner":{"id":7},"default_branch":"main"}`, want: http.StatusForbidden},
		{name: "transferred repository", metadata: `{"id":42,"owner":{"id":8},"default_branch":"main"}`, want: http.StatusForbidden},
		{name: "metadata unavailable", metadataStatus: http.StatusNotFound, want: http.StatusBadGateway},
		{name: "feature branch dry run", claims: jwt.MapClaims{"ref": "refs/heads/feature/unsafe"}, dryRun: true, want: http.StatusOK},
		{name: "pull request dry run", claims: jwt.MapClaims{"event_name": "pull_request", "ref": "refs/pull/5/merge"}, dryRun: true, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/app" {
					t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if tc.metadataStatus != 0 {
					w.WriteHeader(tc.metadataStatus)
					return
				}
				metadata := tc.metadata
				if metadata == "" {
					metadata = `{"id":42,"owner":{"id":7},"default_branch":"main"}`
				}
				_, _ = w.Write([]byte(metadata))
			}))
			defer upstream.Close()
			rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 2})
			defer rl.Stop()
			loads := 0
			s := New(":0", Deps{
				Logger:     logger.Discard(),
				Client:     ghclient.New(ghclient.Config{APIURL: upstream.URL, Limiter: rl, Logger: logger.Discard()}),
				Reconciler: reconciler.New(logger.Discard()),
				OIDC:       fi.verifier(t),
				Settings: func(context.Context, config.Repo, string) (*config.Resolution, error) {
					loads++
					return config.RepoOnly(nil), nil
				},
			})
			body, err := json.Marshal(map[string]any{"owner": "acme", "repo": "app", "dry_run": tc.dryRun})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/reconcile", strings.NewReader(string(body)))
			req.Header.Set("Authorization", "Bearer "+fi.tokenWithClaims(t, "acme", "app", tc.claims))
			rr := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want != http.StatusOK && loads != 0 {
				t.Fatal("refused workflow reached settings loading")
			}
		})
	}
}

func (fi *fakeIssuer) verifier(t *testing.T) *oidc.Verifier {
	t.Helper()
	v, err := oidc.New(oidc.Config{
		Issuers:  []string{fi.URL},
		Audience: "repo-settings",
		// The fake issuer stands in for GitHub Actions, the same way a
		// GitHub Enterprise Server issuer would.
		GitHubIssuers: []string{fi.URL},
		HTTPClient:    fi.Client(),
		Logger:        logger.Discard(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func oidcServer(t *testing.T, fi *fakeIssuer, apiToken string) *Server {
	t.Helper()
	// The settings resolve to nothing, so no lane runs and no GitHub
	// call is made; the client is here for the per-repository lock the
	// reconcile path takes.
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 2})
	t.Cleanup(rl.Stop)
	return New(":0", Deps{
		Logger:     logger.Discard(),
		Client:     ghclient.New(ghclient.Config{APIURL: "https://example.invalid", Limiter: rl, Logger: logger.Discard()}),
		Reconciler: reconciler.New(logger.Discard()),
		OIDC:       fi.verifier(t),
		APIToken:   apiToken,
		Settings: func(context.Context, config.Repo, string) (*config.Resolution, error) {
			return config.RepoOnly(nil), nil
		},
	})
}

func postCheck(t *testing.T, s *Server, bearer, owner, name string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"owner":"` + owner + `","repo":"` + name + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/check", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	return rr
}

// The whole point of the OIDC mode: a workflow token names the
// repository it was minted in, and the service will only act on that
// repository. Without this binding, any repository holding a valid token
// could reconfigure any other repository the App is installed in.
func TestAPI_OIDCCallerIsBoundToItsOwnRepository(t *testing.T) {
	fi := newFakeIssuer(t)
	s := oidcServer(t, fi, "")
	token := fi.tokenFor(t, "acme", "app")

	t.Run("its own repository is allowed", func(t *testing.T) {
		if rr := postCheck(t, s, token, "acme", "app"); rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("another repository is forbidden", func(t *testing.T) {
		rr := postCheck(t, s, token, "acme", "other")
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "acme/app") {
			t.Errorf("the refusal should name the credential's scope: %s", rr.Body.String())
		}
	})

	t.Run("another owner is forbidden", func(t *testing.T) {
		if rr := postCheck(t, s, token, "evilcorp", "app"); rr.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("owner case is not a boundary", func(t *testing.T) {
		if rr := postCheck(t, s, token, "ACME", "App"); rr.Code != http.StatusOK {
			t.Fatalf("GitHub names are case-insensitive; expected 200, got %d", rr.Code)
		}
	})
}

func TestAPI_OIDCRejectsBadTokens(t *testing.T) {
	fi := newFakeIssuer(t)
	other := newFakeIssuer(t)
	s := oidcServer(t, fi, "")

	for _, tc := range []struct {
		name   string
		bearer string
	}{
		{"no credential", ""},
		{"a token from another issuer", other.tokenFor(t, "acme", "app")},
		{"a truncated token", fi.tokenFor(t, "acme", "app")[:20]},
		{"a garbage bearer", "not-a-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := postCheck(t, s, tc.bearer, "acme", "app"); rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// With both credentials configured, a JWT is judged as a JWT. It must
// never fall through to a string comparison against the static secret,
// or a rejected workflow token would be retried as one.
func TestAPI_JWTIsNeverComparedAgainstTheStaticToken(t *testing.T) {
	fi := newFakeIssuer(t)
	s := oidcServer(t, fi, "topsecret")

	// The static operator token is not repository-scoped.
	if rr := postCheck(t, s, "topsecret", "acme", "anything"); rr.Code != http.StatusOK {
		t.Fatalf("static token should reach any repository, got %d: %s", rr.Code, rr.Body.String())
	}
	// A workflow token still only reaches its own.
	token := fi.tokenFor(t, "acme", "app")
	if rr := postCheck(t, s, token, "acme", "other"); rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a scoped token, got %d", rr.Code)
	}
	// And a JWT that fails verification is refused outright rather than
	// being retried against the static token.
	if rr := postCheck(t, s, "a.b.c", "acme", "app"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
