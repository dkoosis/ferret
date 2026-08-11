#!/usr/bin/env bash
# .sandbox/local-activate.sh — ferret's repo-local sandbox customizations.
# Sourced by lib-activate.sh's generic seam (see .sandbox/lib/lib-activate.sh).
# Kept outside .sandbox/lib/ so the shared lib stays byte-identical fleet-wide
# and a re-pull from GO_SANDBOX_REF never deletes this.

# Anthropic key — single source of truth is the macOS keychain item the Go code
# reads (service=ferret account=anthropic, per the keyring convention: service =
# app, account = provider), so a rotation is one `/usr/bin/security
# add-generic-password -U -s ferret -a anthropic …` and every shell + local
# runner + the binary itself pick it up. Only overrides the ambient env when the
# keychain actually holds a value (missing entry / non-macOS → leave env as-is).
# Absolute /usr/bin/security (not PATH-resolved) so a hijacked $PATH can't
# substitute a malicious binary into the credential path — mirrors canapay's
# internal/secrets threat model (can-k85o).
if [ -x /usr/bin/security ]; then
  _FERRET_KEY="$(/usr/bin/security find-generic-password -s ferret -a anthropic -w 2>/dev/null)"
  [ -n "$_FERRET_KEY" ] && export FERRET_ANTHROPIC_API_KEY="$_FERRET_KEY"
  unset _FERRET_KEY
fi
