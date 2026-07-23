# Makefile for libgen-mcp
# Run `make help` for the list of targets.
# Static analysis mirrors the sibling project gitlab-mcp-server:
# golangci-lint (bundles govet, staticcheck, gosec, ...) + govulncheck.

.PHONY: all build build-probe build-all run version \
        test test-short test-race test-e2e eval coverage cover-check \
        lint golangci-lint govulncheck analyze fmt tidy vet \
        format-md-tables check-md-tables \
        godoc-audit godoc-check \
        gen-llms check-llms audit-tokens \
        install-tools release-check check-server-json check-mcpb-manifest check-lhm-manifest \
        mcpb publish-lobehub sonar clean help \
        build-linux-amd64 build-linux-arm64 build-darwin-amd64 \
        build-darwin-arm64 build-windows-amd64 build-windows-arm64

# ─── Variables ──────────────────────────────────────────────────────────────
BINARY_NAME := libgen-mcp
CMD_PATH    := ./cmd/server
PROBE_PATH  := ./cmd/probe
PKGS        := ./cmd/... ./internal/...

GO_ANALYSIS_PKGS := ./...
GO_ANALYSIS_TAGS := e2e
COVERAGE_MIN     := 85
COVERAGE_PKGS    := ./internal/...

# Version from the VERSION file (single source of truth); commit from git.
# Use shell `cat` (portable to GNU Make 3.81 on macOS; `$(file ...)` needs Make 4+).
VERSION := $(strip $(shell cat VERSION 2>/dev/null))
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

# Portable helpers (Windows vs POSIX).
ifeq ($(OS),Windows_NT)
  BINARY_EXT := .exe
  MKDIR_P    = if not exist $(subst /,\,$1) mkdir $(subst /,\,$1)
  RM_RF      = if exist $(subst /,\,$1) rmdir /s /q $(subst /,\,$1)
  RM_F       = if exist $(subst /,\,$1) del /q $(subst /,\,$1)
else
  BINARY_EXT :=
  MKDIR_P    = mkdir -p $1
  RM_RF      = rm -rf $1
  RM_F       = rm -f $1
endif

all: build ## Build the server binary (default)

# ─── Build ──────────────────────────────────────────────────────────────────
build: ## Build the server binary into dist/
	$(call MKDIR_P,dist)
	go build -trimpath -buildmode=pie -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)$(BINARY_EXT) $(CMD_PATH)

build-probe: ## Build the probe diagnostic CLI into dist/
	$(call MKDIR_P,dist)
	go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-probe$(BINARY_EXT) $(PROBE_PATH)

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64 build-windows-arm64 ## Cross-compile the server for all platforms

build-linux-amd64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-linux-amd64 $(CMD_PATH)

build-linux-arm64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-linux-arm64 $(CMD_PATH)

build-darwin-amd64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-darwin-amd64 $(CMD_PATH)

build-darwin-arm64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-darwin-arm64 $(CMD_PATH)

build-windows-amd64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-windows-amd64.exe $(CMD_PATH)

build-windows-arm64:
	$(call MKDIR_P,dist)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)-windows-arm64.exe $(CMD_PATH)

run: ## Run the server on stdio
	go run $(CMD_PATH)

version: ## Print the version that would be stamped into a build
	@echo $(VERSION) $(COMMIT)

# ─── Test ───────────────────────────────────────────────────────────────────
test: ## Run all tests with a coverage profile
	go test -count=1 -coverprofile=coverage.out $(PKGS)

test-short: ## Run tests without the coverage profile
	go test -count=1 $(PKGS)

test-race: ## Run all tests under the race detector
	go test -count=1 -race $(PKGS)

test-e2e: ## Run the gated live e2e suite against the real site (needs network; loads .env if present)
	set -a; [ -f .env ] && . ./.env; set +a; \
	LIBGEN_E2E=1 go test -tags e2e -timeout 600s -count=1 ./test/e2e/

eval: ## Run the LIVE LLM-driven eval harness (needs ANTHROPIC_API_KEY; real API + mirrors + downloads; loads .env if present)
	set -a; [ -f .env ] && . ./.env; set +a; \
	LIBGEN_EVAL=1 go run -tags eval ./cmd/eval

coverage: test ## Generate an HTML coverage report (coverage.html)
	go tool cover -html=coverage.out -o coverage.html

cover-check: ## Fail if coverage over internal/ is below COVERAGE_MIN
	go test -count=1 -coverpkg=$(COVERAGE_PKGS) -coverprofile=coverage.internal.out $(COVERAGE_PKGS)
	@go tool cover -func=coverage.internal.out | grep total
	@COVERAGE=$$(go tool cover -func=coverage.internal.out | grep total | awk '{print $$3}' | tr -d '%'); \
	if awk "BEGIN {exit !($$COVERAGE + 0 < $(COVERAGE_MIN) + 0)}"; then \
		echo "FAIL: coverage $$COVERAGE% is below minimum $(COVERAGE_MIN)%"; exit 1; \
	fi; \
	echo "PASS: coverage $$COVERAGE% meets minimum $(COVERAGE_MIN)%"

# ─── Static Analysis ────────────────────────────────────────────────────────
lint: golangci-lint govulncheck ## Run all static analysis (golangci-lint + govulncheck)

analyze: lint ## Alias for lint

golangci-lint: ## Verify config, check formatting, and run golangci-lint
	@echo "=== golangci-lint config verify ==="
	golangci-lint config verify
	@echo "=== golangci-lint fmt --diff ==="
	golangci-lint fmt --diff
	@echo "=== golangci-lint run ==="
	golangci-lint run --build-tags $(GO_ANALYSIS_TAGS) $(GO_ANALYSIS_PKGS)

