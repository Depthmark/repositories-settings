package oidc

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// issuerServer is a stand-in OIDC provider: a discovery document and a
// JWKS, plus a signer for minting tokens the verifier should accept.
type issuerServer struct {
	*httptest.Server
	key *rsa.PrivateKey
	kid string
	// jwksURI overrides the discovery document's jwks_uri, to exercise
	// host pinning.
	jwksURI string
	// discoveryStatus overrides the discovery response code.
	discoveryStatus int
	// jwksFetches counts JWKS reads, so key caching is observable.
	jwksFetches atomic.Int32
}

func newIssuer(t *testing.T) *issuerServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &issuerServer{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if is.discoveryStatus != 0 {
			w.WriteHeader(is.discoveryStatus)
			return
		}
		uri := is.jwksURI
		if uri == "" {
			uri = is.URL + "/jwks"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": uri})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		is.jwksFetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{is.jwk()}})
	})
	is.Server = httptest.NewTLSServer(mux)
	t.Cleanup(is.Close)
	return is
}

func (is *issuerServer) jwk() map[string]string {
	pub := is.key.Public().(*rsa.PublicKey)
	return map[string]string{
		"kty": "RSA",
		"kid": is.kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// mint signs a token, letting a test override any claim or the kid.
func (is *issuerServer) mint(t *testing.T, claims jwt.MapClaims, kid string) string {
	t.Helper()
	base := jwt.MapClaims{
		"iss": is.URL,
		"aud": "repo-settings",
		"sub": "repo:acme/app:ref:refs/heads/main",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Add(-time.Minute).Unix(),
	}
	for k, v := range claims {
		if v == nil {
			delete(base, k)
			continue
		}
		base[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, base)
	if kid == "" {
		kid = is.kid
	}
	if kid != "-" {
		tok.Header["kid"] = kid
	}
	signed, err := tok.SignedString(is.key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func (is *issuerServer) verifier(t *testing.T, mutate func(*Config)) *Verifier {
	t.Helper()
	cfg := Config{
		Issuers:    []string{is.URL},
		Audience:   "repo-settings",
		HTTPClient: is.Client(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	// The test issuer is https via httptest's TLS server, so the https
	// requirement in New is satisfied without special-casing it.
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNew_RefusesUnsafeConfigurations(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"no issuers", Config{Audience: "a"}, "at least one issuer"},
		{"no audience", Config{Issuers: []string{"https://x.example"}}, "audience is required"},
		{"http issuer", Config{Issuers: []string{"http://x.example"}, Audience: "a"}, "must be https"},
		{"not a url", Config{Issuers: []string{"nope"}, Audience: "a"}, "absolute URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestVerify_AcceptsAWellFormedToken(t *testing.T) {
	is := newIssuer(t)
	v := is.verifier(t, nil)
	claims, err := v.Verify(context.Background(), is.mint(t, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	if claims["sub"] != "repo:acme/app:ref:refs/heads/main" {
		t.Fatalf("claims did not survive verification: %+v", claims)
	}
}

func TestVerify_Rejects(t *testing.T) {
	is := newIssuer(t)
	other := newIssuer(t)

	tests := []struct {
		name  string
		token func() string
		v     func() *Verifier
		want  string
	}{
		{
			name:  "an expired token",
			token: func() string { return is.mint(t, jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}, "") },
			want:  "expired",
		},
		{
			name:  "a token with no expiry",
			token: func() string { return is.mint(t, jwt.MapClaims{"exp": nil}, "") },
			want:  "verification failed",
		},
		{
			name:  "a token for another audience",
			token: func() string { return is.mint(t, jwt.MapClaims{"aud": "some-other-service"}, "") },
			want:  "verification failed",
		},
		{
			name:  "a token from an issuer that is not allowed",
			token: func() string { return other.mint(t, jwt.MapClaims{"iss": other.URL}, "") },
			want:  "not allowed",
		},
		{
			name:  "a token with no issuer",
			token: func() string { return is.mint(t, jwt.MapClaims{"iss": nil}, "") },
			want:  "no issuer",
		},
		{
			name:  "a token whose kid is unknown",
			token: func() string { return is.mint(t, nil, "some-other-kid") },
			want:  "verification failed",
		},
		{
			name:  "a token with no kid at all",
			token: func() string { return is.mint(t, nil, "-") },
			want:  "verification failed",
		},
		{
			name:  "a signature from the wrong key",
			token: func() string { return other.mint(t, jwt.MapClaims{"iss": is.URL}, is.kid) },
			want:  "verification failed",
		},
		{
			name:  "an unsigned token",
			token: func() string { return "not.a.jwt" },
			want:  "malformed",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := is.verifier(t, nil)
			if tc.v != nil {
				v = tc.v()
			}
			_, err := v.Verify(context.Background(), tc.token())
			if err == nil {
				t.Fatal("expected the token to be rejected")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The "alg": "none" family of attacks: a token that declares no
// signature must never be accepted, whatever its claims say.
func TestVerify_RejectsUnsignedAlgorithm(t *testing.T) {
	is := newIssuer(t)
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss": is.URL, "aud": "repo-settings", "sub": "repo:acme/app",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = is.kid
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := is.verifier(t, nil).Verify(context.Background(), signed); err == nil {
		t.Fatal("an unsigned token was accepted")
	}
}

// Whoever can tamper with a discovery document must not be able to point
// signature verification at keys they control.
func TestVerify_PinsJWKSToTheIssuerHost(t *testing.T) {
	is := newIssuer(t)
	attacker := newIssuer(t)
	is.jwksURI = attacker.URL + "/jwks"

	t.Run("an off-host jwks_uri is refused", func(t *testing.T) {
		_, err := is.verifier(t, nil).Verify(context.Background(), is.mint(t, nil, ""))
		if err == nil || !strings.Contains(err.Error(), "neither its own host nor a pinned exception") {
			t.Fatalf("off-host JWKS should be refused, got %v", err)
		}
	})

	t.Run("an operator can pin an exception", func(t *testing.T) {
		host := strings.TrimPrefix(attacker.URL, "https://")
		v := is.verifier(t, func(c *Config) {
			c.TrustedJWKSHosts = map[string][]string{is.URL: {host}}
		})
		// The token is signed by is, but the keys now come from attacker,
		// so it still fails — on the signature, not on the host check.
		_, err := v.Verify(context.Background(), is.mint(t, nil, ""))
		if err != nil && strings.Contains(err.Error(), "pinned exception") {
			t.Fatalf("the pinned host should have been accepted, got %v", err)
		}
	})
}

// A discovery document that redirects must not be followed: it is the
// same escape hatch as an off-host jwks_uri, one hop later.
func TestVerify_DoesNotFollowRedirects(t *testing.T) {
	is := newIssuer(t)
	is.discoveryStatus = http.StatusFound
	_, err := is.verifier(t, nil).Verify(context.Background(), is.mint(t, nil, ""))
	if err == nil || !strings.Contains(err.Error(), "discovery") {
		t.Fatalf("a redirected discovery must fail, got %v", err)
	}
}

// The key set is fetched once per issuer per TTL, not once per request.
func TestVerify_CachesKeysAcrossRequests(t *testing.T) {
	is := newIssuer(t)
	v := is.verifier(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := v.Verify(context.Background(), is.mint(t, nil, "")); err != nil {
			t.Fatal(err)
		}
	}
	if got := is.jwksFetches.Load(); got != 1 {
		t.Fatalf("expected one JWKS fetch across three verifications, got %d", got)
	}
}

// A burst of concurrent first requests must collapse into a single
// fetch: a cold cache under load should not stampede the issuer.
func TestVerify_ConcurrentColdStartFetchesOnce(t *testing.T) {
	is := newIssuer(t)
	v := is.verifier(t, nil)
	token := is.mint(t, nil, "")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Verify(context.Background(), token); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := is.jwksFetches.Load(); got != 1 {
		t.Fatalf("expected one JWKS fetch for a concurrent cold start, got %d", got)
	}
}
