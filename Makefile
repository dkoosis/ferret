# Ferret Makefile — strict QA gates, pattern from ~/Projects/trixi.
#
# Primary: check (vet+lint+test+build) | audit (check+race+vuln+dupe+nilcheck)

.DEFAULT_GOAL := check

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

# ── Shared sandbox (go-sandbox) ──
# doctor ← Makefile.doctor.mk; cross / cross-amd64 / cross-arm64 ← Makefile.cross.mk
include .sandbox/lib/Makefile.doctor.mk
include .sandbox/lib/Makefile.cross.mk

.PHONY: help check audit vet lint test race build selfcheck vuln dupe nilcheck fuzz install deploy clean corpus

# Serialize golangci-lint through the machine-global mkdir mutex (see script
# header — golangci-lint's cache lock fails exit-3 on contention instead of
# waiting, which cascades across parallel sessions/worktrees).
GOLANGCILINT := bash scripts/lint-locked

help: ## Show this help
	@printf '\n\033[1mFour verbs. Identical in every dkoosis repo.\033[0m\n\n'
	@printf '  \033[36mcheck \033[0m  fast gate — vet + lint + test + build. Pre-commit; required in CI.\n'
	@printf '  \033[36maudit \033[0m  check, plus race + fuzz + vuln + dupe + nilcheck. Before you ask for review.\n'
	@printf '  \033[36mdeploy\033[0m  build + install this tool locally.\n'
	@printf '  \033[36mhelp  \033[0m  this text.\n\n'
	@printf 'Everything below is an internal step of one of those four. Call the verbs.\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*?## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf '\n'

check: vet lint test build selfcheck ## Fast validation: vet + lint + test + build + conform
	@echo "=== check pass ==="

FUZZTIME ?= 30s
fuzz: ## Fuzz the parsers (FUZZTIME=30s)
	go test -run '^$$' -fuzz '^FuzzSplit$$' -fuzztime=$(FUZZTIME) ./internal/shellnorm

audit: check race fuzz vuln dupe nilcheck ## Exhaustive validation
	@echo "=== audit pass ==="

vet: ## Run go vet
	go vet ./...

lint: ## Run golangci-lint (strict config)
	$(GOLANGCILINT) run ./...

# No -count=1: Go's test cache stays ON for the dev loop; race/audit bypass it.
test: ## Run tests with coverage
	go test -cover ./...

race: ## Run tests with race detector (fresh run)
	go test -race -count=1 -cover ./...

# Fleet gate (sd-th5.22): conform pinned as a go.mod tool dependency
# (go.sum-verified); bumping the pin is a deliberate PR.
selfcheck: ## Run conform (fleet SDLC checker) against this repo
	go tool conform

build: ## Compile everything
	go build ./...

# CORPUS_OUT/N/SEED override the defaults: make corpus N=60 OUT=/tmp/big
CORPUS_OUT ?= /tmp/ferret-corpus
N          ?= 24
SEED       ?= 42
corpus: ## Generate a synthetic transcript corpus + ingest it
	go run ./cmd/gen-corpus -out $(CORPUS_OUT) -sessions $(N) -seed $(SEED)
	go run ./cmd/ferret ingest -root $(CORPUS_OUT)
	@echo "=== corpus at $(CORPUS_OUT); try: ferret ngrams -lens coarse -n 3 ==="

vuln: ## Scan for known vulnerabilities
	govulncheck ./...

dupe: ## Check for code duplication (jscpd)
	TMP_JSCPD=$$(mktemp -d); jscpd . --gitignore --output $$TMP_JSCPD; rm -rf $$TMP_JSCPD

nilcheck: ## Run nilaway (skips if not installed)
	@if ! command -v nilaway >/dev/null 2>&1; then \
		echo "nilcheck: nilaway not installed — skipping (install: go install go.uber.org/nilaway/cmd/nilaway@latest)"; \
	else \
		nilaway -include-pkgs=github.com/dkoosis/ferret ./...; \
	fi

## doctor target provided by .sandbox/lib/Makefile.doctor.mk (project.conf-driven)
## cross / cross-amd64 / cross-arm64 provided by .sandbox/lib/Makefile.cross.mk

install: ## Install ferret to GOPATH/bin
	go install ./cmd/ferret

deploy: build install ## Build, then install ferret to GOPATH/bin
	@echo "=== deployed (ferret installed to $$(go env GOPATH)/bin) ==="

clean: ## Remove built binary + sandbox build artifacts
	@rm -f ferret
	@rm -rf .sandbox/bin/linux-amd64 .sandbox/bin/linux-arm64 .sandbox/cache
	@echo "=== clean ==="
