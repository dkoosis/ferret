# Ferret Makefile — strict QA gates.
#
# Primary: check (vet+lint+test+build+standard) | audit (check+race+fuzz+dupe+nilcheck+deadcode+omitempty+vuln)

.DEFAULT_GOAL := check

SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

# ── Shared sandbox (go-sandbox) ──
# doctor ← Makefile.doctor.mk; cross / cross-amd64 / cross-arm64 ← Makefile.cross.mk
include .sandbox/lib/Makefile.doctor.mk
include .sandbox/lib/Makefile.cross.mk

.PHONY: help check audit vet lint test race build standard vuln dupe nilcheck deadcode omitempty fuzz install deploy clean corpus

# Tool pins. Each gate runs its tool through `go run` or npx at the version
# below, so a green gate means the tool ran. repo-standard and omitemptyscalar
# resolve once github.com/dkoosis/repo-standard is tagged v0.4.0.
REPO_STANDARD_VER := v0.4.0
OMITEMPTYSCALAR_VER := v0.4.0
GOVULNCHECK_VER := v1.9.0
NILAWAY_VER := v0.0.0-20260918162853-acb8859b9031
DEADCODE_VER := v0.52.0
JSCPD_VER := 5.1.2

# Serialize golangci-lint through the machine-global mkdir mutex (see script
# header — golangci-lint's cache lock fails exit-3 on contention instead of
# waiting, which cascades across parallel sessions/worktrees).
GOLANGCILINT := bash scripts/lint-locked

help: ## Show this help
	@printf '\n\033[1mFour verbs. Identical in every dkoosis repo.\033[0m\n\n'
	@printf '  \033[36mcheck \033[0m  fast gate — vet + lint + test + build + standard. Pre-commit; required in CI.\n'
	@printf '  \033[36maudit \033[0m  check, plus race + fuzz + dupe + nilcheck + deadcode + omitempty + vuln. Before you ask for review.\n'
	@printf '  \033[36mdeploy\033[0m  build + install this tool locally.\n'
	@printf '  \033[36mhelp  \033[0m  this text.\n\n'
	@printf 'Everything below is an internal step of one of those four. Call the verbs.\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*?## / { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf '\n'

check: vet lint test build standard ## Fast validation: vet + lint + test + build + repo-standard
	@echo "=== check pass ==="

# Runs each Fuzz function it finds, FUZZTIME apiece.
FUZZTIME ?= 30s
fuzz: ## Fuzz every Fuzz test (FUZZTIME=30s each)
	@set -e; \
	for pkg in $$(go list ./...); do \
		list=$$(go test -list '^Fuzz' $$pkg) || exit $$?; \
		for f in $$(printf '%s\n' "$$list" | sed -n '/^Fuzz/p'); do \
			echo "fuzz $$pkg $$f"; \
			go test -run '^$$' -fuzz "^$$f$$" -fuzztime=$(FUZZTIME) $$pkg; \
		done; \
	done

# vuln runs LAST: govulncheck needs vuln.go.dev, and a sandbox that blocks that
# egress used to abort audit before dupe/nilcheck ever ran (sandbox self-audit,
# 2026-08-22). Network-independent gates now all report before the network one.
audit: check race fuzz dupe nilcheck deadcode omitempty vuln ## Exhaustive validation
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

# Fleet gate: repo-standard (the fleet SDLC checker) at a pinned version;
# bumping the pin is a deliberate PR. It runs last so it grades the tree the
# rest of the gate passed.
standard: ## Run repo-standard (fleet SDLC checker) against this repo
	go run github.com/dkoosis/repo-standard/cmd/repo-standard@$(REPO_STANDARD_VER)

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

vuln: ## Scan for known vulnerabilities (govulncheck, pinned)
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VER) ./...

dupe: ## Check for code duplication (jscpd, pinned)
	TMP_JSCPD=$$(mktemp -d); npx --yes jscpd@$(JSCPD_VER) . --threshold 3 --ignore "**/.git/**,**/.sandbox/**,**/.beads/**" --output $$TMP_JSCPD; rm -rf "$$TMP_JSCPD"

nilcheck: ## Run nilaway (pinned)
	go run go.uber.org/nilaway/cmd/nilaway@$(NILAWAY_VER) -include-pkgs=github.com/dkoosis/ferret ./...

# deadcode lists each function no path from a main reaches and exits 0 either way,
# so the target fails on any output.
deadcode: ## Fail on any function unreachable from main (deadcode, pinned)
	@out=$$(go run golang.org/x/tools/cmd/deadcode@$(DEADCODE_VER) ./cmd/...) || { echo "deadcode: could not run"; exit 1; }; \
	if [ -n "$$out" ]; then echo "$$out"; echo "deadcode: the functions above are unreachable; cut them"; exit 1; fi

# omitemptyscalar exits 3 on a ,omitempty tag on a bool (a false lost on write).
omitempty: ## Fail on a bool field tagged omitempty (omitemptyscalar, pinned)
	go run github.com/dkoosis/repo-standard/cmd/omitemptyscalar@$(OMITEMPTYSCALAR_VER) ./...

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
