# Makefile for libgen-mcp
# Run `make help` for the list of targets.
# Static analysis mirrors the sibling project gitlab-mcp-server:
# golangci-lint (bundles govet, staticcheck, gosec, ...) + govulncheck.

.PHONY: all build build-probe build-all run version \
        coverage-conditions coverage-mutants check-coverage-recipes \
        test test-short test-race test-e2e test-e2e-http test-e2e-stdio test-e2e-collector eval coverage cover-check \
        lint golangci-lint govulncheck check-binary-vulns test-binary-vulns godoc-check-binary-vulns analyze analyze-fix fmt tidy vet \
        gen-vex check-vex vex-release \
        format-md-tables check-md-tables check-doc-links gen-doc-versions check-doc-versions \
        godoc-audit godoc-check \
        gen-llms check-llms gen-lhm-manifest check-lhm-manifest \
        gen-icon-webp check-icon-webp \
        eval-only eval-pages check-eval-pages audit-tokens audit-surface-quality \
        check-install-buttons audit-gateway-chars check-gateway-chars \
        audit-md-escaping check-md-escaping gen-stats check-stats \
        bench-resources bench-resources-render check-bench-resources \
        audit-doc-names check-doc-names \
        audit-test-goroutines check-test-goroutines check-test-file-names \
        audit-test-subtests fix-test-subtests check-test-subtests \
        validate-http-stateless \
        install-tools release-check check-manifests check-stamper \
        check-server-json-packages check-supply-chain check-verify-published check-mcpb check-npm-launcher \
        check-homebrew-tap elf-standalone check-elf-standalone \
        check-pr-description audit-site-deps check-ci-scripts \
        mcpb gen-npm sync-npm-version validate-npm validate-npm-local \
        publish-npm-dry publish-npm \
        gen-pypi validate-pypi validate-pypi-local publish-pypi-dry publish-pypi check-pypi \
        publish-lobehub sonar clean help \
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
# Every build tag in the tree. A plain `golangci-lint run` skips every tagged
# file, which is how a whole harness can go unanalyzed — so a new tag belongs
# here in the same change that introduces it.
GO_ANALYSIS_TAGS := e2e,eval,httpe2e,stdioe2e,collectore2e
COVERAGE_MIN     := 90
# Everything this module builds is measured. The rest of cmd/ used to be out,
# on the premise that build tooling is gated by its own check-* targets rather
# than by a coverage number — but excluding a package hides more than a number,
# which this repository learned once already when cmd/gen_tool_schema shipped
# with no test file at all and nothing reported it. The rule that caught that
# was prose; the exclusion was configuration, and configuration wins.
#
# The three places this is written have to agree — here, the CI profile, and
# sonar-project.properties — or a package is counted and uninstrumented, which
# reports as 0% and is not.
#
# cmd/eval is in the list and contributes nothing, because its files are behind
# the eval build tag and CI does not set it. That is the honest state rather
# than a gap: the harness is measured by running it, which costs Anthropic
# tokens and reaches real mirrors.
COVERAGE_PKGS    := ./internal/... ./cmd/...
# The same list as one comma-separated argument, for -coverpkg.
COVERAGE_COVERPKG := ./internal/...,./cmd/...

# The one nested Go module, cmd/audit_binary_vulns, which keeps golang.org/x/vuln
# and its x/tools, x/mod and x/telemetry out of the server's go.mod. `./...` at
# the root does not descend into it, so every Go gate names it on purpose:
# vet, golangci-lint, govulncheck and godoc-check run there too, and its tests
# run under test-binary-vulns against their own COVERAGE_MIN floor. It is
# outside COVERAGE_PKGS, the CI profile and Sonar's coverage for the same
# reason cmd/eval is: no profile the root module produces can carry a line of
# another module, so counting it there would report it as 0% rather than
# measure it.
VULNGATE_DIR := cmd/audit_binary_vulns

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
# No -buildmode=pie here either, so a local build is the same shape as a released
# one: on linux the flag is what gives a CGO-free Go binary a PT_INTERP, and a
# binary that names a loader is not the standalone artifact every install path in
# this project hands people. See the comment in .goreleaser.yml.
build: ## Build the server binary into dist/
	$(call MKDIR_P,dist)
	go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY_NAME)$(BINARY_EXT) $(CMD_PATH)

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

test-e2e-stdio: ## Run the stdio transport end-to-end module against the real binary over pipes (no network)
	go test -tags stdioe2e -count=1 -timeout 900s ./test/e2e/stdio/

test-e2e-collector: ## Run the real-OTLP-collector acceptance module (needs Docker; hand-run, no CI job)
	go test -tags collectore2e -count=1 -timeout 900s ./test/e2e/collector/

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

