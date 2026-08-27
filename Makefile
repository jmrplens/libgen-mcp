# Makefile for libgen-mcp
# Run `make help` for the list of targets.
# Static analysis mirrors the sibling project gitlab-mcp-server:
# golangci-lint (bundles govet, staticcheck, gosec, ...) + govulncheck.

.PHONY: all build build-probe build-all run version \
        test test-short test-race test-e2e test-e2e-http eval coverage cover-check \
        lint golangci-lint govulncheck analyze fmt tidy vet \
        format-md-tables check-md-tables check-doc-links \
        godoc-audit godoc-check \
        gen-llms check-llms gen-lhm-manifest check-lhm-manifest \
        gen-icon-webp check-icon-webp \
        eval-only eval-pages check-eval-pages audit-tokens audit-surface-quality \
        validate-http-stateless \
        install-tools release-check check-manifests \
        mcpb publish-lobehub sonar clean help \
        build-linux-amd64 build-linux-arm64 build-darwin-amd64 \
        build-darwin-arm64 build-windows-amd64 build-windows-arm64

# ─── Variables ──────────────────────────────────────────────────────────────
BINARY_NAME := libgen-mcp
CMD_PATH    := ./cmd/server
PROBE_PATH  := ./cmd/probe
PKGS        := ./cmd/... ./internal/...

# Knobs for validate-http-stateless; both are positional args to the script, so
# each needs a default or the other would shift into the wrong slot.
MODE ?= binary
PORT ?= 18080

GO_ANALYSIS_PKGS := ./...
GO_ANALYSIS_TAGS := e2e,eval,httpe2e
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

test-e2e: ## Run the gated live e2e suite against the real site (needs network; the suite loads .env itself)
	set -a; [ -f .env ] && . ./.env; set +a; \
	LIBGEN_E2E=1 go test -tags e2e -timeout 900s -count=1 ./test/e2e/

test-e2e-http: ## Run the HTTP transport end-to-end module against the real binary (no network; nginx cases skip without Docker)
	go test -tags httpe2e -count=1 -timeout 900s ./test/e2e/http/

eval: ## Run the LIVE LLM-driven eval harness (needs ANTHROPIC_API_KEY; real API + mirrors + downloads; loads .env if present)
	set -a; [ -f .env ] && . ./.env; set +a; \
	LIBGEN_EVAL=1 go run -tags eval ./cmd/eval --record eval-record.jsonl \
	  --results-doc cmd/eval/testdata/latest-run.md
	@echo "Regenerating the results pages from that run..."
	go run ./cmd/gen_eval_pages/

eval-only: ## Re-run named eval scenarios and merge them into the published table (ONLY=S61,S62)
	@[ -n "$(ONLY)" ] || { echo "ONLY is required, e.g. make eval-only ONLY=S61,S62"; exit 2; }
	set -a; [ -f .env ] && . ./.env; set +a; \
	LIBGEN_EVAL=1 go run -tags eval ./cmd/eval --only $(ONLY) \
	  --results-doc cmd/eval/testdata/latest-run.md
	@echo "Merging those scenarios into the results pages..."
	go run ./cmd/gen_eval_pages/

coverage: test ## Generate an HTML coverage report (coverage.html)
	go tool cover -html=coverage.out -o coverage.html

cover-check: ## Fail if coverage over internal/ is below COVERAGE_MIN
	go test -count=1 -coverpkg=$(COVERAGE_PKGS) -coverprofile=coverage.internal.out $(COVERAGE_PKGS)
	@go tool cover -func=coverage.internal.out | grep '^total:'
	@# The summary line is anchored: a plain "total" also matches any function whose
	@# name contains it (e.g. totalSizeLocked), which yields two values and turns the
	@# comparison below into an awk syntax error that silently passes the gate.
	@COVERAGE=$$(go tool cover -func=coverage.internal.out | grep '^total:' | awk '{print $$3}' | tr -d '%'); \
	if [ -z "$$COVERAGE" ]; then \
		echo "FAIL: no total coverage line in coverage.internal.out"; exit 1; \
	fi; \
	if ! awk "BEGIN {exit !($$COVERAGE + 0 >= $(COVERAGE_MIN) + 0)}" 2>/dev/null; then \
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

check-doc-links: ## Fail if any tracked Markdown/MDX local link or path is broken
	node scripts/check-doc-links.mjs

godoc-audit: ## Report missing/malformed Go doc comments (Markdown)
	go run ./cmd/godoc_tool/ audit --format=markdown

godoc-check: ## Fail if any Go doc comments are missing/malformed (CI mode)
	go run ./cmd/godoc_tool/ audit --fail-on-findings

gen-llms: ## Generate llms.txt and llms-full.txt from the registered tools
	go run ./cmd/gen_llms/

check-llms: ## Fail if llms.txt/llms-full.txt are stale or structurally invalid (CI mode)
	go run ./cmd/gen_llms/ --check

gen-lhm-manifest: ## Regenerate the tools/prompts arrays in lhm.plugin.json from the registered surface
	go run ./cmd/gen_lhm_manifest/

check-lhm-manifest: ## Fail if lhm.plugin.json no longer matches the registered surface (CI mode)
	go run ./cmd/gen_lhm_manifest/ --check

gen-tool-schema: ## Regenerate site/src/data/tool-schema.json from the registered surface
	go run ./cmd/gen_tool_schema/

check-tool-schema: ## Fail if site/src/data/tool-schema.json is stale (CI mode)
	go run ./cmd/gen_tool_schema/ --check

