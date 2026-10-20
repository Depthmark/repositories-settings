package ghclient

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
)

// AppAuth knows how to mint App JWTs and trade them for installation
// tokens. Mirrors github-sts/internal/github/app.go layout.
type AppAuth struct {
	appID      int64
	privateKey *rsa.PrivateKey
	apiURL     string
	httpClient *http.Client
	now        func() time.Time

	jwtMu    sync.Mutex
	jwtCache cachedJWT

	instMu    sync.RWMutex
	instCache map[string]*cachedInstallation

	tokenMu    sync.RWMutex
	tokenCache map[int64]*cachedToken

	sf singleflight.Group
}

type cachedJWT struct {
	token     string
	expiresAt time.Time
}

type cachedInstallation struct {
	id        int64
	fetchedAt time.Time
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// NewAppAuth constructs an AppAuth. apiURL defaults to https://api.github.com.
func NewAppAuth(appID int64, privateKey *rsa.PrivateKey, apiURL string, httpClient *http.Client) *AppAuth {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AppAuth{
		appID:      appID,
		privateKey: privateKey,
		apiURL:     strings.TrimRight(apiURL, "/"),
		httpClient: httpClient,
		now:        time.Now,
		instCache:  map[string]*cachedInstallation{},
		tokenCache: map[int64]*cachedToken{},
	}
}

// AppJWT returns a current App JWT, signing a new one if the cached value
// is within 60s of expiry. JWT lifetime is 10 minutes; we cache for 9.
func (a *AppAuth) AppJWT() (string, error) {
	a.jwtMu.Lock()
	defer a.jwtMu.Unlock()
	if a.jwtCache.token != "" && a.now().Before(a.jwtCache.expiresAt.Add(-1*time.Minute)) {
		return a.jwtCache.token, nil
	}
	now := a.now()
	claims := jwt.MapClaims{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(10 * time.Minute).Unix(),
		"iss": fmt.Sprintf("%d", a.appID),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(a.privateKey)
	if err != nil {
		return "", fmt.Errorf("signing app jwt: %w", err)
	}
	a.jwtCache = cachedJWT{token: signed, expiresAt: now.Add(9 * time.Minute)}
	return signed, nil
}

// InstallationID resolves the installation that owns the given org/user,
// caching for 15 minutes. Concurrent lookups for the same scope are
// deduplicated via singleflight.
func (a *AppAuth) InstallationID(ctx context.Context, org string) (int64, error) {
	a.instMu.RLock()
	if entry, ok := a.instCache[org]; ok && a.now().Sub(entry.fetchedAt) < 15*time.Minute {
		id := entry.id
		a.instMu.RUnlock()
		return id, nil
	}
	a.instMu.RUnlock()

	v, err, _ := a.sf.Do("inst:"+org, func() (any, error) {
		return a.fetchInstallationID(ctx, org)
	})
	if err != nil {
		return 0, err
	}
	id := v.(int64)
	a.instMu.Lock()
	a.instCache[org] = &cachedInstallation{id: id, fetchedAt: a.now()}
	a.instMu.Unlock()
	return id, nil
}

func (a *AppAuth) fetchInstallationID(ctx context.Context, org string) (int64, error) {
	jwtTok, err := a.AppJWT()
	if err != nil {
		return 0, err
	}
	url := fmt.Sprintf("%s/orgs/%s/installation", a.apiURL, org)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwtTok)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("resolving installation for %q: %w", org, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("github app is not installed on org %q", org)
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("install lookup failed: %d %s", resp.StatusCode, string(body))
	}
	var v struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 0, fmt.Errorf("decoding installation lookup: %w", err)
	}
	if v.ID == 0 {
		return 0, errors.New("installation id missing in response")
	}
	return v.ID, nil
}

// InstallationToken returns a cached installation access token, refreshing
// if within 5 minutes of expiry.
func (a *AppAuth) InstallationToken(ctx context.Context, installID int64) (string, error) {
	a.tokenMu.RLock()
	if entry, ok := a.tokenCache[installID]; ok && a.now().Add(5*time.Minute).Before(entry.expiresAt) {
		tok := entry.token
		a.tokenMu.RUnlock()
		return tok, nil
	}
	a.tokenMu.RUnlock()

	v, err, _ := a.sf.Do(fmt.Sprintf("tok:%d", installID), func() (any, error) {
		return a.mintInstallationToken(ctx, installID)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (a *AppAuth) mintInstallationToken(ctx context.Context, installID int64) (string, error) {
	jwtTok, err := a.AppJWT()
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", a.apiURL, installID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwtTok)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("minting installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("install token mint failed: %d %s", resp.StatusCode, string(body))
	}
	var v struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", fmt.Errorf("decoding install token: %w", err)
	}
	a.tokenMu.Lock()
	a.tokenCache[installID] = &cachedToken{token: v.Token, expiresAt: v.ExpiresAt}
	a.tokenMu.Unlock()
	return v.Token, nil
}

// ParsePrivateKey decodes a PEM-encoded RSA private key.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	return jwt.ParseRSAPrivateKeyFromPEM(pemBytes)
}
