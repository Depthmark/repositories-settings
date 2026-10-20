// repo-settings is the declarative GitHub repository configuration-as-code
// service. It listens for GitHub App webhooks, exposes /api/reconcile and
// /api/validate, and (optionally) drives a periodic batch reconcile.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/oidc"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
	"github.com/Depthmark/repositories-settings/internal/server"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	log := logger.New(env("LOG_LEVEL", "info"))
	slog.SetDefault(log)

	addr := env("ADDR", ":8080")
	apiURL := env("GITHUB_API_URL", "https://api.github.com")
	allowUnauthenticated := os.Getenv("ALLOW_UNAUTHENTICATED") == "1"
	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	apiToken := os.Getenv("API_TOKEN")
	if !allowUnauthenticated && webhookSecret == "" {
		log.Error("WEBHOOK_SECRET is required (set ALLOW_UNAUTHENTICATED=1 to opt out for dev)")
		os.Exit(1)
	}

	// OIDC lets a workflow authenticate with the short-lived token GitHub
	// mints for it, which names the repository it runs in. That is a
	// stronger credential than API_TOKEN: the static token proves only
	// that its holder has the static token, and every repository sharing
	// it can reconcile every other one.
	verifier, err := buildOIDCVerifier(log)
	if err != nil {
		log.Error("OIDC configuration", "error", err)
		os.Exit(1)
	}
	if !allowUnauthenticated && apiToken == "" && verifier == nil {
		log.Error("a privileged API credential is required: set OIDC_AUDIENCE for workflow tokens, or API_TOKEN for a static secret (ALLOW_UNAUTHENTICATED=1 opts out for dev)")
		os.Exit(1)
	}
	if verifier != nil {
		log.Info("OIDC enabled for /api routes",
			"issuers", verifier.Issuers(),
			"audience", verifier.Audience(),
			"require_immutable_subject", requireImmutableSubject())
	}
	if apiToken != "" {
		log.Info("static API_TOKEN accepted on /api routes; it is not repository-scoped")
	}

	registry := prometheus.NewRegistry()
	metrics.Register(registry)

	// GitHub App auth. The PEM may be supplied inline (PRIVATE_KEY) or
	// as a file path (PRIVATE_KEY_PATH); APP_ID is required either way.
	// Set ALLOW_UNAUTHENTICATED=1 to skip — only useful for dev / CI
	// smoke tests against public repos. In production a misconfigured
	// daemon must NOT silently degrade to anonymous reads, because
	// private repos return 404 (not 403) and the loader treats that as
	// "unmanaged" — which would make every PR check pass with 0 diffs.
	var auth *ghclient.AppAuth
	appIDStr := os.Getenv("APP_ID")
	if appIDStr == "" {
		if allowUnauthenticated {
			log.Warn("APP_ID unset and ALLOW_UNAUTHENTICATED=1; running anonymously (dev only)")
		} else {
			log.Error("APP_ID is required (set ALLOW_UNAUTHENTICATED=1 to opt out for dev)")
			os.Exit(1)
		}
	} else {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			log.Error("invalid APP_ID", "value", appIDStr)
			os.Exit(1)
		}
		pem, err := loadPrivateKey()
		if err != nil {
			log.Error("load private key", "error", err)
			os.Exit(1)
		}
		key, err := ghclient.ParsePrivateKey(pem)
		if err != nil {
			log.Error("parse private key", "error", err)
			os.Exit(1)
		}
		auth = ghclient.NewAppAuth(appID, key, apiURL, nil)
	}

	limiter := ghclient.NewRateLimiter(log, ghclient.Options{Concurrency: 10})
	defer limiter.Stop()

	cl := ghclient.New(ghclient.Config{
		Auth:    auth,
		APIURL:  apiURL,
		Limiter: limiter,
		Logger:  log,
	})

	disabled, unknownDisabled := config.ParseDisabledResources(os.Getenv("DISABLED_RESOURCES"))
	for _, k := range unknownDisabled {
		log.Warn("DISABLED_RESOURCES contains unknown key (ignored)", "key", k)
	}
	if keys := disabled.Keys(); len(keys) > 0 {
		log.Info("operator-disabled resources", "keys", keys)
	}

	rec := reconciler.New(log).WithDisabled(disabled)

	// Settings resolver: the org admin layer (when one is configured),
	// then the repository's own .github/settings/*.yml at the given ref,
	// then the org policy verdict on the merged result.
	//
	// ORG_ADMIN_REPO names the admin repository as "owner/name", or just
	// "name" to resolve it inside each target repository's own owner —
	// which is what a single-org deployment wants, and what makes a
	// multi-org install read each org's policy rather than one org's.
	adminRepoRef := os.Getenv("ORG_ADMIN_REPO")
	if err := config.ValidateAdminRepoRef(adminRepoRef); err != nil {
		log.Error("org admin repository configuration", "error", err)
		os.Exit(1)
	}
	if adminRepoRef == "" {
		log.Warn("ORG_ADMIN_REPO is unset; org defaults and admin policy are not enforced")
	} else {
		log.Info("org admin layer enabled", "admin_repo", adminRepoRef)
	}
	admin := config.NewAdminCache(cl, adminRepoRef, log)

	settingsLoader := func(ctx context.Context, repo config.Repo, ref string) (*config.Resolution, error) {
		repoLayer, err := config.Load(ctx, cl, repo, ref)
		if err != nil {
			return nil, err
		}
		layer, err := admin.For(ctx, repo)
		if err != nil {
			return nil, err
		}
		return config.ResolveFor(layer, repoLayer, config.RepoContext{Repo: repo}), nil
	}

	srv := server.New(addr, server.Deps{
		Logger:        log,
		Client:        cl,
		Reconciler:    rec,
		WebhookSecret: []byte(webhookSecret),
		APIToken:      apiToken,
		OIDC:          verifier,

		AllowUnauthenticated: allowUnauthenticated,
		Registry:             registry,
		Settings:             settingsLoader,
		AppSlug:              env("APP_SLUG", "repo-settings"),
		DisabledResources:    disabled,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting server", "addr", addr)
	if err := srv.ListenAndServe(ctx); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
	log.Info("server stopped")
}

// buildOIDCVerifier constructs the workflow-token verifier from the
// environment, or returns nil when OIDC is not configured.
//
// OIDC_AUDIENCE is the switch. It has no default on purpose: the
// audience is what stops a token minted for some other relying party on
// the same issuer from being replayed here, so an operator has to choose
// a value they also set in the workflow.
func buildOIDCVerifier(log *slog.Logger) (*oidc.Verifier, error) {
	audience := os.Getenv("OIDC_AUDIENCE")
	if audience == "" {
		if os.Getenv("OIDC_ISSUERS") != "" {
			return nil, errors.New("OIDC_ISSUERS is set but OIDC_AUDIENCE is not; refusing to accept tokens minted for any audience")
		}
		return nil, nil
	}
	issuers := splitList(env("OIDC_ISSUERS", oidc.GitHubActionsIssuer))

	// Per-issuer JWKS host exceptions, as "issuer=host[,host]" entries.
	// The default is strict same-host pinning.
	trusted, err := parseTrustedJWKSHosts(os.Getenv("OIDC_TRUSTED_JWKS_HOSTS"))
	if err != nil {
		return nil, err
	}

	return oidc.New(oidc.Config{
		Issuers: issuers,
		// GitHub Enterprise Server mints Actions claims under its own
		// issuer, so every trusted issuer is treated as GitHub-shaped.
		GitHubIssuers:           issuers,
		Audience:                audience,
		TrustedJWKSHosts:        trusted,
		RequireImmutableSubject: requireImmutableSubject(),
		Logger:                  log,
	})
}

// Entries are separated by whitespace, while each entry's host list uses
// commas: "https://issuer.example=keys.example,backup.example".
func parseTrustedJWKSHosts(value string) (map[string][]string, error) {
	trusted := map[string][]string{}
	for _, entry := range strings.Fields(value) {
		issuer, hosts, ok := strings.Cut(entry, "=")
		if !ok || issuer == "" || hosts == "" {
			return nil, fmt.Errorf("OIDC_TRUSTED_JWKS_HOSTS entry %q is not issuer=host[,host]", entry)
		}
		for _, host := range strings.Split(hosts, ",") {
			if host == "" || strings.ContainsAny(host, "=/:?#") {
				return nil, fmt.Errorf("OIDC_TRUSTED_JWKS_HOSTS entry %q contains an invalid host", entry)
			}
			trusted[issuer] = append(trusted[issuer], host)
		}
	}
	return trusted, nil
}

// requireImmutableSubject reports whether an Actions token's subject must
// carry the numeric owner and repository IDs. Off by default because a
// repository has to opt into the immutable subject format first; turn it
// on once every caller has.
func requireImmutableSubject() bool {
	return os.Getenv("OIDC_REQUIRE_IMMUTABLE_SUBJECT") == "1"
}

// splitList parses a comma- or whitespace-separated environment value.
func splitList(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// loadPrivateKey returns the App's PEM, preferring an inline value
// (PRIVATE_KEY) over a file path (PRIVATE_KEY_PATH). Exactly one must
// be set.
func loadPrivateKey() ([]byte, error) {
	if pem := os.Getenv("PRIVATE_KEY"); pem != "" {
		return []byte(pem), nil
	}
	path := os.Getenv("PRIVATE_KEY_PATH")
	if path == "" {
		return nil, errors.New("PRIVATE_KEY or PRIVATE_KEY_PATH is required")
	}
	return os.ReadFile(path)
}