# The WebP icon assets are compared byte for byte, which makes the renderer's
# version part of the toolchain rather than an implementation detail: librsvg's
# stroke antialiasing changed between 2.54 (Debian 12) and 2.58, and the two
# disagree on three of the nine icons. So these targets do not assume the local
# librsvg is usable — they ask the tool itself, via --probe, and fall back to a
# pinned image when it is not, so every machine emits identical bytes.
#
# The threshold is deliberately NOT restated here; it lives beside the
# comparison it governs, in cmd/gen_icon_webp (minLibrsvg).
#
# The tag follows go.mod so the image cannot drift behind the toolchain the
# repository actually builds with. Override to move it: make ICON_IMAGE=...
ICON_IMAGE ?= golang:$(shell sed -n 's/^go \([0-9]*\.[0-9]*\).*/\1/p' go.mod)-trixie

# The one path restated from the Go side (outDir): the container writes as
# root, so a non-root host would otherwise be left with root-owned assets.
ICON_WEBP_DIR := internal/toolutil/icons/webp

# run-icon-tool runs gen_icon_webp with $(1), locally when this machine can
# reproduce the committed bytes and in $(ICON_IMAGE) when it cannot. The final
# branch re-runs --probe for one reason only: to let it print its own diagnosis
# rather than restating it in a second voice that could drift.
define run-icon-tool
@if go run ./cmd/gen_icon_webp/ --probe 2>/dev/null; then \
	go run ./cmd/gen_icon_webp/ $(1); \
elif command -v docker >/dev/null 2>&1; then \
	echo "local librsvg cannot reproduce the committed assets; running in $(ICON_IMAGE)"; \
	MODCACHE="$$(go env GOMODCACHE)"; MOUNT=""; \
	[ -d "$$MODCACHE" ] && MOUNT="-v $$MODCACHE:/go/pkg/mod"; \
	docker run --rm $$MOUNT -v "$(CURDIR)":/src -w /src -e DEBIAN_FRONTEND=noninteractive $(ICON_IMAGE) sh -c \
		"apt-get update -qq >/dev/null && apt-get install -y -qq librsvg2-bin webp >/dev/null && go run ./cmd/gen_icon_webp/ $(1)" \
		&& chown -R "$$(id -u):$$(id -g)" "$(ICON_WEBP_DIR)"; \
else \
	echo "make: no librsvg that can reproduce the committed assets, and no docker to fall back to" >&2; \
	go run ./cmd/gen_icon_webp/ --probe; \
fi
endef

## gen-icon-webp: regenerate the light/dark WebP fallbacks for every icon in
## internal/toolutil/icons.go. Maintainer-only, and deliberately NOT a CI gate:
## the generated .webp files under internal/toolutil/icons/webp/ are committed,
## so ordinary builds never invoke this. Needs either a librsvg >= 2.58 and
## cwebp on PATH, or docker. Run it after adding or editing an icon.
gen-icon-webp:
	$(call run-icon-tool,)

## check-icon-webp: verify the committed WebP icon assets still match
## icons.go. Same toolchain requirement as gen-icon-webp.
check-icon-webp:
	$(call run-icon-tool,--check)

eval-pages: ## Regenerate the evaluator results pages (pass DOC=path to also refresh the run table)
	go run ./cmd/gen_eval_pages/ $(if $(DOC),--results-doc $(DOC))

check-eval-pages: ## Fail if the evaluator results pages are stale (CI mode)
	go run ./cmd/gen_eval_pages/ --check

audit-tokens: ## Report the LLM context-window footprint (tokens) of the tool definitions
	go run ./cmd/audit_tokens/

audit-surface-quality: ## Fail if the MCP tool surface violates a quality convention (CI gate)
	go run ./cmd/audit_surface_quality/

validate-http-stateless: ## Smoke-validate the stateless streamable HTTP transport against a real server (MODE=binary|docker, PORT=18080)
	./scripts/validate-http-stateless.sh $(MODE) $(PORT)

# ─── Tools / Release ────────────────────────────────────────────────────────
install-tools: ## Install golangci-lint, govulncheck and goreleaser
	@echo "Installing static analysis tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/goreleaser/goreleaser/v2@latest
	@echo "All tools installed."

release-check: ## Validate the GoReleaser config
	goreleaser check

# Every JSON manifest that mirrors the VERSION file. A manifest listed here is
# gated; one that is not silently ships the previous version's number, which is
# how .plugin/plugin.json spent a release cycle claiming to be 1.2.0.
VERSION_MANIFESTS := server.json mcpb/manifest.json lhm.plugin.json .plugin/plugin.json

check-manifests: ## Verify every version-bearing manifest parses and matches the VERSION file
	@VF=$$(tr -d '[:space:]' < VERSION); \
	for f in $(VERSION_MANIFESTS); do \
		jq empty "$$f" || exit 1; \
		MV=$$(jq -r '.version' "$$f"); \
		if [ "$$MV" != "$$VF" ]; then \
			echo "FAIL: $$f version ($$MV) != VERSION ($$VF)"; exit 1; \
		fi; \
		echo "$$f: valid JSON, version matches VERSION ($$VF)"; \
	done

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
	fi
	@$(MAKE) --no-print-directory check-lhm-manifest
	@VER=$$(tr -d '[:space:]' < VERSION); \
	echo "Updating jmrplens-libgen-mcp to v$$VER on LobeHub..."; \
	npx -y @lobehub/market-cli plugin update --dir "$(CURDIR)"

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
