package analyst

import (
	"errors"
	"os"
	"testing"

	"github.com/dkoosis/keyring"
)

// TestMain isolates the whole package from the developer's real keychain: a
// stored ferret/anthropic item would otherwise satisfy HasAPIKey in every
// "no key set" test. Individual tests reassign readKeychain to exercise the
// resolution order.
func TestMain(m *testing.M) {
	readKeychain = func() (string, error) { return "", keyring.ErrNotFound }
	os.Exit(m.Run())
}

func TestResolveKey_Order(t *testing.T) {
	restore := readKeychain
	defer func() { readKeychain = restore }()

	t.Run("explicit-config-wins", func(t *testing.T) {
		readKeychain = func() (string, error) { return "from-keychain", nil }
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{APIKey: "from-config"})
		if k != "from-config" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("keychain-beats-env", func(t *testing.T) {
		readKeychain = func() (string, error) { return "from-keychain", nil }
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-keychain" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("notfound-falls-to-env", func(t *testing.T) {
		readKeychain = func() (string, error) { return "", keyring.ErrNotFound }
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-env" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("unsupported-falls-to-env", func(t *testing.T) {
		readKeychain = func() (string, error) { return "", keyring.ErrUnsupported }
		t.Setenv(envAPIKey, "from-env")
		k, err := resolveKey(Config{})
		if k != "from-env" || err != nil {
			t.Errorf("got (%q, %v)", k, err)
		}
	})
	t.Run("unreadable-is-hard-error", func(t *testing.T) {
		readKeychain = func() (string, error) { return "", keyring.ErrUnreadable }
		t.Setenv(envAPIKey, "from-env")
		_, err := resolveKey(Config{})
		if !errors.Is(err, keyring.ErrUnreadable) {
			t.Errorf("locked keychain must surface, not downgrade to env; got %v", err)
		}
	})
	t.Run("nothing-anywhere-empty-nil", func(t *testing.T) {
		readKeychain = func() (string, error) { return "", keyring.ErrNotFound }
		t.Setenv(envAPIKey, "")
		k, err := resolveKey(Config{})
		if k != "" || err != nil {
			t.Errorf("got (%q, %v), want empty+nil (caller's ErrNoAPIKey case)", k, err)
		}
	})
}
