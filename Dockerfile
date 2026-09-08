# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Builder
# Compile the Go binary with CGO disabled for static linking.
# ─────────────────────────────────────────────────────────────────────────────
# Pinned by digest: a mutable tag lets the builder change under a
# reproducible source tree. Keep this Go patch aligned with go.mod and
# rerun govulncheck when changing the toolchain.
FROM golang:1.26.7-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS builder

WORKDIR /build

# Copy dependency files first (better layer caching).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download && go mod verify

# Copy source.
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build static binary.
# sharing=locked: prevents cache corruption from parallel multi-platform builds.
# -trimpath: strips local filesystem paths for reproducibility.
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /repo-settings ./cmd/repo-settings

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# Distroless static image — no shell, no package manager, minimal attack surface.
# ─────────────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

LABEL maintainer="Alexandre Delisle <oss@adelisle.com>"
LABEL description="repo-settings — declarative GitHub repository configuration-as-code"
# x-release-please-start-version
LABEL version="0.1.1"
# x-release-please-end

COPY --from=builder /repo-settings /repo-settings

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/repo-settings"]
