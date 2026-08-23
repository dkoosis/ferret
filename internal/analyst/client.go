package analyst

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/dkoosis/keyring"
)

// DefaultModel is the analyst's working model.
//
// RECOMMENDATION (dk's call — overridable via --model): Sonnet 4.6, not Opus.
// Adjudication is a bounded read-and-judge task run over hundreds of candidates
// across the corpus — Sonnet 4.6 has the judgment to apply the false-positive
// discriminator (it holds a whole session spine in its 1M context) at roughly
// half the per-token cost of Opus. Haiku is the wrong floor: the calls that
// matter (is this a count vs navigation? is this dependency-cache out of the
// index's scope?) are exactly where a smaller model regresses toward the ~80%
// deterministic heuristic this layer exists to beat. Use Opus 4.8
// (--model claude-opus-4-8) for calibration runs and the hardest sessions.
const DefaultModel = "claude-sonnet-4-6"

// maxTokens caps the findings list. A session yields tens of findings, not
// hundreds; 8000 stays well under the SDK's non-streaming HTTP-timeout band.
const maxTokens = 8000

// envAPIKey is the app-scoped env var ferret reads its Anthropic credential from,
// per the <APP>_<PROVIDER>_API_KEY standard (nug metered-per-app-api-keys). The
// key is passed to the SDK explicitly, never via the SDK's own ANTHROPIC_API_KEY
// default, so per-app metering isn't defeated by a stray bare key in the env.
const envAPIKey = "FERRET_ANTHROPIC_API_KEY" //nolint:gosec // G101: env var NAME, not a credential

// Keychain coordinates, per the keyring convention: service = app, account =
// provider. The macOS keychain is the primary credential source; envAPIKey is
// the fallback, so headless runs (cron/launchd) and Linux both work without
// activate.sh plumbing.
const (
	keychainService = "ferret"
	keychainAccount = "anthropic"
)

// newStore is a test seam: tests point it at a Store backed by a stub
// `security` binary (keyring.WithSecurityBin) so resolution-order tests
// exercise the real library without touching the developer's keychain.
// Production never reassigns it. Package-wide isolation from a real
// ferret/anthropic item is the keyring.DisableEnv kill-switch, set in
// TestMain.
var newStore = func() (*keyring.Store, error) {
	return keyring.New(keychainService)
}

// resolveKey resolves the credential: explicit Config.APIKey, then the
// keychain-first GetOrEnv chain. The library owns the fallback semantics: a
// keychain that CONFIRMS absence (ErrNotFound) or has no backend
// (ErrUnsupported, i.e. Linux or KEYRING_DISABLE) falls through to envAPIKey;
// a keychain that could not be read (locked, denied, timed out) is a hard
// error — never silently downgraded to the environment. An empty result with
// nil error means "no key anywhere" and is the caller's ErrNoAPIKey case.
func resolveKey(cfg Config) (string, error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	store, err := newStore()
	if err != nil {
		return "", fmt.Errorf("analyst: opening keychain store: %w", err)
	}
	k, err := store.GetOrEnv(keychainAccount, envAPIKey)
	switch {
	case err == nil:
		return k, nil
	case errors.Is(err, keyring.ErrNotFound), errors.Is(err, keyring.ErrUnsupported):
		// Absent from keychain AND env — the caller's ErrNoAPIKey case.
		return "", nil
	default:
		return "", fmt.Errorf("analyst: reading API key from keychain: %w", err)
	}
}

// Config drives one adjudication call.
type Config struct {
	Model  string // model ID; empty → DefaultModel
	APIKey string // explicit key; empty → FERRET_ANTHROPIC_API_KEY from the environment
	// Timeout is an operator deadline bounding the WHOLE call (across the SDK's
	// internal retries), applied via context.WithTimeout. The SDK already caps
	// each attempt (~10min for MaxTokens=8000) and retries ~3×, so a wedged or
	// throttled API can otherwise busy the CLI ~30min with no operator-settable
	// ceiling (ferret-c71). 0 = no extra deadline (SDK defaults stand).
	Timeout time.Duration
	// HTTPClient is a test seam: inject a stub transport (a hanging or erroring
	// Do) to exercise the deadline path without a live API. nil = SDK default.
	HTTPClient option.HTTPClient
	// Offline refuses the call before the request is built (--offline). The
	// FERRET_OFFLINE environment variable does the same process-wide; either is
	// sufficient. See offline.go (ferret-wvo).
	Offline bool
	// Reporter, when set, receives the model+size notice before the call and
	// the token/cost footer after it, so a paid call is never silent. nil =
	// silent, which is what tests and library callers want.
	Reporter Reporter
}

// ErrNoAPIKey signals the run cannot proceed because no credential is set. The
// caller surfaces it with the --emit-prompt escape hatch (assemble the prompt,
// skip the network) so the pipeline is exercisable before a key is wired up.
var ErrNoAPIKey = errors.New("analyst: no API key (store one in the keychain under service \"ferret\" account \"anthropic\", set FERRET_ANTHROPIC_API_KEY, or use --emit-prompt to assemble the prompt without calling the model)")

