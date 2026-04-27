# template-go

Depthmark Go service template with production-ready CI/CD, security, and Docker packaging.

## Contents

- [x] Go project structure (`cmd/`, `internal/`)
- [x] Minimal HTTP server with health endpoints
- [x] Unit tests
- [x] Multi-stage Dockerfile (distroless, nonroot)
- [x] CI via reusable workflows (lint, test, build, workflow security scanning)
- [x] Release automation (Release Please + STS token exchange)
- [x] Docker build, sign, and attest via reusable workflow
- [x] Dependabot (Go modules, Docker, GitHub Actions)
- [x] PR auto-labelling
- [x] Local development tooling (Makefile, act)

## Quick Start

1. **Use this template** — click "Use this template" on GitHub.
2. **Rename the module** — update `go.mod` and all import paths.
3. **Rename the binary** — update `cmd/template-go/`, `Dockerfile`, and `Makefile`.
4. **Update STS policy** — edit `.github/sts/depthmark-release-bot/release.sts.yaml` with your repo name.
5. **Update release config** — edit `release-please-config.json` with your package name.
6. **Start building** — add your application code in `internal/`.

## Local Development

```bash
make ci            # Run all local checks (vet, fmt, lint, test, build)
make test-race     # Run tests with race detector
make coverage      # Generate coverage report
make lint          # Run golangci-lint
make vuln-check    # Run govulncheck
make docker        # Build Docker image locally
make act           # Run CI workflow locally with act
```

## CI/CD

| Workflow | Trigger | Description |
|----------|---------|-------------|
| **CI** | PR / push to main | Lint, test, build (reusable `ci-go.yml`) + workflow security scan (`ci-actions.yml`) |
| **Release Please** | Push to main | Automated version bumps and changelog via STS token exchange |
| **Release** | Tag `v*` / manual | CI gate, then Docker build, push, cosign sign, and attest to GHCR |
| **Labeler** | PR events | Auto-label PRs based on changed files |

## Security

- **Minimal permissions** — all workflows default to `permissions: {}`, grant only what is needed per job.
- **Pinned actions** — every GitHub Action is pinned to a full commit SHA.
- **STS token exchange** — Release Please uses OIDC-based token exchange, not PATs.
- **Trust policy** — `.github/sts/` defines exactly which workflow can request tokens.
- **Distroless runtime** — no shell, no package manager in the production image.
- **Non-root user** — container runs as `nonroot` (uid 65532).
- **Static binary** — `CGO_ENABLED=0` with `-trimpath` for reproducible builds.
- **Cosign signing** — released images are signed with keyless cosign via Sigstore OIDC.
- **Dependabot** — automated weekly updates for Go modules, Docker base images, and GitHub Actions.
- **Workflow scanning** — actionlint, zizmor, and poutine run on every CI pass.

## Project Structure

```
.
├── cmd/template-go/       # Application entry point
├── internal/server/       # HTTP server package
├── .github/
│   ├── workflows/         # CI, release, release-please, labeler
│   ├── sts/               # STS trust policies
│   ├── labeler.yml        # PR labeler configuration
│   └── dependabot.yml     # Dependency update configuration
├── Dockerfile             # Multi-stage distroless build
├── Makefile               # Local development targets
├── release-please-config.json
└── .release-please-manifest.json
```

## License

[MIT](LICENSE)
