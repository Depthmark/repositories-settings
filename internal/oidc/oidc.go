// Package oidc verifies OIDC bearer tokens against their issuer's JWKS.
//
// It exists so a GitHub Actions workflow can call this service with the
// short-lived token GitHub mints for it, instead of a long-lived shared
// secret copied into every repository. The token proves which repository
// the workflow runs in, which is the property the API actually needs:
// a static token proves only that someone holds the static token.
//
// The verifier is a value, not a package-level singleton, so a test — or
// a second deployment in the same process — gets its own key cache.
package oidc

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

const (
	// DefaultJWKSCacheTTL is how long a fetched key set is reused.
	DefaultJWKSCacheTTL = time.Hour
	// maxCachedIssuers bounds the key cache so a stream of tokens
	// carrying distinct issuers cannot exhaust memory.
	maxCachedIssuers = 32
	// Response caps for the two documents fetched from an issuer.
	maxDiscoveryBytes = 1 << 20
	maxJWKSBytes      = 5 << 20
)

// Claims is a decoded, verified JWT claim set.
type Claims map[string]any

// Config declares what a verifier will accept.
type Config struct {
	// Issuers is the allowlist. An empty list rejects every token: a
	// verifier that trusts any issuer trusts anyone who can run an
	// OIDC provider.
	Issuers []string
	// Audience is required on every token. GitHub mints a token with
	// whatever audience the workflow asks for, so without this check a
	// token minted for an unrelated relying party is accepted here.
	Audience string
	// TrustedJWKSHosts optionally allows an issuer to publish its keys
	// on another host, keyed by issuer URL. The default is strict
	// same-host pinning; a few real providers legitimately split them.
	TrustedJWKSHosts map[string][]string
	// GitHubIssuers are the issuers whose tokens carry GitHub Actions
	// repository claims. Empty means GitHub.com. A GitHub Enterprise
	// Server deployment names its own issuer here.
	GitHubIssuers []string
	// RequireImmutableSubject rejects a GitHub token whose subject does
	// not carry the numeric owner and repository IDs. Names can be
	// released and re-registered; IDs cannot.
	RequireImmutableSubject bool
	// CacheTTL overrides DefaultJWKSCacheTTL.
	CacheTTL time.Duration
	// HTTPClient overrides the default fetch client. Tests use this.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Verifier validates tokens against a fixed issuer allowlist.
type Verifier struct {
	cfg    Config
	client *http.Client
	ttl    time.Duration
	logger *slog.Logger

	mu    sync.RWMutex
	cache map[string]*keySet
	sf    singleflight.Group
}

type keySet struct {
	keys    map[string]*rsa.PublicKey // kid -> key
	fetched time.Time
}

// New builds a verifier. It returns an error rather than a verifier that
// accepts everything, because the failure mode of a misconfigured
// verifier is silent and total.
func New(cfg Config) (*Verifier, error) {
	issuers := make([]string, 0, len(cfg.Issuers))
	for _, iss := range cfg.Issuers {
		iss = strings.TrimSpace(iss)
		if iss == "" {
			continue
		}
		u, err := url.Parse(iss)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("issuer %q is not an absolute URL", iss)
		}
		if !strings.EqualFold(u.Scheme, "https") {
			return nil, fmt.Errorf("issuer %q must be https: token signatures would be fetched over an unauthenticated channel", iss)
		}
		issuers = append(issuers, iss)
	}
	if len(issuers) == 0 {
		return nil, fmt.Errorf("at least one issuer is required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, fmt.Errorf("an audience is required: without one, a token minted for any other relying party on the same issuer is accepted here")
	}
	cfg.Issuers = issuers

	trusted := make(map[string][]string, len(cfg.TrustedJWKSHosts))
	for iss, hosts := range cfg.TrustedJWKSHosts {
		lowered := make([]string, 0, len(hosts))
		for _, h := range hosts {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				lowered = append(lowered, h)
			}
		}
		if len(lowered) > 0 {
			trusted[strings.TrimRight(iss, "/")] = lowered
		}
	}
	cfg.TrustedJWKSHosts = trusted

	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			// Refuse redirects. A tampered discovery document must not be
			// able to walk signature verification over to another host,
			// nor chain a request through this service into a network it
			// can otherwise not reach.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	ttl := cfg.CacheTTL
	if ttl <= 0 {
		ttl = DefaultJWKSCacheTTL
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Verifier{
		cfg:    cfg,
		client: client,
		ttl:    ttl,
		logger: logger,
		cache:  map[string]*keySet{},
	}, nil
}

// VerifyGitHub verifies a token and returns the repository identity it
// proves. It is the call the API surface wants: a verified token that
// cannot be tied to a repository authorizes nothing here, so the two
// steps are not useful apart.
func (v *Verifier) VerifyGitHub(ctx context.Context, token string) (*GitHubIdentity, error) {
	claims, err := v.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	id, err := ParseGitHubIdentity(claims, v.cfg.GitHubIssuers, v.cfg.RequireImmutableSubject)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, fmt.Errorf("issuer %q does not mint GitHub Actions repository claims", claims["iss"])
	}
	return id, nil
}