govulncheck: ## Scan for known vulnerabilities (govulncheck)
	@echo "=== govulncheck ==="
	govulncheck -tags $(GO_ANALYSIS_TAGS) $(GO_ANALYSIS_PKGS)

fmt: ## Apply formatters (goimports, gofumpt, gci)
	golangci-lint fmt

vet: ## Run go vet
	go vet $(GO_ANALYSIS_PKGS)

tidy: ## Tidy go.mod / go.sum
	go mod tidy

# ─── Documentation ──────────────────────────────────────────────────────────
format-md-tables: ## Normalize Markdown pipe tables in README.md and docs/
	go run ./cmd/format_md_tables/

check-md-tables: ## Fail if any Markdown table needs formatting (CI mode)
	go run ./cmd/format_md_tables/ --check

godoc-audit: ## Report missing/malformed Go doc comments (Markdown)
	go run ./cmd/godoc_tool/ audit --format=markdown

godoc-check: ## Fail if any Go doc comments are missing/malformed (CI mode)
	go run ./cmd/godoc_tool/ audit --fail-on-findings

gen-llms: ## Generate llms.txt and llms-full.txt from the registered tools
	go run ./cmd/gen_llms/

check-llms: ## Fail if llms.txt/llms-full.txt are stale or structurally invalid (CI mode)
	go run ./cmd/gen_llms/ --check

audit-tokens: ## Report the LLM context-window footprint (tokens) of the tool definitions
	go run ./cmd/audit_tokens/

# ─── Tools / Release ────────────────────────────────────────────────────────
install-tools: ## Install golangci-lint and govulncheck
	@echo "Installing static analysis tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	@echo "All tools installed."

release-check: ## Validate the GoReleaser config
	goreleaser check

check-server-json: ## Verify server.json parses and matches the VERSION file
	@jq empty server.json && echo "server.json: valid JSON"
	@SJ=$$(jq -r '.version' server.json); VF=$$(cat VERSION | tr -d '[:space:]'); \
	if [ "$$SJ" != "$$VF" ]; then \
		echo "FAIL: server.json version ($$SJ) != VERSION ($$VF)"; exit 1; \
	fi; \
	echo "server.json version matches VERSION ($$VF)"

check-mcpb-manifest: ## Verify mcpb/manifest.json parses and matches the VERSION file
	@jq empty mcpb/manifest.json && echo "mcpb/manifest.json: valid JSON"
	@MV=$$(jq -r '.version' mcpb/manifest.json); VF=$$(cat VERSION | tr -d '[:space:]'); \
	if [ "$$MV" != "$$VF" ]; then \
		echo "FAIL: mcpb/manifest.json version ($$MV) != VERSION ($$VF)"; exit 1; \
	fi; \
	echo "mcpb/manifest.json version matches VERSION ($$VF)"

check-lhm-manifest: ## Verify lhm.plugin.json parses and matches the VERSION file
	@jq empty lhm.plugin.json && echo "lhm.plugin.json: valid JSON"
	@LV=$$(jq -r '.version' lhm.plugin.json); VF=$$(cat VERSION | tr -d '[:space:]'); \
	if [ "$$LV" != "$$VF" ]; then \
		echo "FAIL: lhm.plugin.json version ($$LV) != VERSION ($$VF)"; exit 1; \
	fi; \
	echo "lhm.plugin.json version matches VERSION ($$VF)"

mcpb: ## Build the .mcpb Claude Desktop bundle (needs GoReleaser artifacts in dist/)
	bash scripts/build-mcpb.sh $(VERSION)

## publish-lobehub: publish the current version to the LobeHub Marketplace.
## Reads lhm.plugin.json (version kept in sync by scripts/update-server-json-sha.sh
## on each release) and posts it via the @lobehub/market-cli. Requires a one-time
## interactive `lhm login` + `lhm github connect` first — LobeHub has no
## non-interactive publish path, so this cannot run in CI.
publish-lobehub:
	@command -v node >/dev/null || { echo "ERROR: Node.js >= 22 is required"; exit 1; }
	@NODE_MAJOR=$$(node -v | sed 's/^v\([0-9]*\).*/\1/'); \
	if [ "$$NODE_MAJOR" -lt 22 ]; then echo "ERROR: Node.js >= 22 is required (found $$(node -v))"; exit 1; fi
	@command -v jq >/dev/null || { echo "ERROR: jq is required"; exit 1; }
	@VER=$$(tr -d '[:space:]' < VERSION); \
	MVER=$$(jq -r '.version' lhm.plugin.json); \
	if [ "$$VER" != "$$MVER" ]; then \
		echo "ERROR: VERSION ($$VER) != lhm.plugin.json version ($$MVER); run a release stamp first"; exit 1; \
	fi; \
	echo "Publishing jmrplens-libgen-mcp v$$VER to LobeHub..."; \
	npx -y @lobehub/market-cli plugin publish --dir "$(CURDIR)"

sonar: ## Run the SonarCloud scanner locally (needs sonar-scanner + SONAR_TOKEN)
	@command -v sonar-scanner >/dev/null || { echo "sonar-scanner not installed"; exit 1; }
	go test -count=1 -coverprofile=coverage.out $(PKGS)
	sonar-scanner -Dsonar.host.url=https://sonarcloud.io

# ─── Housekeeping ───────────────────────────────────────────────────────────
clean: ## Remove build and coverage artifacts
	$(call RM_RF,dist)
	$(call RM_F,coverage.out)
	$(call RM_F,coverage.internal.out)
	$(call RM_F,coverage.html)

help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
