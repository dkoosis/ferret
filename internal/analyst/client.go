package analyst

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

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

// Config drives one adjudication call.
type Config struct {
	Model  string // model ID; empty → DefaultModel
	APIKey string // explicit key; empty → ANTHROPIC_API_KEY from the environment
}

// ErrNoAPIKey signals the run cannot proceed because no credential is set. The
// caller surfaces it with the --emit-prompt escape hatch (assemble the prompt,
// skip the network) so the pipeline is exercisable before a key is wired up.
var ErrNoAPIKey = errors.New("analyst: no API key (set ANTHROPIC_API_KEY, or use --emit-prompt to assemble the prompt without calling the model)")

// HasAPIKey reports whether a credential is available from cfg or the
// environment — lets the command choose between a live run and --emit-prompt.
func (c Config) HasAPIKey() bool {
	return c.APIKey != "" || os.Getenv("ANTHROPIC_API_KEY") != ""
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
	model, text, err := complete(ctx, cfg, system, user)
	if err != nil {
		return Result{}, err
	}
	findings, err := ParseFindings(text)
	if err != nil {
		return Result{}, err
	}
	return Result{Session: session, Model: model, Findings: findings}, nil
}

// complete sends one (system, user) turn to Claude with adaptive thinking and
// returns the responding model id and the concatenated text content. The shared
// transport for both analyst modes (adjudicate, propose) — the modes differ only
// in prompt assembly and response parsing, not in how the model is called.
func complete(ctx context.Context, cfg Config, system, user string) (model, text string, err error) {
	if !cfg.HasAPIKey() {
		return "", "", ErrNoAPIKey
	}
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
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
		return "", "", fmt.Errorf("analyst: messages call failed: %w", err)
	}

	// Thinking blocks precede the text block; collect the text content.
	var b strings.Builder
	for i := range resp.Content {
		if tb, ok := resp.Content[i].AsAny().(anthropic.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return resp.Model, b.String(), nil
}