# The recipe is scripts/coverage-conditions.sh, which also pins gobco's
# version. gobco parses and type-checks every .go file of a directory together,
# whatever its build constraint says, so a package declaring one function per
# platform (cmd/server, internal/libgen, internal/pathguard) died with a
# redeclaration panic before measuring anything. The script stages such a
# package in a copy of the module under the temporary directory, holding only
# the files the go command builds here, blanks the constraint lines of the kept
# files whose constraint names more than the platform (a tag, a release, cgo),
# which gobco's own narrower build context would decline to instrument, runs
# gobco there, names the files it left out, and removes the copy whatever
# happens. The copy holds what `git ls-files -co --exclude-standard` lists, so
# no gitignored build output and no .env reaches the temporary directory. A
# package gobco could already read is run where it is, as before.
# TAGS names build tags for a package behind one, passed to go list and to
# gobco's go test alike, and a report that measured no condition (0/0) is
# refused.
coverage-conditions: ## Report the boolean conditions of PKG never evaluated both ways (gobco; PKG=./internal/netguard [TAGS=e2e])
	@test -n "$(PKG)" || { echo "usage: make coverage-conditions PKG=./internal/netguard [TAGS=e2e]"; exit 2; }
	@scripts/coverage-conditions.sh $(PKG) $(TAGS)

# The recipe is scripts/coverage-mutants.sh, and the reasons below are written
# out beside the code there as well.
#
# A package main is measured through a staged copy. gremlins picks the package
# whose tests decide a mutant's verdict by walking up from the mutated file
# until a directory name ends in the package clause's name, and falls back to
# the module path when none does. For `package main` in cmd/<tool> nothing
# matches, so every mutant ran `go test` on the module root, package libgenmcp,
# which has no test file and exits 0: cmd/format_md_tables reported 32 LIVED
# in under a second without its own tests ever running. The script copies such
# a package beside itself into <dir>.mutants-main, runs gremlins there, and
# removes the copy whatever happens: gremlins and every go test run in the
# background in a process group of their own, so an INT or TERM (what `docker
# stop` sends) stops them within seconds and the cleanup runs, rather than
# waiting behind a foreground child until the SIGKILL that skips it.
#
# The per-mutant timeout is derived from PKG's own baseline, because gremlins
# computes it as that baseline times a coefficient and applies no floor. The
# baseline is a run of the command gremlins times, `go test -cover
# -coverprofile ./<pkg>/...` from the module root under the tags and -coverpkg
# GREMLINS_FLAGS gives it, timed by the clock. It used to be read off the last
# line of an untagged `go test`, which is the test binary's run without the
# build, and guessed at 0.010s when that line carried no duration: on
# internal/version, which has no test file, that was a coefficient of 3001. A
# package with no test file under its tags is now refused instead, unless an
# integration run with a -coverpkg naming it lets the module's other tests
# measure it.
#
# MUTANT_BUDGET is the per-mutant budget in seconds and the coefficient is
# whatever reaches it, never below 8 so a slow package still gets a real
# multiple of its own runtime. MUTANT_BUDGET_FLOOR is what the budget may not go
# under: below it the budget is smaller than the cost of starting `go test` and
# every mutant is reported TIMED OUT having never run, which is not a kill, so
# it flatters exactly the packages it never managed to test. MUTANT_DEADLINE_MAX
# holds each mutant's deadline to about that many seconds, so a mutant that
# makes the tests hang is reported within the hour rather than holding a worker
# for as long as the coefficient allows. All three are plain numbers of seconds.
#
# The budget is sized for a compile as well as a run. gremlins copies the module
# into a directory per worker, and without -trimpath Go keys a compile on the
# package's directory, so the first mutant on each worker recompiled every
# package its test imports inside its own deadline. The script runs every go
# command, and gremlins, under -trimpath, and runs the per-mutant command once
# before gremlins starts, so a worker's copy finds everything compiled. Under
# -trimpath a binary no longer records where the toolchain is installed, so the
# script also exports GOROOT as `go env GOROOT` names it.
#
# A package that does not pass its own tests is refused rather than measured,
# and so is one whose subtree does not, or whose per-mutant command fails.
# Gremlins would otherwise run against a suite that already fails, where every
# mutant is reported KILLED: a perfect score over a broken package.
#
# -count=1 is what makes the coefficient mean what it says. Gremlins multiplies
# it by the elapsed time of its OWN coverage run, which Go's test cache answers
# instantly for a package whose files have not changed, so a second invocation
# would derive the budget from a fraction of a second and report a well-tested
# package as entirely timed out. The script exports it in GOFLAGS beside
# -trimpath.
MUTANT_BUDGET ?= 300
MUTANT_BUDGET_FLOOR ?= 10
MUTANT_DEADLINE_MAX ?= 3600
coverage-mutants: ## Mutation-test PKG with gremlins (PKG=./internal/netguard). The gate on a changed package is Lived 0 and Not covered 0
	@test -n "$(PKG)" || { echo "usage: make coverage-mutants PKG=./internal/netguard"; exit 2; }
	@scripts/coverage-mutants.sh $(PKG) $(MUTANT_BUDGET) $(MUTANT_BUDGET_FLOOR) $(MUTANT_DEADLINE_MAX)

check-coverage-recipes: ## Exercise the coverage-mutants and coverage-conditions scripts against a stand-in go (offline)
	python3 -m unittest discover -s scripts -p 'coverage_mutants_sh_test.py'
	python3 -m unittest discover -s scripts -p 'coverage_conditions_sh_test.py'

coverage: test ## Generate an HTML coverage report (coverage.html)
	go tool cover -html=coverage.out -o coverage.html