// HasAPIKey reports whether a credential is available from cfg, the keychain,
// or the environment — lets the command choose between a live run and
// --emit-prompt. An unreadable (locked/denied) keychain is a hard error here
// too, so the precheck never masks the cause behind a generic ErrNoAPIKey.
func (c Config) HasAPIKey() (bool, error) {
	k, err := resolveKey(c)
	if err != nil {
		return false, err
	}
	return k != "", nil
}

func (c Config) model() string {
	if c.Model != "" {
		return c.Model
	}
	return DefaultModel
}

// Run adjudicates one session spine: assembles the prompt, calls Claude with
// adaptive thinking (the recommended mode for 4.6+ — Claude decides depth, no
// token budget to tune), and parses the findings. The raw chain of thought is
// not needed, so thinking display stays at the default.
func Run(ctx context.Context, cfg Config, session, spine string) (Result, error) {
	system, user := BuildPrompt(spine)
	model, text, _, err := complete(ctx, cfg, system, user)
	if err != nil {
		return Result{}, err
	}
	findings, err := ParseFindings(text)
	if err != nil {
		return Result{}, err
	}
	return Result{Session: session, Model: model, Findings: findings}, nil
}

// Usage is the token cost of one model call — the burn a burn-measuring tool is
// obliged to report for its own LLM use (ferret-bbp.5 staged-LLM decision).
type Usage struct{ InputTokens, OutputTokens int64 }

// usageFrom carries the SDK's per-call token counts into the local Usage. Kept a
// pure mapping so it is unit-testable without a network call.
func usageFrom(u anthropic.Usage) Usage {
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

// complete sends one (system, user) turn to Claude with adaptive thinking and
// returns the responding model id, the concatenated text content, and the call's
// token usage. The shared transport for every analyst mode (adjudicate, propose,
// relevance, coverage) — the modes differ only in prompt assembly and response
// parsing, not in how the model is called.
func complete(ctx context.Context, cfg Config, system, user string) (model, text string, usage Usage, err error) {
	// Offline first: refuse before resolving a credential, so the kill-switch
	// works identically on a machine that has a key and one that does not
	// (ferret-wvo).
	if cfg.offline() {
		return "", "", Usage{}, ErrOffline
	}
	key, err := resolveKey(cfg)
	if err != nil {
		return "", "", Usage{}, err
	}
	if key == "" {
		return "", "", Usage{}, ErrNoAPIKey
	}
	// Operator deadline: bound the whole call (all retries) so a wedged/throttled
	// API can't busy the CLI for ~30min with no settable ceiling (ferret-c71).
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	// The key is passed explicitly via WithAPIKey (rather than letting the SDK
	// read its own ANTHROPIC_API_KEY default) so per-app metering isn't defeated
	// by a stray bare key in the env.
	opts := []option.RequestOption{option.WithAPIKey(key)}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	client := anthropic.NewClient(opts...)

	// Announce the call before making it: what model, how much prompt. The
	// notice goes to the Reporter (stderr in the CLI), never stdout, so a
	// --format json payload stays machine-readable.
	if cfg.Reporter != nil {
		cfg.Reporter.Preflight(cfg.model(), len(system)+len(user))
	}

	adaptive := anthropic.ThinkingConfigAdaptiveParam{}
	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     cfg.model(),
		MaxTokens: maxTokens,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
		},
	})
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("analyst: messages call failed: %w", err)
	}

	// max_tokens is the authoritative truncation signal — the response is
	// well-formed-but-incomplete and parsing it would fail with a bare decode
	// error. Surface the typed error here, before parsing, so every mode
	// (adjudicate, propose, relevance, coverage) reports the paid call hit the
	// %d-token cap rather than silently discarding it as malformed (ferret-e6s).
	if resp.StopReason == anthropic.StopReasonMaxTokens {
		// A truncated response was still billed, so it still gets a footer —
		// a silent paid call is exactly what the notice exists to prevent.
		tu := usageFrom(resp.Usage)
		if cfg.Reporter != nil {
			usd, priced := CostUSD(resp.Model, tu)
			cfg.Reporter.Complete(resp.Model, tu, usd, priced)
		}
		return "", "", tu, fmt.Errorf("%w (cap=%d)", ErrTruncatedResponse, maxTokens)
	}

	// Thinking blocks precede the text block; collect the text content.
	var b strings.Builder
	for i := range resp.Content {
		if tb, ok := resp.Content[i].AsAny().(anthropic.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	u := usageFrom(resp.Usage)
	// Footer: what the call actually cost, priced from the same table
	// `ferret usage` reports the corpus with.
	if cfg.Reporter != nil {
		usd, priced := CostUSD(resp.Model, u)
		cfg.Reporter.Complete(resp.Model, u, usd, priced)
	}
	return resp.Model, b.String(), u, nil
}
