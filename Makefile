BINARY  := katana
MODULE  := github.com/adaptive-scale/katana
BIN_DIR := bin
DIST_DIR:= dist

# Version comes from git when available, otherwise "dev". Override with
# `make build VERSION=v1.2.3`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/cli.Version=$(VERSION)

GO       ?= go
GOFLAGS  ?=
PLATFORMS?= darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.DEFAULT_GOAL := build

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the katana binary into bin/
	$(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BINARY) .

.PHONY: install
install: ## Install katana into $GOBIN (or $GOPATH/bin)
	$(GO) install $(GOFLAGS) -ldflags '$(LDFLAGS)' .

.PHONY: test
test: ## Run the test suite
	$(GO) test $(GOFLAGS) ./...

.PHONY: cover
cover: ## Run tests and open the HTML coverage report
	$(GO) test $(GOFLAGS) -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format all Go source
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if any Go source is unformatted
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	$(GO) mod tidy

.PHONY: check
check: fmt-check vet test ## Run all checks (format, vet, test)

.PHONY: run
run: ## Run katana; pass arguments with ARGS="generate --dry-run"
	$(GO) run . $(ARGS)

.PHONY: release
release: ## Cross-compile release binaries into dist/
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		out="$(DIST_DIR)/$(BINARY)_$(VERSION)_$${os}_$${arch}$$ext"; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o "$$out" . || exit 1; \
	done
	@$(MAKE) --no-print-directory checksums

.PHONY: checksums
checksums: ## Write dist/checksums.txt for the built release binaries
	@cd $(DIST_DIR) && \
	if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum $(BINARY)_$(VERSION)_* > checksums.txt; \
	else \
		shasum -a 256 $(BINARY)_$(VERSION)_* > checksums.txt; \
	fi
	@echo "wrote $(DIST_DIR)/checksums.txt"

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) $(DIST_DIR) coverage.out