cover-check: ## Fail if coverage over internal/, cmd/server and cmd/internal is below COVERAGE_MIN
	go test -count=1 -coverpkg=$(COVERAGE_COVERPKG) -coverprofile=coverage.internal.out $(COVERAGE_PKGS)
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

# analyze runs every step and reports which ones failed, rather than stopping at
# the first. A sweep that stops at the first failure makes a contributor pay one
# round trip per finding, and the steps are independent: a stale generated file
# says nothing about whether the linter is happy.
#
# It is a deliberate pre-commit action, not a per-save one: the linter runs under
# every build tag and several generators run in check mode.
ANALYZE_STEPS = \
	golangci-lint \
	vet \
	godoc-check \
	check-md-escaping \
	check-gateway-chars \
	check-test-file-names \
	check-test-subtests \
	check-test-goroutines \
	check-md-tables \
	check-doc-versions \
	check-doc-links \
	check-stats \
	check-doc-names \
	check-bench-resources

analyze: ## Run the pre-commit sweep: lint, vet and the doc/surface gates, reporting every failure
	@failed=""; \
	for step in $(ANALYZE_STEPS); do \
		printf '\n\033[1m=== %s ===\033[0m\n' "$$step"; \
		$(MAKE) --no-print-directory "$$step" || failed="$$failed $$step"; \
	done; \
	if [ -n "$$failed" ]; then \
		printf '\n\033[31manalyze: failed:%s\033[0m\n' "$$failed"; exit 1; \
	fi; \
	printf '\n\033[32manalyze: every step passed\033[0m\n'

analyze-fix: ## Apply what the sweep can fix by itself, then run it
	golangci-lint fmt
	go run ./cmd/audit_test_subtests/ -fix
	go run ./cmd/format_md_tables/
	@$(MAKE) --no-print-directory analyze

golangci-lint: ## Verify config, check formatting, and run golangci-lint
	@echo "=== golangci-lint config verify ==="
	golangci-lint config verify
	@echo "=== golangci-lint fmt --diff ==="
	golangci-lint fmt --diff
	@echo "=== golangci-lint run ==="
	golangci-lint run --build-tags $(GO_ANALYSIS_TAGS) $(GO_ANALYSIS_PKGS)
	@echo "=== golangci-lint run ($(VULNGATE_DIR)) ==="
	cd $(VULNGATE_DIR) && golangci-lint run --config $(CURDIR)/.golangci.yml ./...

govulncheck: ## Scan for known vulnerabilities (govulncheck)
	@echo "=== govulncheck ==="
	govulncheck -tags $(GO_ANALYSIS_TAGS) $(GO_ANALYSIS_PKGS)
	govulncheck -C $(VULNGATE_DIR) ./...

# The scanners' question rather than ours: govulncheck above asks whether this
# module's code reaches a vulnerable symbol, while Trivy, Grype, osv-scanner and
# every SBOM consumer report any advisory against any module a binary's build
# information names. This builds the six targets .goreleaser.yml declares and
# holds each one to that question, failing on a finding the table in
# cmd/audit_binary_vulns/declarations.go does not accept and on a declaration no
# finding needs. BINARIES=<glob> scans binaries already built instead (one per
# target), which is how the release holds GoReleaser's own output to it. It runs
# from its own module (VULNGATE_DIR), so every path it is handed is absolute.
check-binary-vulns: ## Fail when a release binary carries an undeclared advisory at module grain (needs network)
	go -C $(VULNGATE_DIR) run . -dir $(CURDIR) -config $(CURDIR)/.goreleaser.yml $(if $(BINARIES),-binaries '$(CURDIR)/$(BINARIES)')

# The OpenVEX document scanners read: one not_affected statement per not-linked
# declaration in the table above, every product pinned to the version in
# VERSION, generated from the table and never edited by hand. check-vex holds the
# committed copy to the table and to VERSION in both directions (offline, no
# build), so a declaration removed without its statement fails, a statement with
# no declaration behind it fails, and a version bump fails until gen-vex has run.
# vex-release writes the copy a release attaches to its image and publishes as an
# asset, pinned to VEX_VERSION and the image index VEX_DIGEST: it runs the whole
# binary-vuln gate on VEX_DIR first (the not-linked check included) and writes
# nothing unless that passes, so it needs the network. VEX_DIR is the tree the
# release is built from, which vex-attach sets to the tagged checkout.
VEX_DOC := .vex/libgen-mcp.openvex.json
VEX_OUT ?= dist/libgen-mcp.openvex.json
VEX_DIR ?= $(CURDIR)

gen-vex: ## Rewrite .vex/libgen-mcp.openvex.json from the binary-vuln declarations and VERSION
	go -C $(VULNGATE_DIR) run . -vex-write $(CURDIR)/$(VEX_DOC) -vex-version $(VERSION)

check-vex: ## Fail when .vex/libgen-mcp.openvex.json disagrees with the binary-vuln declarations or VERSION
	go -C $(VULNGATE_DIR) run . -vex-check $(CURDIR)/$(VEX_DOC) -vex-version $(VERSION)

