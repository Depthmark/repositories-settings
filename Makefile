.PHONY: build test test-race coverage lint vet fmt fmt-check vuln-check docker docker-load ci clean

# ─── Build ───────────────────────────────────────────────────────────────────

build:
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/template-go ./cmd/template-go

# ─── Test ────────────────────────────────────────────────────────────────────

test:
	go test ./...

test-race:
	go test -race ./...

coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@echo "---"
	@echo "HTML report: go tool cover -html=coverage.out -o coverage.html"

# ─── Lint & Vet ──────────────────────────────────────────────────────────────

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "ERROR: files not formatted:" && gofmt -l . && exit 1)

# ─── Security ────────────────────────────────────────────────────────────────

vuln-check:
	govulncheck ./...

# ─── Docker ──────────────────────────────────────────────────────────────────

docker:
	docker build -t template-go:local .

docker-load:
	docker build -t template-go:local --load .

# ─── CI (local) ──────────────────────────────────────────────────────────────

ci: vet fmt-check lint test-race build

# ─── Act (local GitHub Actions) ──────────────────────────────────────────────

act:
	act pull_request --workflows .github/workflows/ci.yml

act-lint:
	act pull_request --workflows .github/workflows/ci.yml --job lint

act-test:
	act pull_request --workflows .github/workflows/ci.yml --job test

# ─── Clean ───────────────────────────────────────────────────────────────────

clean:
	rm -rf bin/ coverage.out coverage.html