// Issuers reports the allowlist, for logging at startup.
func (v *Verifier) Issuers() []string { return slices.Clone(v.cfg.Issuers) }

// Audience reports the required audience, for logging at startup.
func (v *Verifier) Audience() string { return v.cfg.Audience }

// Verify checks a token's signature, issuer, audience, and lifetime, and
// returns its claims.
func (v *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	// Parse once without verifying, only to learn which issuer's keys to
	// fetch. Nothing from this parse is trusted.
	unverified, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).
		ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		metrics.OIDCValidationsTotal.WithLabelValues("unknown", "malformed").Inc()
		return nil, fmt.Errorf("malformed token: %w", err)
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		metrics.OIDCValidationsTotal.WithLabelValues("unknown", "malformed").Inc()
		return nil, fmt.Errorf("malformed token claims")
	}
	issuer, _ := claims["iss"].(string)
	if issuer == "" {
		metrics.OIDCValidationsTotal.WithLabelValues("unknown", "malformed").Inc()
		return nil, fmt.Errorf("token has no issuer")
	}
	if !slices.Contains(v.cfg.Issuers, issuer) {
		metrics.OIDCValidationsTotal.WithLabelValues("untrusted", "issuer_not_allowed").Inc()
		return nil, fmt.Errorf("issuer %q is not allowed", issuer)
	}

	keys, err := v.keysFor(ctx, issuer)
	if err != nil {
		metrics.OIDCValidationsTotal.WithLabelValues(issuer, "jwks_unavailable").Inc()
		return nil, fmt.Errorf("fetching keys for %q: %w", issuer, err)
	}

	verified, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		// A missing kid used to fall back to "whichever key the map
		// yields first", which Go randomises. During a rotation overlap
		// that made a retired key validate intermittently.
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("token header has no kid")
		}
		key, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("key %q is not in the issuer's JWKS", kid)
		}
		return key, nil
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithIssuer(issuer),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	if err != nil {
		metrics.OIDCValidationsTotal.WithLabelValues(issuer, classify(err)).Inc()
		return nil, fmt.Errorf("token verification failed: %w", err)
	}

	out, ok := verified.Claims.(jwt.MapClaims)
	if !ok {
		metrics.OIDCValidationsTotal.WithLabelValues(issuer, "malformed").Inc()
		return nil, fmt.Errorf("unexpected claims type")
	}
	metrics.OIDCValidationsTotal.WithLabelValues(issuer, "ok").Inc()
	return Claims(out), nil
}

// classify maps a verification failure onto a bounded metric label.
func classify(err error) string {
	switch msg := err.Error(); {
	case strings.Contains(msg, "expired"):
		return "expired"
	case strings.Contains(msg, "audience"):
		return "audience"
	case strings.Contains(msg, "signature"):
		return "signature"
	case strings.Contains(msg, "signing method"):
		return "algorithm"
	case strings.Contains(msg, "kid"):
		return "kid"
	default:
		return "claims"
	}
}

