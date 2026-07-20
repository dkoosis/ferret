//go:build darwin

package analyst

import (
	"os"
	"testing"

	"github.com/dkoosis/keyring"
)

// TestLiveKeychainKeyResolves exercises the REAL keychain read path: the
// anthropic API key must resolve via keyring under service "ferret",
// account "anthropic" (no env fallback taken). Gated on
// FERRET_LIVE_KEYCHAIN=1 so CI and sandboxes skip it.
func TestLiveKeychainKeyResolves(t *testing.T) {
	if os.Getenv("FERRET_LIVE_KEYCHAIN") == "" {
		t.Skip("set FERRET_LIVE_KEYCHAIN=1 to run against the real keychain")
	}
	t.Setenv(keyring.DisableEnv, "") // TestMain arms the kill-switch for hermetic tests
	s, err := keyring.New(keychainService)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	k, err := s.Get(keychainAccount)
	if err != nil {
		t.Fatalf("Get(%q/%q): %v", keychainService, keychainAccount, err)
	}
	if len(k) < 20 {
		t.Fatalf("resolved key implausibly short (%d chars)", len(k))
	}
}
