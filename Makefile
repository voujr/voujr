# voujr — developer Makefile
BINARY      := voujr
PKG         := github.com/voujr/voujr
CMD         := ./cmd/voujr
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS=":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Resolve and pin module dependencies
	go mod tidy

.PHONY: build
build: ## Build the binary into ./bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(CMD)

.PHONY: run
run: ## Run the agent against the current kube-context
	go run $(CMD)

.PHONY: test
test: ## Run unit tests
	go test -count=1 ./...

.PHONY: test-race
test-race: ## Run unit tests with the race detector (requires CGO + a C compiler)
	CGO_ENABLED=1 go test -race -count=1 ./...

.PHONY: lint
lint: ## Static analysis (requires golangci-lint)
	golangci-lint run ./...

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: vulncheck
vulncheck: ## Scan for known vulnerabilities (requires govulncheck)
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: snapshot
snapshot: ## Build a local release snapshot for the host (requires goreleaser)
	goreleaser build --snapshot --clean --single-target

.PHONY: release-check
release-check: ## Validate the GoReleaser config
	goreleaser check

.PHONY: docker
docker: ## Build the container image
	docker build -t ghcr.io/voujr/$(BINARY):$(VERSION) -f deploy/Dockerfile .

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist
