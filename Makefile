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

.PHONY: help check audit vet lint test race build vuln dupe nilcheck install clean corpus

# Serialize golangci-lint through the machine-global mkdir mutex (see script
# header — golangci-lint's cache lock fails exit-3 on contention instead of
# waiting, which cascades across parallel sessions/worktrees).
GOLANGCILINT := bash scripts/lint-locked

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\n"} \
		/^[a-zA-Z0-9_-]+:.*?## / { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

check: vet lint test build ## Fast validation: vet + lint + test + build
	@echo "=== check pass ==="

audit: check race vuln dupe nilcheck ## Exhaustive validation
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
		exit 0; \
	fi
	nilaway -include-pkgs=github.com/dkoosis/ferret ./...

## doctor target provided by .sandbox/lib/Makefile.doctor.mk (project.conf-driven)
## cross / cross-amd64 / cross-arm64 provided by .sandbox/lib/Makefile.cross.mk

install: ## Install ferret to GOPATH/bin
	go install ./cmd/ferret

clean: ## Remove built binary + sandbox build artifacts
	@rm -f ferret
	@rm -rf .sandbox/bin/linux-amd64 .sandbox/bin/linux-arm64 .sandbox/cache
	@echo "=== clean ==="
