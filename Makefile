BINARY  := quet
PKG     := github.com/8bu/quet
PLATFORMS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

# A tag wins; otherwise fall back to the revision so dev builds stay traceable.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%d)

LDFLAGS := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT) -X $(PKG)/internal/version.Date=$(DATE)

.PHONY: help build install test race vet fmt check release clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-9s\033[0m %s\n", $$1, $$2}'

build: ## Build ./quet for this machine
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/quet

install: ## Install the CLI into GOBIN
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/quet

test: ## Run the test suite
	go test ./...

race: ## Run the test suite with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Fail if any file needs gofmt
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmt vet test ## Everything CI would run

release: ## Cross-compile every platform into dist/ with checksums
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; out=dist/$(BINARY)_$${os}_$${arch}; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/quet || exit 1; \
	done
	@cd dist && { command -v shasum >/dev/null 2>&1 && shasum -a 256 $(BINARY)_* || sha256sum $(BINARY)_*; } > SHA256SUMS
	@cat dist/SHA256SUMS

clean: ## Remove build and release output
	rm -rf dist $(BINARY)