vex-release: ## Gate VEX_DIR, then write the release's OpenVEX copy (VEX_VERSION=x.y.z VEX_DIGEST=sha256:... [VEX_OUT=path] [VEX_DIR=tree]; needs network)
	@test -n "$(VEX_VERSION)" && test -n "$(VEX_DIGEST)" || { echo "usage: make vex-release VEX_VERSION=<x.y.z> VEX_DIGEST=sha256:<index> [VEX_OUT=<path>] [VEX_DIR=<tree>]"; exit 2; }
	go -C $(VULNGATE_DIR) run . -vex-check $(CURDIR)/$(VEX_DOC) -vex-version $(VERSION) \
		-dir $(abspath $(VEX_DIR)) -config $(abspath $(VEX_DIR))/.goreleaser.yml \
		-vex-out $(abspath $(VEX_OUT)) -vex-release $(VEX_VERSION) -vex-index-digest $(VEX_DIGEST)

# The nested module's tests, with the same floor the root module is held to.
test-binary-vulns: ## Run cmd/audit_binary_vulns's tests (its own module) and hold it to COVERAGE_MIN
	@dir=$$(mktemp -d) && trap 'rm -rf "$$dir"' EXIT && \
		go -C $(VULNGATE_DIR) test -count=1 -coverprofile="$$dir/cover.out" ./... && \
		total=$$(go -C $(VULNGATE_DIR) tool cover -func="$$dir/cover.out" | grep '^total:' | awk '{print $$3}' | tr -d '%') && \
		echo "$(VULNGATE_DIR) coverage: $$total%" && \
		awk "BEGIN {exit !($$total + 0 >= $(COVERAGE_MIN) + 0)}" || { echo "FAIL: $(VULNGATE_DIR) is below $(COVERAGE_MIN)%"; exit 1; }

fmt: ## Apply formatters (goimports, gofumpt, gci)
	golangci-lint fmt

vet: ## Run go vet
	go vet $(GO_ANALYSIS_PKGS)
	go -C $(VULNGATE_DIR) vet ./...

tidy: ## Tidy go.mod / go.sum
	go mod tidy

# ─── Documentation ──────────────────────────────────────────────────────────
format-md-tables: ## Normalize Markdown pipe tables in README.md and docs/
	go run ./cmd/format_md_tables/

check-md-tables: ## Fail if any Markdown table needs formatting (CI mode)
	go run ./cmd/format_md_tables/ --check

# The table pass follows because a longer number (2.9.0 to 2.10.0) widens a
# cell, and docs/ tables are held to their normalized widths.
gen-doc-versions: ## Write VERSION and CITATION.cff's date into docs/ wherever the Starlight twin writes a release token
	go run ./cmd/gen_doc_versions/
	go run ./cmd/format_md_tables/

check-doc-versions: ## Fail if docs/ names another release or date where the Starlight twin writes a release token
	go run ./cmd/gen_doc_versions/ --check

check-doc-links: ## Fail if any tracked Markdown/MDX local link, path or anchor is broken
	node scripts/check-doc-links.mjs

godoc-audit: ## Report missing/malformed Go doc comments (Markdown)
	go run ./cmd/godoc_tool/ audit --format=markdown

godoc-check: ## Fail if any Go doc comments are missing/malformed (CI mode)
	go run ./cmd/godoc_tool/ audit --fail-on-findings
	@$(MAKE) --no-print-directory godoc-check-binary-vulns

# godoc_tool lists `./...` of the module it runs in, which at the root stops at
# the nested module, so it is built once and run from inside that module.
godoc-check-binary-vulns: ## Fail if cmd/audit_binary_vulns (its own module) has a missing or malformed doc comment
	@dir=$$(mktemp -d) && trap 'rm -rf "$$dir"' EXIT && \
		go build -o "$$dir/godoc_tool" ./cmd/godoc_tool/ && \
		cd $(VULNGATE_DIR) && "$$dir/godoc_tool" audit --include-tests --fail-on-findings

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

audit-gateway-chars: ## Report served strings carrying characters an MCP gateway may reject
	go run ./cmd/audit_gateway_chars/

check-gateway-chars: ## Fail when the served surface carries a character an MCP gateway may reject (CI gate)
	go run ./cmd/audit_gateway_chars/ -check

audit-doc-names: ## Report names in the documentation that the server does not have
	go run ./cmd/audit_doc_names/

check-doc-names: ## Fail when the documentation names a variable, source, tool or prompt the server does not have (CI gate)
	go run ./cmd/audit_doc_names/ -check

gen-stats: ## Recount the surface and rewrite the stats tables in README.md
	go run ./cmd/gen_stats/

check-stats: ## Fail if README.md's stats tables no longer match the source (CI gate)
	go run ./cmd/gen_stats/ --check

# The measurement is hand-run and the gate is not, which is the whole
# arrangement: the numbers come from one machine and the check only asks whether
# the page still says what they say. A gate that re-measured would fail for
# running on a different CPU, which is a gate nobody can keep green.
bench-resources: ## Measure what the server costs to run and rewrite the record (hand-run; minutes)
	go run ./cmd/bench_resources/ $(if $(SCENARIOS),-scenarios $(SCENARIOS)) $(if $(QUICK),-quick) -v

bench-resources-render: ## Redraw the benchmark page from the committed record, without measuring
	go run ./cmd/bench_resources/ -render

