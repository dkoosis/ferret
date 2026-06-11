#!/usr/bin/env bash
# Codex cloud environment setup for ferret
# Auto-discovered by Codex from .sandbox/codex/setup.sh on first container creation.
# Cached ~12h; maintenance.sh refreshes cached containers.
set -euo pipefail

SANDBOX_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_DIR="$(cd "$SANDBOX_DIR/.." && pwd)"
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac
PREBUILT_DIR="$SANDBOX_DIR/bin/linux-$ARCH"
INSTALL_DIR="/usr/local/bin"

# shellcheck source=/dev/null
source "$SANDBOX_DIR/lib/lib-doctor.sh"
# shellcheck source=/dev/null
source "$SANDBOX_DIR/lib/lib-setup.sh"

# --- Timing helpers ---
SETUP_START=$(date +%s)
STEP_START=$SETUP_START
CURRENT_STEP=""
step() {
  local now=$(date +%s)
  if [ -n "$CURRENT_STEP" ]; then
    echo "  done ($(( now - STEP_START ))s)"
  fi
  STEP_START=$now
  CURRENT_STEP="$1"
  echo ""
  echo "--- $1 ---"
}
finish() {
  local now=$(date +%s)
  if [ -n "$CURRENT_STEP" ]; then
    echo "  done ($(( now - STEP_START ))s)"
  fi
  echo ""
  echo "=== setup finished in $(( now - SETUP_START ))s ==="
}

echo "=== ferret sandbox setup ==="
echo "  arch: $ARCH"
echo "  repo: $REPO_DIR"
echo "  prebuilt dir: $PREBUILT_DIR"

step "1. System aliases"
setup_fd_alias

step "2. Prebuilt binaries"
install_sandbox_binaries

step "3. Node tools (jscpd)"
install_jscpd

step "4. fo"
install_fo_stub

step "5. Go module cache"
echo "  running go mod download ..."
if ! download_go_modules; then
  fatal "go mod download failed"
fi

step "5b. Warm test build cache"
warm_test_cache

step "6. Environment check"

echo "  6a. Go version compatibility"
check_go_version
echo "  go: $ACTUAL_GO_VER (go.mod wants $(grep '^go ' "$REPO_DIR/go.mod" | awk '{print $2}'))"

echo "  6b. golangci-lint version check"
if have golangci-lint; then
  LINT_GO_VER=$(golangci_lint_go_version)
  echo "  golangci-lint built with go$LINT_GO_VER"
  if [ -n "$LINT_GO_VER" ] && [ -n "$ACTUAL_GO_VER" ] && [ "$LINT_GO_VER" != "$ACTUAL_GO_VER" ]; then
    LINT_NUM=$(version_to_int "$LINT_GO_VER")
    ACTUAL_NUM=$(version_to_int "$ACTUAL_GO_VER")
    if [ "$LINT_NUM" -lt "$ACTUAL_NUM" ]; then
      warn "golangci-lint built with go$LINT_GO_VER; sandbox has go$ACTUAL_GO_VER — rebuild prebuilt with newer Go"
    fi
  fi
fi

echo "  6c. Restore missing prebuilt tools"
restore_sandbox_binaries

echo "  6d. Required tools"
check_required_tools

echo "  6e. Optional tools"
check_optional_tools

finish
echo "SETUP_COMPLETE"
doctor_exit "setup"
