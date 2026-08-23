package analyst

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/dkoosis/ferret/internal/apiusage"
)

// Offline mode and the call-cost notice (ferret-wvo).
//
// Four ferret commands send transcript-derived material to Anthropic:
// adjudicate, over-initiative, feedback judge, and retrieval --hop1. Nothing
// used to distinguish them from the ~37 purely local commands, so an accidental
// paid call was one typo away and nothing stated what left the machine. Two
// guards, both funnelled through complete() — the package's single transport:
//
//   - EnvOffline / Config.Offline refuse the call before the request is built.
//   - Config.Reporter prints the model and input size BEFORE the call and the
//     measured token/cost footer AFTER, so a paid call is never silent.
//
// The CLI checks offline mode earlier still (a usage error before any work),
// but the check lives here too: a path that reaches the transport some other
// way must not escape the guard.

// EnvOffline is the environment kill-switch. Any value strconv.ParseBool reads
// as true ("1", "true", "TRUE") refuses model calls process-wide.
const EnvOffline = "FERRET_OFFLINE"

// ErrOffline is returned instead of calling the API when offline mode is set.
// Callers surface it as a usage error, not a run failure: the request was
// well-formed, the operator forbade it.
var ErrOffline = errors.New("offline mode: refusing to call the Anthropic API (unset " + EnvOffline + " / drop --offline to allow it)")

// EnvOfflineSet reports whether the environment forbids model calls.
// An unparseable value is treated as OFF — a typo'd kill-switch that silently
// stopped every model call would be worse than one that does nothing, because
// the CLI reports refusals and cannot report a refusal it never made.
func EnvOfflineSet() bool {
	v, ok := os.LookupEnv(EnvOffline)
	if !ok || v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

// offline reports whether this call must be refused: the explicit config flag
// (from --offline) or the environment.
func (c Config) offline() bool { return c.Offline || EnvOfflineSet() }

// Reporter receives the before/after notices for one model call. The CLI
// implements it over stderr so the notices never contaminate stdout, which
// carries the command's --format json payload.
type Reporter interface {
	// Preflight announces the call about to be made: the model id and the
	// assembled prompt size in bytes.
	Preflight(model string, inputBytes int)
	// Complete reports what the call actually cost. priced is false when the
	// model is absent from the posted price table — the cost is then unknown,
	// never guessed.
	Complete(model string, u Usage, usd float64, priced bool)
}

// StderrReporter is the production Reporter: two lines per call on stderr.
type StderrReporter struct{ W io.Writer }

// Preflight prints the model and input size before the request goes out, so a
// call is visible before it is paid for rather than after.
func (r StderrReporter) Preflight(model string, inputBytes int) {
	fmt.Fprintf(r.w(), "ferret: calling %s — %s of prompt\n", model, humanBytes(inputBytes))
}

// Complete prints the measured token counts and USD cost, dated to the price
// table that produced it.
func (r StderrReporter) Complete(model string, u Usage, usd float64, priced bool) {
	if !priced {
		fmt.Fprintf(r.w(), "ferret: %s used %d in / %d out tokens — cost unknown (%s not in the price table)\n",
			model, u.InputTokens, u.OutputTokens, model)
		return
	}
	fmt.Fprintf(r.w(), "ferret: %s used %d in / %d out tokens — $%.4f (prices as of %s)\n",
		model, u.InputTokens, u.OutputTokens, usd, apiusage.PricedAt)
}

func (r StderrReporter) w() io.Writer {
	if r.W != nil {
		return r.W
	}
	return os.Stderr
}

// CostUSD prices one call from the posted table shared with `ferret usage`, so
// the footer and the corpus-wide ledger cannot drift apart. Reports false when
// the model is unpriced.
func CostUSD(model string, u Usage) (float64, bool) {
	row := apiusage.Row{Model: model, Input: int(u.InputTokens), Output: int(u.OutputTokens)}
	return row.Cost()
}

// humanBytes renders a prompt size at the granularity a reader judges cost by.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fkB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