check-bench-resources: ## Fail if the benchmark page no longer matches its record (CI gate)
	go run ./cmd/bench_resources/ -check

audit-md-escaping: ## Report catalog text reaching a Markdown construct with no escaper between it and the page
	go run ./cmd/audit_md_escaping/ -v -contexts all,card

check-md-escaping: ## Fail when a value reaches a Markdown construct unescaped or a card row is written by hand (CI gate)
	go run ./cmd/audit_md_escaping/ -check -contexts all,card -fail-unresolved-in internal/toolutil

check-install-buttons: ## Decode every one-click install button and hold them to one configuration per command
	go run ./cmd/audit_install_buttons/

check-test-file-names: ## Fail when a _test.go file is not named after a module it tests (CI gate)
	go run ./cmd/audit_test_names/ -check-files cmd internal test

audit-test-subtests: ## Report case loops that assert without opening a subtest
	go run ./cmd/audit_test_subtests/

fix-test-subtests: ## Rewrite the case loops whose subtest name is unambiguous
	go run ./cmd/audit_test_subtests/ -fix

check-test-subtests: ## Fail when a case loop still asserts without a subtest (CI gate)
	go run ./cmd/audit_test_subtests/ -check

audit-test-goroutines: ## Report every testing.T abort made off the test goroutine, plus the advisory Errorf sites
	go run ./cmd/audit_test_goroutines/

check-test-goroutines: ## Fail when a testing.T abort is made off the test goroutine (CI gate)
	go run ./cmd/audit_test_goroutines/ -check

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
# how .plugin/plugin.json spent a release cycle claiming to be 1.2.0. The npm
# launcher is in the set for the same reason: the release workflow re-stamps it
# and commits it back, so a bump that skips `make sync-npm-version` would leave
# main advertising the previous version on npm until the next tag.
VERSION_MANIFESTS := server.json mcpb/manifest.json lhm.plugin.json .plugin/plugin.json \
                     plugin.json npm/libgen-mcp/package.json

# The citation file GitHub's "Cite this repository" reads. YAML, so it is gated
# by its top-level `version:` line rather than through jq; a citation naming the
# wrong release is a wrong citation.
CITATION_FILE := CITATION.cff

check-manifests: ## Verify every version-bearing manifest matches the VERSION file, and the Agent Plugins files their schemas
	@VF=$$(tr -d '[:space:]' < VERSION); \
	for f in $(VERSION_MANIFESTS); do \
		jq empty "$$f" || exit 1; \
		MV=$$(jq -r '.version' "$$f"); \
		if [ "$$MV" != "$$VF" ]; then \
			echo "FAIL: $$f version ($$MV) != VERSION ($$VF)"; exit 1; \
		fi; \
		echo "$$f: valid JSON, version matches VERSION ($$VF)"; \
	done; \
	CV=$$(sed -n 's/^version: *//p' $(CITATION_FILE)); \
	if [ "$$CV" != "$$VF" ]; then \
		echo "FAIL: $(CITATION_FILE) version ($$CV) != VERSION ($$VF)"; exit 1; \
	fi; \
	echo "$(CITATION_FILE): version matches VERSION ($$VF)"
	@# plugin.json and mcp.json against the Agent Plugins 1.0 schemas, vendored
	@# under scripts/testdata so this stays offline, plus the rules a schema
	@# cannot state: a host leaves every ${...} but two literal.
	python3 -m unittest discover -s scripts -p 'agent_plugin_test.py'

check-stamper: ## Exercise the release stamper (server.json) against a fixture manifest
	bash scripts/update-server-json-sha_test.sh

check-server-json-packages: ## Download every package server.json declares and check it is what it claims (needs network)
	bash scripts/validate-server-json-packages.sh

check-verify-published: ## Exercise the published-package verifier and the NuGet layout its unsigning relies on (offline)
	python3 -m unittest discover -s scripts -p 'verify_published_packages_test.py'
	python3 -m unittest discover -s scripts -p 'validate_nuget_test.py'

check-mcpb: ## Exercise the Claude Desktop bundles: the manifests, the Linux launcher, the packer, the fetch and the registry check (offline)
	python3 -m unittest discover -s scripts -p 'mcpb_manifest_test.py'
	python3 -m unittest discover -s scripts -p 'mcpb_launch_sh_test.py'
	python3 -m unittest discover -s scripts -p 'build_mcpb_sh_test.py'
	python3 -m unittest discover -s scripts -p 'fetch_release_assets_sh_test.py'
	python3 -m unittest discover -s scripts -p 'validate_server_json_packages_sh_test.py'

# One case runs the real server, to show a Ctrl+C through the launcher keeps
# its --drain-delay, so the binary is built first into a directory removed after.
check-npm-launcher: ## Exercise the npm launcher against a stand-in and the real server: argv, stdout, exit status and signals (offline)
	@dir=$$(mktemp -d) && trap 'rm -rf "$$dir"' EXIT && \
		CGO_ENABLED=0 go build -o "$$dir/libgen-mcp" $(CMD_PATH) && \
		NPM_LAUNCHER_TEST_SERVER="$$dir/libgen-mcp" python3 -m unittest discover -s scripts -p 'npm_cli_js_test.py'

