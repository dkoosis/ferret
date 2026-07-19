package analyst

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
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
}

// ErrNoAPIKey signals the run cannot proceed because no credential is set. The
// caller surfaces it with the --emit-prompt escape hatch (assemble the prompt,
// skip the network) so the pipeline is exercisable before a key is wired up.
var ErrNoAPIKey = errors.New("analyst: no API key (set FERRET_ANTHROPIC_API_KEY, or use --emit-prompt to assemble the prompt without calling the model)")

// HasAPIKey reports whether a credential is available from cfg or the
// environment — lets the command choose between a live run and --emit-prompt.
func (c Config) HasAPIKey() bool {
	return c.APIKey != "" || os.Getenv(envAPIKey) != ""
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
	if !cfg.HasAPIKey() {
		return "", "", Usage{}, ErrNoAPIKey
	}
	// Operator deadline: bound the whole call (all retries) so a wedged/throttled
	// API can't busy the CLI for ~30min with no settable ceiling (ferret-c71).
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	var opts []option.RequestOption
	// Resolve the key ourselves and pass it explicitly — HasAPIKey already gated,
	// so exactly one of cfg.APIKey / env is set. Passing it via WithAPIKey (rather
	// than letting the SDK read its own ANTHROPIC_API_KEY default) is what keeps
	// per-app metering intact under the renamed var.
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv(envAPIKey)
	}
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if cfg.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(cfg.HTTPClient))
	}
	client := anthropic.NewClient(opts...)

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
		return "", "", usageFrom(resp.Usage), fmt.Errorf("%w (cap=%d)", ErrTruncatedResponse, maxTokens)
	}

	// Thinking blocks precede the text block; collect the text content.
	var b strings.Builder
	for i := range resp.Content {
		if tb, ok := resp.Content[i].AsAny().(anthropic.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return resp.Model, b.String(), usageFrom(resp.Usage), nil
}
