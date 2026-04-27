# ─────────────────────────────────────────────────────────────────────────────
# Stage 1: Builder
# Compile the Go binary with CGO disabled for static linking.
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

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
    go build -trimpath -ldflags="-s -w" -o /template-go ./cmd/template-go

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2: Runtime
# Distroless static image — no shell, no package manager, minimal attack surface.
# ─────────────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

LABEL maintainer="Alexandre Delisle <oss@adelisle.com>"
LABEL description="template-go — Depthmark Go service template"
# x-release-please-start-version
LABEL version="0.1.1"
# x-release-please-end

COPY --from=builder /template-go /template-go

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/template-go"]