check-homebrew-tap: ## Render the Homebrew formula from a fixture checksums.txt and check what it installs (offline)
	python3 -m unittest discover -s scripts -p 'update_homebrew_tap_sh_test.py'

# Every linux ELF under DIST is ELF64 with no PT_INTERP program header: the
# property the "binaries are standalone" rule is about, read from the program
# headers rather than grepped for. The release runs it over GoReleaser's dist/
# before anything ships; check-elf-standalone drives the script itself against
# a -buildmode=pie build (refused) and the release's build (accepted).
#
# ELF_EXPECT is how many there must be: .goreleaser.yml builds linux for amd64
# and arm64, so a dist/ holding one fewer is a release that dropped a target,
# not a release that passed. The universal darwin binary is Mach-O, not ELF.
DIST ?= dist
ELF_EXPECT ?= 2
elf-standalone: ## Fail unless DIST (default dist/) holds ELF_EXPECT linux ELF files, each standalone: ELF64, no PT_INTERP
	python3 scripts/check_elf_standalone.py --expect $(ELF_EXPECT) $(DIST)

check-elf-standalone: ## Exercise the standalone-ELF check against PIE and static fixture builds (needs go)
	python3 -m unittest discover -s scripts -p 'check_elf_standalone_test.py'

check-supply-chain: ## Every action pinned, no run-time-resolved code in a credentialed job, cooldowns stated
	go run ./cmd/audit_supply_chain/

# The squash merge makes the description the commit message on main, so this is
# the half of a change no later commit can fix. Reads the open pull request
# through gh, PR_NUMBER=<n> names one, and PR_TITLE_FILE/PR_BODY_FILE judge text
# from disk. A branch with no pull request passes, having nothing to land.
check-pr-description: ## Refuse a skip command, a bot-injected block or assistant attribution in the PR title/body
	bash scripts/check-pr-description.sh

# Needs the npm registry. A registry that does not answer is a warning rather
# than a failure, since it says nothing about the tree; a finding always fails.
audit-site-deps: ## pnpm audit of site/ at high and above, tolerant of an npm outage (needs network)
	bash scripts/audit-site-deps.sh

check-ci-scripts: ## Exercise the PR description gate, the site audit's outage rule and the image smoke test against fixtures (offline)
	python3 -m unittest discover -s scripts -p 'check_pr_description_sh_test.py'
	python3 -m unittest discover -s scripts -p 'audit_site_deps_sh_test.py'
	python3 -m unittest discover -s scripts -p 'smoke_test_image_sh_test.py'

mcpb: ## Build the .mcpb Claude Desktop bundles, one per OS and the universal one (needs GoReleaser artifacts in dist/)
	bash scripts/build-mcpb.sh $(VERSION)

# ─── npm distribution ───────────────────────────────────────────────────────
# The npm channel is a thin launcher package (@jmrp.io/libgen-mcp) plus six
# per-platform packages, each carrying one release binary and gated by npm's
# os/cpu fields so only the matching one is unpacked. Nothing runs at install
# time, which is what keeps `npx`, `npm ci --ignore-scripts`, offline and
# proxied installs working — a postinstall that downloads breaks all four, on
# exactly the locked-down machines an MCP server gets dropped onto.
#
# NPM_BINARIES points at a directory holding the release assets under their
# published names (libgen-mcp-linux-amd64, …) — `dist/` after GoReleaser, or a
# directory of assets downloaded from a release.
NPM_BINARIES ?=

gen-npm: ## Assemble the npm distribution from release binaries (NPM_BINARIES=<dir>)
	@command -v node >/dev/null || { echo "ERROR: Node.js is required"; exit 1; }
	@test -n "$(NPM_BINARIES)" || { echo "ERROR: set NPM_BINARIES=<dir of release binaries>"; exit 1; }
	node scripts/build-npm.mjs --binaries "$(NPM_BINARIES)" --version "$(VERSION)"

sync-npm-version: ## Stamp VERSION into npm/libgen-mcp/package.json and its six pins
	@command -v node >/dev/null || { echo "ERROR: Node.js is required"; exit 1; }
	node scripts/build-npm.mjs --sync-only --version "$(VERSION)"

# Runs in a clean node:22 container so the check never installs anything on the
# host: structural checks on all seven packages, plus a real install and MCP
# handshake for the container's native platform (linux-x64).
validate-npm: ## Build and validate the npm packages in Docker (NPM_BINARIES=<dir>)
	@command -v docker >/dev/null || { echo "ERROR: Docker is required for isolated validation (or use validate-npm-local)"; exit 1; }
	@test -n "$(NPM_BINARIES)" || { echo "ERROR: set NPM_BINARIES=<dir of release binaries>"; exit 1; }
	@# --user with a writable HOME: the container writes npm/packages/ and rewrites
	@# npm/libgen-mcp/package.json through the bind mount, so running as root would
	@# leave a tracked manifest root-owned on the host. HOME must move with the uid
	@# or npm has nowhere to put its cache.
	docker run --rm --user "$$(id -u):$$(id -g)" -e HOME=/tmp -v "$(CURDIR):/work" -v "$(abspath $(NPM_BINARIES)):/binaries:ro" -w /work node:22 sh -euc 'node scripts/build-npm.mjs --binaries /binaries --version $(VERSION) && node scripts/validate-npm.mjs --packages npm/packages --main npm/libgen-mcp --version $(VERSION)'

