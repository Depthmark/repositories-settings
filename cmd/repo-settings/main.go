// repo-settings is the declarative GitHub repository configuration-as-code
// service. It listens for GitHub App webhooks, exposes /api/reconcile and
// /api/validate, and (optionally) drives a periodic batch reconcile.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
	"github.com/Depthmark/repositories-settings/internal/server"
	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	log := logger.New(env("LOG_LEVEL", "info"))
	slog.SetDefault(log)

	addr := env("ADDR", ":8080")
	apiURL := env("GITHUB_API_URL", "https://api.github.com")

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
		if os.Getenv("ALLOW_UNAUTHENTICATED") == "1" {
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

	// Settings loader closure: reads .github/settings/*.yml from the
	// target repo at the given ref. No org/suborg layering yet — that's
	// out of scope for this slice (fold into Resolve once the org admin
	// repo is wired).
	settingsLoader := func(ctx context.Context, repo config.Repo, ref string) (*config.Settings, error) {
		return config.Load(ctx, cl, repo, ref)
	}

	srv := server.New(addr, server.Deps{
		Logger:            log,
		Client:            cl,
		Reconciler:        rec,
		WebhookSecret:     []byte(os.Getenv("WEBHOOK_SECRET")),
		APIToken:          os.Getenv("API_TOKEN"),
		Registry:          registry,
		Settings:          settingsLoader,
		AppSlug:           env("APP_SLUG", "repo-settings"),
		DisabledResources: disabled,
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
