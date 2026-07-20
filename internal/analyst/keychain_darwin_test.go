//go:build darwin

package analyst

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dkoosis/keyring"
)

// stubSecurity writes an executable shell script standing in for
// /usr/bin/security and returns its path. Lets the keychain-present arms of
// the resolution-order tests exercise the REAL keyring library (exit-code
// classification, GetOrEnv fallback) without ever touching the developer's
// keychain — the same pattern as canapay's secrets tests.
func stubSecurity(t *testing.T, script string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// useStubStore clears TestMain's keyring.DisableEnv kill-switch and points
// the newStore seam at a Store backed by the stub, restoring both afterward.
func useStubStore(t *testing.T, script string) {
	t.Helper()
	t.Setenv(keyring.DisableEnv, "")
	bin := stubSecurity(t, script)
	restore := newStore
	t.Cleanup(func() { newStore = restore })
	newStore = func() (*keyring.Store, error) {
		return keyring.New(keychainService, keyring.WithSecurityBin(bin))
	}
}

// Stub scripts mirroring the `security find-generic-password -w` CLI
// contract: value on stdout + exit 0; exit 44 = confirmed item-not-found;
// any other non-zero exit = unreadable (locked, denied, timed out).
const (
	stubHasKey     = "printf 'from-keychain\\n'\nexit 0\n"
	stubNotFound   = "exit 44\n"
	stubUnreadable = "echo 'User interaction is not allowed.' >&2\nexit 36\n"
)

func TestResolveKey_KeychainArms(t *testing.T) {
	t.Run("keychain-beats-env", func(t *testing.T) {
		useStubStore(t, stubHasKey)
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-keychain" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("notfound-falls-to-env", func(t *testing.T) {
		useStubStore(t, stubNotFound)
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-env" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("unreadable-is-hard-error", func(t *testing.T) {
		useStubStore(t, stubUnreadable)
		t.Setenv(envAPIKey, "from-env")
		_, err := resolveKey(Config{})
		if !errors.Is(err, keyring.ErrUnreadable) {
			t.Errorf("locked keychain must surface, not downgrade to env; got %v", err)
		}
	})
	t.Run("notfound-and-no-env-empty-nil", func(t *testing.T) {
		useStubStore(t, stubNotFound)
		t.Setenv(envAPIKey, "")
		k, err := resolveKey(Config{})
		if k != "" || err != nil {
			t.Errorf("got (%q, %v), want empty+nil (caller's ErrNoAPIKey case)", k, err)
		}
	})
}

func TestConfigHasAPIKey_UnreadableKeychainSurfaces(t *testing.T) {
	useStubStore(t, stubUnreadable)
	t.Setenv(envAPIKey, "sk-env")
	if _, err := (Config{}).HasAPIKey(); !errors.Is(err, keyring.ErrUnreadable) {
		t.Errorf("locked keychain must surface through HasAPIKey, got %v", err)
	}
}