# Same validation without Docker, for the ephemeral CI runner — already
# disposable, and the release job has no Docker-in-Docker to spare.
validate-npm-local: ## Same validation without Docker, for CI (NPM_BINARIES=<dir>)
	@command -v node >/dev/null || { echo "ERROR: Node.js is required"; exit 1; }
	@test -n "$(NPM_BINARIES)" || { echo "ERROR: set NPM_BINARIES=<dir of release binaries>"; exit 1; }
	node scripts/build-npm.mjs --binaries "$(NPM_BINARIES)" --version "$(VERSION)"
	node scripts/validate-npm.mjs --packages npm/packages --main npm/libgen-mcp --version "$(VERSION)"

publish-npm-dry: ## Assemble and pack the npm publish set without publishing (NPM_BINARIES=<dir>)
	@test -n "$(NPM_BINARIES)" || { echo "ERROR: set NPM_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-npm.sh "$(NPM_BINARIES)" "$(VERSION)" --dry-run

# Auth is left to the environment (`npm login`, or a temporary .npmrc for the
# one-time bootstrap). From the first tag after bootstrap on, the release
# workflow publishes over OIDC and this target is only a fallback.
publish-npm: ## Publish the npm distribution: platform packages first, then the launcher
	@test -n "$(NPM_BINARIES)" || { echo "ERROR: set NPM_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-npm.sh "$(NPM_BINARIES)" "$(VERSION)"

# ─── PyPI distribution ──────────────────────────────────────────────────────
# Six platform wheels, each carrying one release binary in .data/scripts — the
# uv/ruff model, where the installer puts the binary on the scripts path and it
# IS the command. Nothing runs at install time and nothing is compiled.
#
# The two linux wheels carry musllinux tags beside the manylinux ones, which the
# validator checks against the archived bytes rather than trusting: the binaries
# are static, so one file serves Debian and Alpine alike.
PYPI_BINARIES ?=

gen-pypi: ## Assemble the PyPI wheelhouse from release binaries (PYPI_BINARIES=<dir>)
	@command -v python3 >/dev/null || { echo "ERROR: Python 3 is required"; exit 1; }
	@test -n "$(PYPI_BINARIES)" || { echo "ERROR: set PYPI_BINARIES=<dir of release binaries>"; exit 1; }
	python3 scripts/build_pypi.py --binaries "$(PYPI_BINARIES)" --version "$(VERSION)"

validate-pypi: ## Build and validate the wheels in a clean python:3.14-slim container (PYPI_BINARIES=<dir>)
	@command -v docker >/dev/null || { echo "ERROR: Docker is required for isolated validation (or use validate-pypi-local)"; exit 1; }
	@test -n "$(PYPI_BINARIES)" || { echo "ERROR: set PYPI_BINARIES=<dir of release binaries>"; exit 1; }
	@# --user with a writable HOME, as validate-npm does: the container writes
	@# pypi/dist through the bind mount, so running as root would leave it
	@# root-owned on the host.
	docker run --rm --user "$$(id -u):$$(id -g)" -e HOME=/tmp \
		-v "$(CURDIR):/work" -v "$(abspath $(PYPI_BINARIES)):/binaries:ro" \
		-w /work python:3.14-slim sh -euc ' \
			python3 scripts/build_pypi.py --binaries /binaries --version $(VERSION) && \
			python3 scripts/validate_pypi.py --wheels pypi/dist --version $(VERSION)'

validate-pypi-local: ## Same validation without Docker, for CI (PYPI_BINARIES=<dir>)
	@command -v python3 >/dev/null || { echo "ERROR: Python 3 is required"; exit 1; }
	@test -n "$(PYPI_BINARIES)" || { echo "ERROR: set PYPI_BINARIES=<dir of release binaries>"; exit 1; }
	python3 scripts/build_pypi.py --binaries "$(PYPI_BINARIES)" --version "$(VERSION)"
	python3 scripts/validate_pypi.py --wheels pypi/dist --version "$(VERSION)"

# The macOS wheel tag must be the minimum macOS the binary declares, which the
# Go toolchain decides: one case cross-builds darwin/amd64 and darwin/arm64 and
# reads the floor back, so a toolchain bump that moves it fails here first.
check-pypi: ## Exercise the macOS wheel tag against the toolchain's floor, the builder and the validator (needs go)
	python3 -m unittest discover -s scripts -p 'build_pypi_test.py'

publish-pypi-dry: ## Assemble and validate the wheelhouse without uploading (PYPI_BINARIES=<dir>)
	@test -n "$(PYPI_BINARIES)" || { echo "ERROR: set PYPI_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-pypi.sh "$(PYPI_BINARIES)" "$(VERSION)" --dry-run

# Auth via PYPI_TOKEN. The release workflow publishes through the OIDC trusted
# publisher instead; this target is the bootstrap and the manual fallback.
publish-pypi: ## Assemble, validate and publish the PyPI wheels out of band (PYPI_BINARIES=<dir>)
	@test -n "$(PYPI_BINARIES)" || { echo "ERROR: set PYPI_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-pypi.sh "$(PYPI_BINARIES)" "$(VERSION)"

# ─── NuGet distribution ─────────────────────────────────────────────────────
# A .NET tool whose entry point is the native binary: one pointer package that
# names a package per runtime identifier, and six runtime packages that each
# carry one binary. Nothing in them is .NET code, and a .nupkg is a zip with an
# OPC skeleton, so packing needs Python and no SDK — only the validator's
# install-and-run step needs one.
NUGET_BINARIES ?=

# Pinned by digest because dnx's behaviour is an SDK property, not a package
# property: bump it deliberately, and re-run the validator when you do.
NUGET_SDK_IMAGE = mcr.microsoft.com/dotnet/sdk:10.0@sha256:e1ffd2a92ae84c1291bc1b6887501f8af98e6331e7af6d4c8d37168c5e87a64c

gen-nuget: ## Assemble the NuGet packages from release binaries (NUGET_BINARIES=<dir>)
	@command -v python3 >/dev/null || { echo "ERROR: Python 3 is required"; exit 1; }
	@test -n "$(NUGET_BINARIES)" || { echo "ERROR: set NUGET_BINARIES=<dir of release binaries>"; exit 1; }
	python3 scripts/build_nuget.py --binaries "$(NUGET_BINARIES)" --version "$(VERSION)"

validate-nuget: ## Build and validate the packages in a pinned .NET SDK container (NUGET_BINARIES=<dir>)
	@command -v docker >/dev/null || { echo "ERROR: Docker is required for isolated validation (or use validate-nuget-local)"; exit 1; }
	@test -n "$(NUGET_BINARIES)" || { echo "ERROR: set NUGET_BINARIES=<dir of release binaries>"; exit 1; }
	docker run --rm \
		-v "$(CURDIR):/work" -v "$(abspath $(NUGET_BINARIES)):/binaries:ro" \
		-e DOTNET_NOLOGO=1 -e DOTNET_CLI_TELEMETRY_OPTOUT=1 \
		-w /work $(NUGET_SDK_IMAGE) sh -euc ' \
			apt-get -qq update && apt-get -qq install -y --no-install-recommends python3 >/dev/null && \
			python3 scripts/build_nuget.py --binaries /binaries --version $(VERSION) && \
			python3 scripts/validate_nuget.py --packages nuget/dist --version $(VERSION)'

validate-nuget-local: ## Same validation without Docker, for CI (needs the .NET 10 SDK on PATH)
	@command -v python3 >/dev/null || { echo "ERROR: Python 3 is required"; exit 1; }
	@test -n "$(NUGET_BINARIES)" || { echo "ERROR: set NUGET_BINARIES=<dir of release binaries>"; exit 1; }
	python3 scripts/build_nuget.py --binaries "$(NUGET_BINARIES)" --version "$(VERSION)"
	python3 scripts/validate_nuget.py --packages nuget/dist --version "$(VERSION)"

publish-nuget-dry: ## Assemble and validate the NuGet packages without pushing (NUGET_BINARIES=<dir>)
	@test -n "$(NUGET_BINARIES)" || { echo "ERROR: set NUGET_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-nuget.sh "$(NUGET_BINARIES)" "$(VERSION)" --dry-run

# Auth via NUGET_API_KEY. The release workflow pushes under NuGet's trusted
# publishing instead; this target is the bootstrap and the manual fallback.
publish-nuget: ## Assemble, validate and push the NuGet packages out of band (NUGET_BINARIES=<dir>)
	@test -n "$(NUGET_BINARIES)" || { echo "ERROR: set NUGET_BINARIES=<dir of release binaries>"; exit 1; }
	scripts/publish-nuget.sh "$(NUGET_BINARIES)" "$(VERSION)"

# The marketplace CLI is pinned rather than resolved, for the reason
# GORELEASER_VERSION and COSIGN_VERSION are: `npx -y <pkg>` downloads and runs
# whatever the registry serves as latest, and this target runs on the
# maintainer's own machine with a credential that can publish the listing. An
# upstream compromise would execute there, with that credential present.
#
# It is not a hypothetical on this dependency, either. 0.0.41 moved the
# repository to a positional argument and made `plugin update` the verb for an
# existing listing, which broke this target until PR #119 caught up — an
# unpinned upgrade arriving mid-release is how that is found.
#
# Bump it deliberately: read the changelog, run the target, and check the
# published listing with a cache-buster afterwards.
LOBEHUB_CLI_VERSION ?= 0.0.41

## publish-lobehub: publish the current version to the LobeHub Marketplace.
## Reads lhm.plugin.json (version kept in sync by scripts/update-server-json-sha.sh
## on each release) and posts it via the @lobehub/market-cli. It prompts for
## nothing: the credential in ~/.lobehub-market/user-credentials.json carries a
## refreshToken and the CLI renews itself. Only the first login on a machine is
## interactive, which is why this is not in CI — the credential is the
## maintainer's, not a repository secret.
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
	npx -y @lobehub/market-cli@$(LOBEHUB_CLI_VERSION) plugin update --dir "$(CURDIR)"

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
