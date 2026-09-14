package ghclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func newRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	return k
}

// AppJWT must produce an RS256 token signed by the configured key
// containing the App's iss claim and a 10-minute exp window.
func TestAppJWT_FormatAndClaims(t *testing.T) {
	key := newRSAKey(t)
	a := NewAppAuth(12345, key, "", nil)

	tok, err := a.AppJWT()
	if err != nil {
		t.Fatalf("AppJWT: %v", err)
	}

	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return &key.PublicKey, nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse: %v valid=%v", err, parsed != nil && parsed.Valid)
	}
	if got := parsed.Method.Alg(); got != "RS256" {
		t.Fatalf("alg = %s, want RS256", got)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims type %T", parsed.Claims)
	}
	if claims["iss"] != "12345" {
		t.Fatalf("iss = %v, want 12345", claims["iss"])
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp-iat < 9*60 || exp-iat > 11*60 {
		t.Fatalf("exp-iat = %v seconds, want ~10 minutes", exp-iat)
	}
}

// Within the 9-minute window AppJWT returns the cached value byte-for-
// byte; once we shove the clock forward we get a fresh signature.
func TestAppJWT_CachedThenRefreshed(t *testing.T) {
	key := newRSAKey(t)
	a := NewAppAuth(7, key, "", nil)
	now := time.Unix(1_700_000_000, 0)
	a.now = func() time.Time { return now }

	first, err := a.AppJWT()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := a.AppJWT()
	if first != second {
		t.Fatal("expected cached token reuse within window")
	}

	now = now.Add(10 * time.Minute) // past 9-min cache
	third, _ := a.AppJWT()
	if third == first {
		t.Fatal("expected refreshed token after expiry")
	}
}

// Concurrent InstallationToken calls for the same installation must
// trigger exactly one backend mint thanks to singleflight; subsequent
// calls within the cache window return the cached token without another
// HTTP roundtrip.
func TestInstallationToken_SingleflightAndCaching(t *testing.T) {
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /app/installations/<id>/access_tokens
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&mints, 1)
		// Slow path so concurrent callers all collapse onto the same flight.
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      fmt.Sprintf("ghs_%d", atomic.LoadInt32(&mints)),
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer srv.Close()

	key := newRSAKey(t)
	a := NewAppAuth(1, key, srv.URL, srv.Client())

	const fanout = 16
	var wg sync.WaitGroup
	tokens := make([]string, fanout)
	wg.Add(fanout)
	for i := 0; i < fanout; i++ {
		i := i
		go func() {
			defer wg.Done()
			tok, err := a.InstallationToken(context.Background(), 99)
			if err != nil {
				t.Errorf("InstallationToken: %v", err)
				return
			}
			tokens[i] = tok
		}()
	}
	wg.Wait()

	// All callers see the same token.
	for i, tok := range tokens {
		if tok != tokens[0] {
			t.Fatalf("caller %d got %q, want %q", i, tok, tokens[0])
		}
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Fatalf("backend mint calls = %d under fanout=%d, want 1 (singleflight should dedupe)", got, fanout)
	}

	// A subsequent call within the cache window must not hit the server.
	if _, err := a.InstallationToken(context.Background(), 99); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&mints); got != 1 {
		t.Fatalf("expected cache hit after first mint; backend was called %d times", got)
	}
}

// InstallationID resolves owner→id and caches the result. A fanout of
// concurrent lookups for the same org collapses to one backend call.
func TestInstallationID_LookupAndCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&hits, 1)
		time.Sleep(20 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": int64(4242)})
	}))
	defer srv.Close()

	key := newRSAKey(t)
	a := NewAppAuth(1, key, srv.URL, srv.Client())

	var wg sync.WaitGroup
	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			id, err := a.InstallationID(context.Background(), "acme")
			if err != nil {
				t.Errorf("InstallationID: %v", err)
				return
			}
			if id != 4242 {
				t.Errorf("id = %d, want 4242", id)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("backend lookups = %d, want 1 (singleflight should dedupe)", got)
	}

	// Cached path: no further hits.
	if _, err := a.InstallationID(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("cache miss: backend hits = %d after second call", got)
	}
}
