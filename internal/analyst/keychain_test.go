package analyst

import (
	"os"
	"testing"

	"github.com/dkoosis/keyring"
)

// TestMain isolates the whole package from the developer's real keychain: a
// stored ferret/anthropic item would otherwise satisfy HasAPIKey in every
// "no key set" test. The keyring.DisableEnv kill-switch makes every keychain
// op report ErrUnsupported (env fallback still works), exactly as on a
// platform with no backend. Tests that need real keychain behavior clear the
// switch with t.Setenv and point newStore at a stub `security` binary — see
// keychain_darwin_test.go.
func TestMain(m *testing.M) {
	os.Setenv(keyring.DisableEnv, "1")
	os.Exit(m.Run())
}

// TestResolveKey_Order covers the portable slice of the resolution order:
// explicit config first, env fallback when no keychain backend answers,
// empty+nil when nothing is set anywhere. The keychain-present arms
// (value wins over env, confirmed-absence falls to env, unreadable is a hard
// error) exercise the real library against a stub `security` binary and live
// in keychain_darwin_test.go.
func TestResolveKey_Order(t *testing.T) {
	t.Run("explicit-config-wins", func(t *testing.T) {
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{APIKey: "from-config"})
		if k != "from-config" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("unsupported-falls-to-env", func(t *testing.T) {
		// TestMain's kill-switch makes the keychain report ErrUnsupported.
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-env" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("nothing-anywhere-empty-nil", func(t *testing.T) {
		t.Setenv(envAPIKey, "")
		k, err := resolveKey(Config{})
		if k != "" || err != nil {
			t.Errorf("got (%q, %v), want empty+nil (caller's ErrNoAPIKey case)", k, err)
		}
	})
}
