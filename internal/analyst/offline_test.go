package analyst

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/apiusage"
	"github.com/dkoosis/keyring"
)

// TestEnvOfflineSet pins the truth table the kill-switch depends on:
// strconv.ParseBool decides ON, and anything it cannot parse reads as OFF — a
// typo'd env var must not silently stop every model call, because the CLI
// reports refusals and cannot report one it never made (see the doc comment
// on EnvOfflineSet).
func TestEnvOfflineSet(t *testing.T) {
	tests := []struct {
		name string
		set  bool // false = leave the var unset entirely, not just empty
		val  string
		want bool
	}{
		{name: "unset", set: false, want: false},
		{name: "empty string", set: true, val: "", want: false},
		{name: "1", set: true, val: "1", want: true},
		{name: "true", set: true, val: "true", want: true},
		{name: "TRUE", set: true, val: "TRUE", want: true},
		{name: "0", set: true, val: "0", want: false},
		{name: "false", set: true, val: "false", want: false},
		{name: "unparseable", set: true, val: "banana", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(EnvOffline, tt.val)
			} else {
				// t.Setenv has no "unset" form, so register its restore-on-cleanup
				// with a throwaway value first, then unset for real — the var is
				// genuinely absent for the EnvOfflineSet() call below, and t
				// still restores whatever the environment held before this test.
				t.Setenv(EnvOffline, "unused")
				os.Unsetenv(EnvOffline)
			}
			if got := EnvOfflineSet(); got != tt.want {
				t.Errorf("EnvOfflineSet() with %s=%q (set=%v) = %v, want %v", EnvOffline, tt.val, tt.set, got, tt.want)
			}
		})
	}
}

// failDoer fails the test the moment it is invoked. It stands in for the
// network leg of complete(): if offline mode is doing its job, this must
// never run.
type failDoer struct{ t *testing.T }

func (d failDoer) Do(*http.Request) (*http.Response, error) {
	d.t.Fatal("complete() reached the HTTP transport despite offline mode")
	return nil, nil //nolint:nilnil // unreachable: t.Fatal calls runtime.Goexit before this executes
}

// TestComplete_ReturnsErrOffline_WithoutTouchingKeychainOrNetwork asserts the
// guard is the FIRST thing complete() does — before resolveKey, before the
// request is built. Both the keychain and the transport are wired to fail the
// test if reached, for both forms of the kill-switch (Config.Offline and
// FERRET_OFFLINE).
func TestComplete_ReturnsErrOffline_WithoutTouchingKeychainOrNetwork(t *testing.T) {
	guardKeychain := func(t *testing.T) {
		t.Helper()
		orig := newStore
		newStore = func() (*keyring.Store, error) {
			t.Fatal("complete() opened the keychain despite offline mode")
			return nil, nil //nolint:nilnil // unreachable: t.Fatal calls runtime.Goexit before this executes
		}
		t.Cleanup(func() { newStore = orig })
	}

	t.Run("via Config.Offline", func(t *testing.T) {
		guardKeychain(t)
		cfg := Config{Offline: true, HTTPClient: failDoer{t: t}}
		_, _, _, err := complete(context.Background(), cfg, "system", "user")
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("complete() err = %v, want ErrOffline", err)
		}
	})

	t.Run("via FERRET_OFFLINE", func(t *testing.T) {
		guardKeychain(t)
		t.Setenv(EnvOffline, "1")
		cfg := Config{HTTPClient: failDoer{t: t}}
		_, _, _, err := complete(context.Background(), cfg, "system", "user")
		if !errors.Is(err, ErrOffline) {
			t.Fatalf("complete() err = %v, want ErrOffline", err)
		}
	})
}

// TestStderrReporter covers both notice paths: the priced footer (a model in
// apiusage's table) and the unpriced one (a model the table doesn't know),
// which must say "unknown" rather than guess.
func TestStderrReporter(t *testing.T) {
	t.Run("preflight", func(t *testing.T) {
		var buf bytes.Buffer
		r := StderrReporter{W: &buf}
		r.Preflight("claude-opus-5", 2048)

		got := buf.String()
		if !strings.Contains(got, "claude-opus-5") || !strings.Contains(got, "2.0kB") {
			t.Errorf("Preflight output = %q, want model id and human-readable byte size", got)
		}
	})

	t.Run("complete priced", func(t *testing.T) {
		var buf bytes.Buffer
		r := StderrReporter{W: &buf}
		r.Complete("claude-opus-5", Usage{InputTokens: 1000, OutputTokens: 500}, 1.2345, true)

		got := buf.String()
		if !strings.Contains(got, "1000") || !strings.Contains(got, "500") || !strings.Contains(got, "$1.2345") {
			t.Errorf("Complete (priced) output = %q, want token counts and a $ figure", got)
		}
		if strings.Contains(got, "unknown") {
			t.Errorf("Complete (priced) output = %q, must not say cost is unknown", got)
		}
	})

	t.Run("complete unpriced", func(t *testing.T) {
		var buf bytes.Buffer
		r := StderrReporter{W: &buf}
		r.Complete("some-future-model", Usage{InputTokens: 10, OutputTokens: 5}, 0, false)

		got := buf.String()
		if !strings.Contains(got, "unknown") {
			t.Errorf("Complete (unpriced) output = %q, want it to say the cost is unknown rather than print a guessed $ figure", got)
		}
		if strings.Contains(got, "$") {
			t.Errorf("Complete (unpriced) output = %q, must not print a dollar figure it doesn't have", got)
		}
	})
}

// TestCostUSD_AgreesWithApiusageRowCost pins the reason CostUSD exists: the
// CLI's own footer and `ferret usage`'s corpus-wide ledger must price the same
// call identically, because they are read from the same table.
func TestCostUSD_AgreesWithApiusageRowCost(t *testing.T) {
	const model = "claude-opus-5" // priced in apiusage's table (see apiusage_test.go)
	u := Usage{InputTokens: 1_000_000, OutputTokens: 500_000}

	gotUSD, gotPriced := CostUSD(model, u)

	row := apiusage.Row{Model: model, Input: int(u.InputTokens), Output: int(u.OutputTokens)}
	wantUSD, wantPriced := row.Cost()

	if !wantPriced {
		t.Fatalf("%s is unpriced in apiusage's table — pick a model this test can actually pin", model)
	}
	if gotPriced != wantPriced || gotUSD != wantUSD {
		t.Errorf("CostUSD(%q) = (%v, %v), want (%v, %v) — must match apiusage.Row.Cost() exactly, or the CLI footer and `ferret usage` disagree",
			model, gotUSD, gotPriced, wantUSD, wantPriced)
	}
}