// keysFor returns the issuer's key set, fetching it at most once per TTL
// even under a burst of concurrent requests.
func (v *Verifier) keysFor(ctx context.Context, issuer string) (map[string]*rsa.PublicKey, error) {
	if keys, ok := v.cached(issuer); ok {
		return keys, nil
	}
	got, err, _ := v.sf.Do(issuer, func() (any, error) {
		// Another goroutine may have won the race and already stored it.
		if keys, ok := v.cached(issuer); ok {
			return keys, nil
		}
		keys, err := v.fetchKeys(ctx, issuer)
		if err != nil {
			return nil, err
		}
		v.store(issuer, keys)
		return keys, nil
	})
	if err != nil {
		return nil, err
	}
	return got.(map[string]*rsa.PublicKey), nil
}

func (v *Verifier) cached(issuer string) (map[string]*rsa.PublicKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	set, ok := v.cache[issuer]
	if !ok || time.Since(set.fetched) >= v.ttl {
		return nil, false
	}
	return set.keys, true
}

func (v *Verifier) store(issuer string, keys map[string]*rsa.PublicKey) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cache[issuer] = &keySet{keys: keys, fetched: time.Now()}
	// Bounded by the issuer allowlist in practice; evict anyway so a
	// future multi-tenant deployment cannot grow this without limit.
	for len(v.cache) > maxCachedIssuers {
		oldestKey, oldest := "", time.Now()
		for k, set := range v.cache {
			if k != issuer && !set.fetched.After(oldest) {
				oldest, oldestKey = set.fetched, k
			}
		}
		if oldestKey == "" {
			break
		}
		delete(v.cache, oldestKey)
	}
}

// fetchKeys discovers the issuer's jwks_uri and reads its keys.
func (v *Verifier) fetchKeys(ctx context.Context, issuer string) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var discovery struct {
		JWKSURI string `json:"jwks_uri"`
	}
	discoveryURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	if err := v.getJSON(ctx, discoveryURL, maxDiscoveryBytes, &discovery); err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if discovery.JWKSURI == "" {
		return nil, fmt.Errorf("discovery document has no jwks_uri")
	}
	if err := v.checkJWKSHost(issuer, discovery.JWKSURI); err != nil {
		return nil, err
	}

	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := v.getJSON(ctx, discovery.JWKSURI, maxJWKSBytes, &doc); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		if k.Kid == "" {
			// Verification requires a kid match, so a key without one can
			// never be selected. Dropping it keeps the map honest.
			v.logger.Warn("skipping JWKS key with no kid", "issuer", issuer)
			continue
		}
		pub, err := k.publicKey()
		if err != nil {
			v.logger.Warn("skipping unusable JWKS key", "issuer", issuer, "kid", k.Kid, "error", err)
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("issuer %q published no usable RSA keys", issuer)
	}
	return keys, nil
}

func (v *Verifier) getJSON(ctx context.Context, rawURL string, limit int64, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	// A redirect arrives here as a 3xx because the client refuses to
	// follow it, and falls into this check.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %d", rawURL, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(into)
}

// checkJWKSHost refuses a jwks_uri that is not on the issuer's own host,
// unless the operator pinned the exception. Without this, whoever can
// tamper with a discovery document chooses the keys this service trusts.
func (v *Verifier) checkJWKSHost(issuer, jwksURI string) error {
	iss, err := url.Parse(issuer)
	if err != nil || iss.Host == "" {
		return fmt.Errorf("issuer %q is not a URL", issuer)
	}
	jwks, err := url.Parse(jwksURI)
	if err != nil || jwks.Host == "" {
		return fmt.Errorf("jwks_uri %q is not a URL", jwksURI)
	}
	if !strings.EqualFold(jwks.Scheme, iss.Scheme) {
		return fmt.Errorf("jwks_uri scheme %q does not match issuer scheme %q", jwks.Scheme, iss.Scheme)
	}
	if strings.EqualFold(jwks.Host, iss.Host) {
		return nil
	}
	if slices.Contains(v.cfg.TrustedJWKSHosts[strings.TrimRight(issuer, "/")], strings.ToLower(jwks.Host)) {
		return nil
	}
	return fmt.Errorf("issuer %q published its keys on %q, which is neither its own host nor a pinned exception",
		issuer, jwks.Host)
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (k jwk) publicKey() (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	e := new(big.Int).SetBytes(eb)
	if !e.IsInt64() || e.Int64() <= 0 || e.Int64() > 1<<31-1 {
		return nil, fmt.Errorf("exponent out of range")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e.Int64())}, nil
}
