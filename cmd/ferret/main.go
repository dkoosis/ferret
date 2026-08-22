// ferret mines Claude Code transcripts for repeated behavior:
// scriptable routines, friction loops, and noisy context.
//
// AX-first: dense default output, --format json everywhere, hard output caps.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/dkoosis/ferret/internal/analyst"
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
)

var (
	errBadFormat    = usage("bad --format")
	errMaxBytesJSON = usage("--max-bytes is not supported with --format json (use --limit)")
	errMinSupport   = usage("--min-support must be ≥ 1 (0 or negative grows the pattern lattice unbounded)")
	errMaxGap       = usage("--max-gap must be ≥ 1")
	errMaxLen       = usage("--max-len must be ≥ 1")
	errOrder        = usage("--order must be ≥ 1")
	errBadSource    = usage("bad --source")
)

// usageError marks a sentinel as a complaint about the COMMAND LINE rather
// than about the run — the distinction the contract's exit-code split turns
// on. Declaring it at the sentinel (rather than listing sentinels at the exit
// path) is what keeps the classification from drifting: a new validation error
// is usage-classified by construction, and cannot be forgotten in a list far
// from where it was written.
//
// Each sentinel is a distinct pointer, so errors.Is against it keeps working
// through `fmt.Errorf("%w: %q", …)` exactly as an errors.New value would.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// usage declares a command-line validation sentinel. Use it for anything the
// caller could fix by retyping the command; leave runtime failures
// (unreadable file, malformed input data, absent API key) on errors.New —
// those are not the caller's typing.
func usage(msg string) error { return &usageError{msg} }

// validateSeqParams rejects the PrefixSpan bounds that would otherwise blow up
// the search (ferret-g2o): a non-positive --min-support makes every token a
// frequent root, and --max-gap/--max-len below 1 are degenerate. Checked at the
// command boundary — like --format/--n/--by — so a typo errors loudly instead of
// pinning a core and growing memory until OOM.
func validateSeqParams(minSupport, maxGap, maxLen int) error {
	switch {
	case minSupport < 1:
		return errMinSupport
	case maxGap < 1:
		return errMaxGap
	case maxLen < 1:
		return errMaxLen
	}
	return nil
}

// shared JSON response keys — every truncating JSON response carries
// keyTotal + keyTruncated (the AX truncation contract)
const (
	fmtJSON      = "json"
	fmtMD        = "md"
	fmtText      = "text"
	keyLens      = "lens"
	keyRows      = "rows"
	keySessions  = "sessions"
	keyTotal     = "total"
	keyTruncated = "truncated"
)

// Exit codes, per the DK-AXI operating contract: a caller distinguishes a
// mistyped invocation from a run that tried and failed.
const (
	exitFailure = 1 // the command ran and could not finish
	exitUsage   = 2 // the command line itself was wrong (unknown flag, bad value)
)

// ---- CLI grammar ----

// userHomeDir is indirected through a var so a test can fault-inject a missing
// home dir (HOME/USERPROFILE unset) without manipulating process env globally.
var userHomeDir = os.UserHomeDir

// errNoHomeDir explains why a default path could not be resolved and points the
// user at the explicit flag that bypasses home-dir resolution.
var errNoHomeDir = errors.New("cannot resolve home directory (set --data/--root explicitly)")

// defaultData returns ~/.ferret. It surfaces the UserHomeDir error rather than
// discarding it: with HOME unset, a discarded error yields home=="" and a
// RELATIVE ".ferret" path, so artifacts land under the current working dir and
// the corpus location silently depends on CWD.
func defaultData() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoHomeDir, err)
	}
	return filepath.Join(home, ".ferret"), nil
}

// defaultRoot returns ~/.claude/projects. Like defaultData, it surfaces the
// UserHomeDir error instead of falling back to a relative path.
func defaultRoot() (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoHomeDir, err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// resolveRoot returns root unchanged, or the default transcript root when the
// flag is empty. The transcript-family subcommands (spine/segment/dialogue/…)
// share this --root resolution.
func resolveRoot(root string) (string, error) {
	if root != "" {
		return root, nil
	}
	return defaultRoot()
}

// validateFormat rejects any --format outside the text|json pair the
// transcript-family subcommands render.
func validateFormat(format string) error {
	if format != fmtText && format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, format)
	}
	return nil
}

// newAnalystRun assembles an analyst.Config from --model/--timeout, checks the
// API key is present, and derives the SIGINT-cancellable context every analyst
// call runs under (analystContext, cmd/ferret/adjudicate.go) — the preamble
// adjudicate/over-initiative/helped's LLM legs all repeated by hand
// (ferret-9l1). A non-nil error means neither ctx nor stop is usable; ctx is
// nil and stop is nil in that case, so a caller must check err before deferring
// stop.
func newAnalystRun(model string, timeout time.Duration) (ctx context.Context, cfg analyst.Config, stop func(), err error) {
	cfg = analyst.Config{Model: model, Timeout: timeout}
	ok, err := cfg.HasAPIKey()
	if err != nil {
		return nil, analyst.Config{}, nil, err
	}
	if !ok {
		return nil, analyst.Config{}, nil, analyst.ErrNoAPIKey
	}
	ctx, stop = analystContext()
	return ctx, cfg, stop, nil
}

// warnAmbiguousSession prints ferret's standard disambiguation notice when a
// --session prefix (session) matches more than one session (distinct > 1):
// which one was picked (chosen), so dk can supply a longer prefix instead of
// silently trusting the pick. verb names what the command does with the
// session ("scoring", "emitting") so the message reads naturally per caller.
// distinct ≤ 1 is a no-op — the common case, unambiguous, prints nothing.
func warnAmbiguousSession(session string, distinct int, chosen, verb string) {
	if distinct <= 1 {
		return
	}
	fmt.Fprintf(os.Stderr,
		"ferret: --session %q matched %d sessions; %s %q (use a longer prefix to disambiguate)\n",
		session, distinct, verb, chosen)
}

// CommonFlags are shared across all analysis subcommands.
type CommonFlags struct {
	Data     string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
	Format   string `help:"Output format: text|json (graph: +mermaid|dot|sankey)." default:"text" name:"format"`
	Limit    int    `help:"Max rows (unset = the command's compact default; N = exactly N; negative = unlimited)." default:"0" name:"limit"`
	MaxBytes int    `help:"Max output bytes, text only (0 = unlimited)." default:"0" name:"max-bytes"`
}

// LensFlags are shared across all subcommands that build a corpus.
type LensFlags struct {
	Lens        string `help:"Token lens: coarse|tool|target|exact." default:"tool" name:"lens"`
	NoMarkFail  bool   `help:"Don't append ! to failed-action tokens." name:"no-mark-fail"`
	NoCollapse  bool   `help:"Don't run-length collapse repeated tokens." name:"no-collapse"`
	NoSidechain bool   `help:"Exclude sidechain events." name:"no-sidechain"`
}

// CLI is the root grammar parsed by kong.
var CLI struct {
	Ingest struct {
		Data       string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		Root       string `help:"Transcript root (dir or .jsonl file)." name:"root"`
		Project    string `help:"Only projects whose slug contains this substring." name:"project"`
		DryRun     bool   `help:"Scan and report; write nothing." name:"dry-run"`
		SnipeUsage string `help:"Glob of snipe .snipe/usage.jsonl files to join by session, e.g. ~/Projects/*/.snipe/usage.jsonl." name:"snipe-usage"`
	} `cmd:"" help:"Build ~/.ferret/events.jsonl from transcripts." name:"ingest"`

	Summary struct {
		CommonFlags
		By string `help:"Aggregation grain: corpus|project|session." default:"corpus" name:"by"`
	} `cmd:"" help:"Corpus health + tool mix."`

	Ngrams struct {
		CommonFlags
		LensFlags
		N           string `help:"Gram lengths ≥2, e.g. 3 or 2-5." default:"2-5" name:"n"`
		MinCount    int    `help:"Min total occurrences." default:"5" name:"min-count"`
		MinSessions int    `help:"Min distinct streams." default:"3" name:"min-sessions"`
	} `cmd:"" help:"Repeated n-grams across streams."`

	Seqs struct {
		CommonFlags
		LensFlags
		MinSupport int `help:"Min distinct streams containing the pattern." default:"20" name:"min-support"`
		MaxGap     int `help:"Max positions between consecutive items (1 = adjacent)." default:"3" name:"max-gap"`
		MaxLen     int `help:"Max pattern length." default:"5" name:"max-len"`
	} `cmd:"" help:"Gapped subsequences (PrefixSpan)."`

	Rank struct {
		CommonFlags
		LensFlags
		MinSupport int `help:"Min distinct streams containing the pattern." default:"20" name:"min-support"`
		MaxGap     int `help:"Max positions between consecutive items (1 = adjacent)." default:"3" name:"max-gap"`
		MaxLen     int `help:"Max pattern length." default:"5" name:"max-len"`
		Order      int `help:"Gram-model order for cohesion scoring." default:"3" name:"order"`
		Top        int `help:"Max cards per bucket." default:"10" name:"top"`
	} `cmd:"" help:"Ranked review queue (cohesion-scored, bucketed)."`

	Report struct {
		CommonFlags
		LensFlags
		MinSupport int    `help:"Min distinct streams containing the pattern." default:"20" name:"min-support"`
		MaxGap     int    `help:"Max positions between consecutive items (1 = adjacent)." default:"3" name:"max-gap"`
		MaxLen     int    `help:"Max pattern length." default:"5" name:"max-len"`
		Order      int    `help:"Gram-model order for cohesion scoring." default:"3" name:"order"`
		Top        int    `help:"Max cards per bucket fed to the projection." default:"10" name:"top"`
		Kind       string `help:"Only this kind: routine|friction|loop|noise (default: all but noise)." name:"kind"`
		SinceFixes bool   `help:"Annotate findings against the fix ledger: fixed-date + burn baseline→now." name:"since-fixes"`
	} `cmd:"" help:"Findings: motifs classified into actions, ranked by measured burn."`

	Surprise struct {
		CommonFlags
		LensFlags
		Order   int `help:"Model order: predict each token from up to N prior tokens." default:"3" name:"order"`
		MinToks int `help:"Skip streams shorter than this." default:"20" name:"min-toks"`
	} `cmd:"" help:"Per-session predictability (low=scriptable, high=thrash)."`

	Graph struct {
		CommonFlags
		LensFlags
		MinCount int  `help:"Min edge count." default:"20" name:"min-count"`
		Loops    bool `help:"Show A→B→A bounce cycles (friction signatures)." name:"loops"`
	} `cmd:"" help:"Token transition graph."`

	Tokens struct {
		CommonFlags
		LensFlags
		Session string `help:"Session ID prefix (required)." required:"" name:"session"`
	} `cmd:"" help:"One session's token stream (lens debugger)."`

	Spine struct {
		Session string `help:"Session ID prefix (required)." required:"" name:"session"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
	} `cmd:"" help:"Compact session spine: prompts + thinking + tool calls + result status/size."`

	Segments struct {
		Session string `help:"Session ID prefix (required)." required:"" name:"session"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Deterministic task-boundary candidates (1 per user prompt) + thinking-pivot hints."`

	Dialogue struct {
		Session string `help:"Session ID prefix (required)." required:"" name:"session"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Tag user turns: per-turn repair/accept moves + PARADISE outcome rollup (regex-first; v1)."`

	Search struct {
		Query   string `arg:"" help:"Literal substring to find (case-sensitive; regex/case-fold are follow-ups)." name:"query"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
		Limit   int    `help:"Max hits (0 = unlimited)." default:"20" name:"limit"`
		Context int    `help:"Entries (turn blocks) of context before/after each hit." default:"2" name:"context"`
	} `cmd:"" help:"Find a literal string across all transcripts: hits show session + surrounding turns, capped at --limit."`

	Candidates struct {
		Session     string `help:"Session ID prefix. Omit for corpus-recurrence mode (rank task-shapes across all sessions)." name:"session"`
		Root        string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format      string `help:"Output format: text|json." default:"text" name:"format"`
		Top         int    `help:"Max candidate tasks/shapes (0 = all)." default:"10" name:"top"`
		MinSessions int    `help:"Corpus mode: min distinct sessions a shape must recur in." default:"2" name:"min-sessions"`
		Conformance string `help:"Optional JSON file of per-task reference plans ([{task,reference,observed}]); folds kuv.4 conformance into the leak score for covered tasks (--session mode)." name:"conformance"`
	} `cmd:"" help:"Rank a session's tasks (--session), or recurring task-shapes across the whole corpus (no --session), as cost-leak candidates for the analyst proposal loop."`

	Conformance struct {
		Spec   string `help:"JSON spec file (reference plan + observed labeled calls); '-' or empty = stdin." name:"spec"`
		Format string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Score a task's calls against a reference plan: fitness/precision + alignment localizes the deviating call."`

	Landmark struct {
		Spec    string `help:"JSON spec file (loose milestone set + observed shape tokens); '-' or empty = stdin. Single-task spec scope." name:"spec"`
		Session string `help:"Session ID prefix: segment the session and score each task's goal against its milestone-set library (no spec needed). Whole-session scope." name:"session"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout); --session scope only." name:"root"`
		Data    string `help:"Artifact directory (corpus for uniqueness weighting; uniform fallback if absent)." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Score goal PROGRESS by necessary milestones touched (set-coverage, backtrack-tolerant), uniqueness-weighted. --spec scores one supplied task; --session segments a session and scores each task against the milestone-set library."`

	Gates struct {
		CommonFlags
	} `cmd:"" help:"Mine review gates (code-review/plan-review/precommit/QA): per-gate rejection sets + overlap ratio ω (high ω = redundant gate) + confirmed friction loops."`

	// default:"withargs" makes a bare `ferret` (and `ferret --data X`) run
	// status instead of erroring into the synopsis — AXI #8, content first.
	// `ferret --help` still prints the full command list.
	Status StatusCmd `cmd:"" default:"withargs" help:"Corpus health + the heaviest waste rows (the bare-ferret default)." name:"status"`

	Friction FrictionCmd `cmd:"" help:"One ranked table of estimated wasted bytes — polling, misfires and motif findings merged, priced by burn." name:"friction"`

	Burn BurnCmd `cmd:"" help:"Ranked corpus-wide context-byte burn per normalized command (the tune-up list)."`

	Misfires MisfiresCmd `cmd:"" help:"Rank repeated command misfires + repair pairs corpus-wide."`

	Polling PollingCmd `cmd:"" help:"Rank exact-duplicate commands repeated within a session." name:"polling"`

	Parallel ParallelCmd `cmd:"" help:"Rank context spend by how many threads ran at once — session overlap vs subagent fan-out." name:"parallel"`

	Substitutable SubstitutableCmd `cmd:"" help:"Rank Bash calls a native tool (Grep/Glob/Read) could replace — deterministic, no judge." name:"substitutable"`

	Retrieval struct {
		CommonFlags
		Session    string        `help:"Only episodes from sessions matching this prefix/substring (omit for whole corpus)." name:"session"`
		Hop1       bool          `help:"Grade each episode's Hop1 interp-fidelity (LLM judge; requires --session, reports its own token cost)." name:"hop1"`
		Model      string        `help:"Claude model ID for --hop1 (default: claude-sonnet-4-6)." name:"model"`
		EmitPrompt bool          `help:"With --hop1: assemble + print each escalated episode's judge prompt without calling the model." name:"emit-prompt"`
		Timeout    time.Duration `help:"With --hop1: operator deadline for the analyst call across all retries (0 = SDK defaults)." default:"5m" name:"timeout"`
	} `cmd:"" help:"Score get_nug retrieval episodes: RU northstar (consumed/firsttry/nonabandon) + det Q/R/C scorers."`

	Quality struct {
		Session string `help:"Session ID prefix for per-task axes. Omit for corpus-wide pass^k consistency (cluster recurring task-shapes across all sessions)." name:"session"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Reference-free quality: per-task efficiency/adaptivity axes (--session), or corpus pass^k consistency over recurring task-shapes (no --session)."`

	Helped struct {
		Session string `help:"Session ID prefix (required): the transcript whose task segments the events join against." required:"" name:"session"`
		Events  string `help:"Retrieval-event JSONL (trixi sidecar rows); '-' or empty = stdin." name:"events"`
		Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Data    string `help:"Artifact directory (the label ledger this session's feedback-tap probe answers are excluded via)." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		Format  string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Adjudicate retrieval outcomes (helped|ignored|misled|conflict|no_signal): join each search event to its task segment by timestamp, then apply the deterministic lattice. Live ts→segment join (bbp.16)."`

	Adjudicate struct {
		Session     string        `help:"Session ID prefix (required unless --recall-trace)." name:"session"`
		Root        string        `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Model       string        `help:"Claude model ID (default: claude-sonnet-4-6; use claude-opus-4-8 for calibration)." name:"model"`
		Format      string        `help:"Output format: text|json." default:"text" name:"format"`
		EmitPrompt  bool          `help:"Assemble + print the prompt without calling the model (no API key needed)." name:"emit-prompt"`
		Propose     bool          `help:"Propose mode: feed the cost-leak candidates + spine and return one fix per task (automate/de-context) instead of mismatch verdicts." name:"propose"`
		RecallTrace string        `help:"Recall-use mode: judge trixi-bot's recall-trace.jsonl (recalled fragments + final answer per run) for memory use/miss instead of a transcript session. Emits the findings shape trixi-bot's eval/adjudicate consumes." name:"recall-trace"`
		Top         int           `help:"Propose mode: max candidate tasks fed to the analyst (0 = all)." default:"10" name:"top"`
		Timeout     time.Duration `help:"Operator deadline for the analyst call across all retries (0 = SDK defaults)." default:"5m" name:"timeout"`
	} `cmd:"" help:"LLM analyst: flag tool-for-intent mismatches in a session, --propose cost-cutting fixes, or --recall-trace judge memory use/miss (precision layer; dk validates)."`

	OverInitiative struct {
		Session    string        `help:"Session ID prefix (required)." name:"session"`
		Root       string        `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Model      string        `help:"Claude model ID (default: claude-sonnet-4-6; use claude-opus-4-8 for calibration)." name:"model"`
		Format     string        `help:"Output format: text|json." default:"text" name:"format"`
		EmitPrompt bool          `help:"Assemble + print each candidate's judge prompt without calling the model (no API key needed)." name:"emit-prompt"`
		Timeout    time.Duration `help:"Operator deadline for the analyst call across all retries (0 = SDK defaults)." default:"5m" name:"timeout"`
	} `cmd:"" name:"over-initiative" help:"LLM judge of the NO-PUSHBACK over-initiative case: episodes where the agent took a mutating action beyond an advice/review-scoped prompt and the human let it stand (the case the deterministic floor can't read). Precision layer; dk validates. (bbp.18)"`

	Feedback struct {
		Prep struct {
			Session string `help:"Session ID prefix (required)." required:"" name:"session"`
			Data    string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Events  string `help:"Retrieval-live events directory (default ~/.trixi/telemetry; the newest retrieval-live-YYYY-MM.jsonl in it is tailed)." name:"events"`
		} `cmd:"" help:"Pure, network-free: tail the live retrieval-event feed for the oldest settled kind:search row this session hasn't judged yet. Prints {\"pending\":bool,\"search_event_id\":...,\"nug_ids\":[...]}."`
		Judge struct {
			Session     string        `help:"Session ID prefix (required)." required:"" name:"session"`
			Root        string        `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
			Data        string        `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Events      string        `help:"Retrieval-live events directory (default ~/.trixi/telemetry)." name:"events"`
			SearchEvent string        `help:"The kind:search event id to judge (from 'feedback prep')." required:"" name:"search-event"`
			Model       string        `help:"Claude model ID (default: claude-sonnet-4-6)." name:"model"`
			Timeout     time.Duration `help:"Operator deadline for the analyst call across all retries (0 = SDK defaults)." default:"2m" name:"timeout"`
		} `cmd:"" help:"Re-derive the deterministic helped verdict for one search event, run analyst.RunRelevance over its returned nugs' bodies ([{id,text}] on stdin), and bank an AskCandidate on disagreement (feedback.Select). No-ops silently when the lattice has no record or SearchFit can't classify."`
		Check struct {
			Session string `help:"Session ID prefix (required)." required:"" name:"session"`
			Data    string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		} `cmd:"" help:"UserPromptSubmit side: read the banked AskCandidate (if any), check feedback.Reserve's budget, print {\"ask\":bool,\"question\":string}. One-shot: clears the bank file either way."`
		Answer struct {
			Session string `help:"Session ID prefix (required)." required:"" name:"session"`
			Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
			Data    string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		} `cmd:"" help:"UserPromptSubmit side, one turn after a granted 'check': read dk's raw prompt from stdin, consume the armed candidate, confirm the ask actually rendered, classify the leading y/n/s token (ferret-j33), and write the gold label. No armed candidate = silent no-op."`
	} `cmd:"" help:"In-session feedback tap live-wiring (ferret-wf9.1/j33): prep (tail+settle) -> judge (RunRelevance+Select) -> check (budget+inject) -> answer (recognize+label). See .claude/hooks/feedback-*.sh."`

	Fixes struct {
		Add struct {
			Data        string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Lens        string `help:"Token lens used to capture the baseline burn (match your scan)." default:"tool" name:"lens"`
			Motif       string `help:"Comma-joined motif tokens — the report's stable join key, e.g. \"Edit!,Read\"." required:"" name:"motif"`
			Fix         string `help:"The fix artifact, or the reason for a wontfix/watch verdict." required:"" name:"fix"`
			Note        string `help:"Optional free-text note." name:"note"`
			Disposition string `help:"Verdict: fix (capture baseline+delta) | wontfix | watch (suppress motif from report with reason, no delta)." default:"fix" enum:"fix,wontfix,watch" name:"disposition"`
		} `cmd:"" help:"Record motif→fix, capturing the motif's current burn as the baseline."`
		List struct {
			Data   string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Format string `help:"Output format: text|json." default:"text" name:"format"`
		} `cmd:"" help:"List recorded fixes."`
		Sub struct {
			Data    string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Intent  string `help:"Normalized intent class the mismatch belongs to (kebab, e.g. \"symbol-callers\") — the dedup key with --better." required:"" name:"intent"`
			Wrong   string `help:"The tool actually reached for (e.g. \"rg\")." required:"" name:"wrong"`
			Better  string `help:"Executable substitution template the consumer runs instead (e.g. \"snipe callers <sym>\")." required:"" name:"better"`
			Example string `help:"One verbatim offending call, for traceability." name:"example"`
			Session string `help:"Source session prefix the mismatch was confirmed in." name:"session"`
		} `cmd:"" help:"Record a confirmed adjudicate mismatch (fit=mismatch) as a substitution rule; a repeat intent→fix bumps its occurrence count (dk validates before recording). ferret-kuv.15."`
		Subs struct {
			Data   string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
			Format string `help:"Output format: text|json." default:"text" name:"format"`
		} `cmd:"" help:"List recorded substitution rules (intent→better, occurrence count)."`
		Proposals struct {
			Session string `help:"Session ID prefix (required)." required:"" name:"session"`
			Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
			Format  string `help:"Output format: text|json." default:"text" name:"format"`
		} `cmd:"" help:"Mine assistant self-audit text for confessed call-waste; prints candidate substitutions for dk to confirm via 'fixes sub' (regex/heuristic only, no LLM leg, no auto-record). ferret-kuv.16."`
	} `cmd:"" help:"Fix ledger: record motif→fix, then 'report --since-fixes' computes burn-delta."`

	Reach struct {
		Root     string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Since    string `help:"Window start YYYY-MM-DD (default: 7 days ago)." name:"since"`
		Until    string `help:"Window end YYYY-MM-DD, inclusive (default: today)." name:"until"`
		Project  string `help:"Only projects whose slug contains this substring." name:"project"`
		Session  string `help:"Only sessions matching this prefix/substring." name:"session"`
		Format   string `help:"Output format: text|json|md (md = ledger-appendable block)." default:"text" name:"format"`
		Limit    int    `help:"Max opportunity rows (0 = unlimited)." default:"0" name:"limit"`
		MaxBytes int    `help:"Max output bytes, text only (0 = unlimited)." default:"0" name:"max-bytes"`
	} `cmd:"" help:"Reach-rate: at recall opportunities in a date window, did Claude reach the trixi store FIRST (store) vs grep/gh/none. Keystone metric for tx-qw86; transcript-only (Phase 1)."`

	Recurrence struct {
		CommonFlags
		Signatures string `help:"Known-signatures JSONL file (default: <data>/friction_signatures.jsonl; absent = learn signatures from the corpus)." name:"signatures"`
	} `cmd:"" help:"Friction-recurrence detector: flag the 2nd+ occurrence of a known friction signature (normalized command/error fingerprint). Emits match records for the /wrap trap-graduation prompt."`

	Emit struct {
		Data       string  `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		Root       string  `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Project    string  `help:"Only projects whose slug contains this substring." name:"project"`
		Order      int     `help:"Surprisal model order: score each token from up to N prior tokens." default:"3" name:"order"`
		Window     int     `help:"Candidate span width in tokens." default:"8" name:"window"`
		MinBits    float64 `help:"Min per-span mean surprisal to emit a candidate (<0 = auto: corpus mean)." default:"-1" name:"min-bits"`
		DryRun     bool    `help:"Derive + print candidates; write no spool rows, cursor, or signatures." name:"dry-run"`
		Format     string  `help:"Output format: text|json." default:"text" name:"format"`
		Signatures string  `help:"Known friction-signatures JSONL (default: <data>/friction_signatures.jsonl)." name:"signatures"`
	} `cmd:"" help:"Emit candidate spool rows (schema_version 1) from salient transcript spans: per-span surprisal + friction recurrence + Drain fingerprint, appended to ~/.ferret/spool/candidates-YYYY-MM.jsonl. Deterministic, LLM-free — no claim text. The producer side of the sensor→kg pipeline (epic gg-eqn)."`
}

func main() {
	// Resolve dynamic defaults before parsing.
	// kong supports ${...} interpolation only for env vars in default tags,
	// so we patch the struct directly before Parse sees it.
	if CLI.Ingest.Root == "" {
		root, err := defaultRoot()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ferret:", err)
			os.Exit(2)
		}
		CLI.Ingest.Root = root
	}
	if CLI.Ingest.Data == "" {
		data, err := defaultData()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ferret:", err)
			os.Exit(2)
		}
		CLI.Ingest.Data = data
	}

	k := kong.Parse(&CLI,
		kong.Name("ferret"),
		kong.Description(
			"Mine Claude Code transcripts for repeated behavior:\n"+
				"scriptable routines, friction loops, and noisy context.\n\n"+
				"  ferret ingest   [--root DIR] [--project SUBSTR] [--dry-run] [--snipe-usage GLOB]\n"+
				"  ferret summary  [--by corpus|project|session]\n"+
				"  ferret ngrams   [--lens tool] [--n 2-5] [--min-count 5] [--min-sessions 3]\n"+
				"  ferret seqs     [--lens tool] [--min-support 20] [--max-gap 3] [--max-len 5]\n"+
				"  ferret rank     [--lens tool] [--min-support 20] [--order 3] [--top 10]\n"+
				"  ferret report   [--lens tool] [--kind routine|friction|loop|noise] [--since-fixes] [--format json|md]\n"+
				"  ferret surprise [--lens tool] [--order 3] [--min-toks 20]\n"+
				"  ferret graph    [--lens tool] [--min-count 20] [--format text|json|mermaid|dot|sankey] [--loops]\n"+
				"  ferret tokens   --session PREFIX [--lens tool]\n"+
				"  ferret spine    --session PREFIX [--root DIR]\n"+
				"  ferret segments --session PREFIX [--root DIR] [--format text|json]\n"+
				"  ferret dialogue --session PREFIX [--root DIR] [--format text|json]\n"+
				"  ferret search   QUERY [--root DIR] [--limit 20] [--context 2] [--format text|json]   (literal substring; case-sensitive)\n"+
				"  ferret candidates [--session PREFIX | (corpus) --min-sessions 2] [--root DIR] [--top 10] [--format text|json]\n"+
				"  ferret conformance [--spec FILE] [--format text|json]   (reads stdin if no --spec)\n"+
				"  ferret landmark  [--spec FILE | --session PREFIX [--root DIR]] [--data DIR] [--format text|json]   (milestone progress; spec reads stdin if no --spec)\n"+
				"  ferret gates    [--data DIR] [--format text|json]   (overlap ratio ω over review-gate rejections)\n"+
				"  ferret status   [--data DIR] [--format text|json]   (corpus health + heaviest waste — what bare `ferret` runs)\n"+
				"  ferret friction [--data DIR] [--source poll|misfire|motif] [--no-motifs] [--format text|json]   (ONE waste-ranked table: polling + misfires + motifs, priced by burn)\n"+
				"  ferret burn     [--data DIR] [--format text|json]   (ranked context-byte burn per normalized command)\n"+
				"  ferret misfires [--data DIR] [--format text|json]   (repeated command failures + repair pairs)\n"+
				"  ferret polling  [--data DIR] [--format text|json]   (exact-duplicate commands repeated within a session)\n"+
				"  ferret retrieval  [--session PREFIX] [--format text|json]   (get_nug search quality: RU + Q/R/C)\n"+
				"  ferret quality    [--session PREFIX] [--format text|json]   (per-task axes; corpus pass^k consistency)\n"+
				"  ferret adjudicate  --session PREFIX [--model ID] [--emit-prompt] [--propose] [--top 10] [--format text|json]\n"+
				"  ferret feedback prep  --session PREFIX   (tail the live retrieval feed for a settled kind:search event)\n"+
				"  ferret feedback judge --session PREFIX --search-event ID   ([{id,text}] candidates on stdin)\n"+
				"  ferret feedback check --session PREFIX   (UserPromptSubmit side: budget-check + read the banked ask)\n"+
				"  ferret feedback answer --session PREFIX   (one turn later: recognize y/n/s, classify valence, write the label; prompt on stdin)\n"+
				"  ferret fixes add  --motif \"Edit!,Read\" --fix \"hookify read-before-edit\" [--note ...]\n"+
				"  ferret fixes list [--format json]\n"+
				"  ferret reach    [--since Y-M-D] [--until Y-M-D] [--project SUBSTR] [--format text|json|md]   (recall-opportunity reach-rate)\n"+
				"  ferret recurrence [--signatures FILE] [--format text|json]   (flag 2nd+ occurrence of a known friction signature)\n"+
				"  ferret emit     [--root DIR] [--window 8] [--order 3] [--min-bits N] [--dry-run] [--format text|json]   (candidate spool rows → ~/.ferret/spool)\n\n"+
				"common: --data DIR (default ~/.ferret)  --format text|json  --limit N (negative = unlimited)  --max-bytes N\n"+
				"lenses: coarse | tool | target | exact",
		),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		// DK-AXI operating-contract rule 3: an unknown flag or an unparseable
		// command line is fatal with a DISTINCT code, so a caller can tell
		// "you typed it wrong" (2) from "the run failed" (1). kong's own
		// default here is 80, which collides with nothing but means nothing
		// either.
		kong.Exit(func(code int) {
			if code != 0 {
				code = exitUsage
			}
			os.Exit(code)
		}),
	)

	var err error
	switch k.Command() {
	case "ingest":
		err = cmdIngest()
	case "summary":
		err = cmdSummary()
	case "ngrams":
		err = cmdNgrams()
	case "seqs":
		err = cmdSeqs()
	case "rank":
		err = cmdRank()
	case "report":
		err = cmdReport()
	case "surprise":
		err = cmdSurprise()
	case "graph":
		err = cmdGraph()
	case "tokens":
		err = cmdTokens()
	case "spine":
		err = cmdSpine()
	case "segments":
		err = cmdSegments()
	case "dialogue":
		err = cmdDialogue()
	case "search <query>":
		err = cmdSearch()
	case "candidates":
		err = cmdCandidates()
	case "conformance":
		err = cmdConformance()
	case "landmark":
		err = cmdLandmark()
	case "gates":
		err = cmdGates()
	case "status":
		err = cmdStatus(&CLI.Status)
	case "friction":
		err = cmdFriction(&CLI.Friction)
	case "burn":
		err = cmdBurn(&CLI.Burn)
	case "misfires":
		err = cmdMisfires(CLI.Misfires)
	case "polling":
		err = cmdPolling(CLI.Polling)
	case "parallel":
		err = cmdParallel(&CLI.Parallel)
	case "substitutable":
		err = cmdSubstitutable(CLI.Substitutable)
	case "retrieval":
		err = cmdRetrieval()
	case "quality":
		err = cmdQuality()
	case "helped":
		err = cmdHelped()
	case "adjudicate":
		err = cmdAdjudicate()
	case "over-initiative":
		err = cmdOverInitiative()
	case "feedback prep":
		err = cmdFeedbackPrep()
	case "feedback judge":
		err = cmdFeedbackJudge()
	case "feedback check":
		err = cmdFeedbackCheck()
	case "feedback answer":
		err = cmdFeedbackAnswer()
	case "fixes add":
		err = cmdFixesAdd()
	case "fixes list":
		err = cmdFixesList()
	case "fixes sub":
		err = cmdFixesSub()
	case "fixes subs":
		err = cmdFixesSubs()
	case "fixes proposals":
		err = cmdFixesProposals()
	case "reach":
		err = cmdReach()
	case "recurrence":
		err = cmdRecurrence()
	case "emit":
		err = cmdEmit()
	default:
		k.Fatalf("unknown command %q", k.Command())
	}
	if err != nil {
		// Diagnostics to stderr, nonzero exit (contract rule 4) — stdout stays
		// machine-consumable, so a `| jq` on a failed run gets an empty stream
		// rather than a prose line it can't parse. A usage-shaped error (a bad
		// flag VALUE, which kong accepts and the command rejects) exits 2 like
		// a bad flag NAME; anything else is a run that failed.
		fmt.Fprintln(os.Stderr, "ferret:", err)
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor maps a command error to the contract's exit codes: the
// command-line validation sentinels are usage errors, everything else is a
// failed run.
func exitCodeFor(err error) int {
	if _, ok := errors.AsType[*usageError](err); ok {
		return exitUsage
	}
	return exitFailure
}

// ---- shared helpers ----
//
// Everything below is plumbing shared by 2+ of the per-command files in this
// package (cmd* implementations live in their own file each — see ingest.go,
// summary.go, ngrams.go, seqs.go, rank.go, report.go, fixes_*.go, surprise.go,
// graph.go, tokens.go — plus the already-split spine/segment/dialogue/search/
// candidates/conformance/landmark/gates/retrieval/quality/helped/adjudicate/
// overinitiative/feedback/reach/recurrence files). Centralizing it here (rather
// than attaching each helper to whichever cmd* happened to sit nearest it in
// the old monolith) keeps a helper's home stable as call sites move —
// ferret-5y6 applied the same order to internal/durable.

// common wraps CommonFlags with helper methods (kept as a receiver type so
// the analysis helpers—validate, eventsPath, ensureData—remain unchanged).
type common struct {
	data     string
	format   string
	limit    int
	maxBytes int
}

func fromCommonFlags(cf CommonFlags) (*common, error) {
	data := cf.Data
	if data == "~/.ferret" {
		d, err := defaultData()
		if err != nil {
			return nil, err
		}
		data = d
	}
	return &common{data: data, format: cf.Format, limit: cf.Limit, maxBytes: cf.MaxBytes}, nil
}

func (c *common) eventsPath() string { return filepath.Join(c.data, "events.jsonl") }

// validate rejects unknown --format values (a typo must not silently produce
// text output) and --max-bytes with json (no streaming cap — refuse rather
// than truncate silently or emit invalid JSON).
func (c *common) validate(formats ...string) error {
	ok := false
	for _, f := range formats {
		if c.format == f {
			ok = true
		}
	}
	if !ok {
		return fmt.Errorf("%w: %q (want %s)", errBadFormat, c.format, strings.Join(formats, "|"))
	}
	if c.format == fmtJSON && c.maxBytes > 0 {
		return errMaxBytesJSON
	}
	return nil
}

// manifestComplete reports whether the manifest at path is a trustworthy
// completeness sentinel. A bare os.Stat is not sufficient: an interrupted or
// crashed ingest can leave a present-but-0-byte or truncated manifest.json,
// which existence-only gating would mistake for a complete corpus and skip
// re-ingest — silently mining a partial events.jsonl. We require the file to
// be non-empty AND parseable JSON before trusting it.
func manifestComplete(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && len(b) > 0 && json.Valid(b)
}

// ensureData runs a default ingest when the artifact is missing or incomplete.
// A bare os.Stat is not sufficient: a 0-byte file (from an interrupted ingest)
// or a file with no companion manifest passes Stat but represents a broken
// corpus. The manifest is written last by every ingest path, so a non-empty,
// valid-JSON manifest is the correct completeness signal.
//
// Refresh semantics: ensureData auto-builds ONLY when the corpus is missing or
// incomplete. It deliberately never auto-REFRESHES a present corpus, because a
// re-ingest is expensive (full transcript walk + parse) — refreshing is an
// explicit 'ferret ingest'. To keep that staleness from being silent (the
// failure mode where day-1 data is mined for weeks, ferret-17q), it warns to
// stderr when transcripts have changed since the corpus was built, then mines
// the existing corpus anyway.
func (c *common) ensureData() error {
	manifestPath := filepath.Join(c.data, "manifest.json")
	if manifestComplete(manifestPath) {
		// manifest present, non-empty, valid JSON → ingest completed
		if stale, builtAt, newest := corpusStale(manifestPath); stale {
			fmt.Fprintf(os.Stderr,
				"ferret: corpus built %s is stale — transcripts changed since (newest %s); run 'ferret ingest' to refresh\n",
				builtAt.Format(time.RFC3339), newest.Format(time.RFC3339))
		}
		return nil
	}
	fmt.Fprintln(os.Stderr, "ferret: no events artifact — running ingest first")
	return ingest(c.data, "", "", "", false)
}

// corpusStale reports whether the corpus described by the manifest at
// manifestPath is older than the newest transcript under its recorded root,
// returning the build time and newest-transcript time for the warning message.
// It is best-effort and advisory: an unreadable/unparseable manifest, a
// manifest that records no root, or an unreadable transcript tree all yield
// (false, …) so the caller stays silent rather than warning spuriously.
func corpusStale(manifestPath string) (stale bool, builtAt, newest time.Time) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return false, time.Time{}, time.Time{}
	}
	var m event.Manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Root == "" {
		return false, time.Time{}, time.Time{}
	}
	newest = newestTranscriptMod(m.Root)
	return newest.After(m.CreatedAt), m.CreatedAt, newest
}

// newestTranscriptMod returns the most recent modification time among the
// transcript files under root (the same set ingest would walk), or the zero
// Time when root is unreadable or holds no transcripts. Unreadable degrades to
// zero rather than erroring because the result feeds an advisory staleness
// warning, not a correctness gate.
func newestTranscriptMod(root string) time.Time {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, s := range srcs {
		if fi, err := os.Stat(s.Path); err == nil && fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
	}
	return newest
}

type lensOpts struct {
	lens        string
	noMarkFail  bool
	noCollapse  bool
	noSidechain bool
}

func fromLensFlags(lf LensFlags) *lensOpts {
	return &lensOpts{
		lens:        lf.Lens,
		noMarkFail:  lf.NoMarkFail,
		noCollapse:  lf.NoCollapse,
		noSidechain: lf.NoSidechain,
	}
}

func (lo *lensOpts) corpus(eventsPath string) (*mine.Corpus, lens.Lens, error) {
	l, err := lens.Get(lo.lens)
	if err != nil {
		return nil, nil, err
	}
	c, err := mine.Build(eventsPath, l, mine.Options{
		MarkFail:    !lo.noMarkFail,
		Collapse:    !lo.noCollapse,
		NoSidechain: lo.noSidechain,
	})
	return c, l, err
}

// ---- about lines ----
// Every text report opens with 1-2 lines saying what the stat measures and
// how to read the notation. JSON output stays clean (schema is the contract).

const legendMarks = "≡ tok! failed · tok? in failed chain · tok+ collapsed repeat run · ex: session@pos"

func about(sink *out.Sink, lines ...string) {
	for _, ln := range lines {
		sink.Head("%s", ln)
	}
}

// applyDefaultLimit resolves --limit for a command that has a sensible row
// default. The flag's three states, per the DK-AXI contract's "compact default
// with an escape hatch" rule:
//
//	unset (0)  → the command's default (compact output, the common case)
//	N > 0      → exactly N rows
//	negative   → unlimited (the escape hatch)
//
// Before this existed, nine commands silently rewrote 0 to their own default,
// which left `--limit 0` — documented as unlimited — truncating instead. There
// is no way to both default to compact AND read bare 0 as unlimited, since
// kong cannot report whether a zero was typed; a distinct sentinel is the only
// honest resolution.
func applyDefaultLimit(c *common, def int) {
	switch {
	case c.limit == 0:
		c.limit = def
	case c.limit < 0:
		c.limit = 0 // out.Sink reads 0 as unlimited
	}
}

// emptyNote prints a definitive empty state — the DK-AXI operating contract's
// "0 results" rule. A table that renders a header and then nothing is
// ambiguous: a model cannot tell a genuinely empty result from a run that
// failed silently, so it retries, which is the exact friction ferret exists to
// find. Says so out loud instead. No-op when there are rows.
func emptyNote(sink *out.Sink, n int, noun string) {
	if n == 0 {
		sink.Head("0 %s", noun)
	}
}

// Report defaults mirrored as constants so 'fixes add' captures a baseline burn
// with the SAME mining params the report later re-measures with — otherwise the
// baseline and the current burn would not be comparable. Kept in sync with the
// kong default tags on CLI.Report by hand (kong tags must be string literals).
const (
	reportMinSupport = 20
	reportMaxGap     = 3
	reportMaxLen     = 5
	reportOrder      = 3
	reportTop        = 10
	// reportSurpriseMinToks skips streams too short for a stable surprise mean
	// when splitting routine vs friction — matches the surprise command default.
	reportSurpriseMinToks = 20
)

// mineFindings runs the shared seqs→rank→cap→project pipeline that both the
// report and the fix-ledger baseline capture depend on, so the burn a fix
// records at add time is measured the same way the report measures it later.
// Cards are capped per bucket (parity with rank --top) before projection.
// Shared by cmdReport (report.go) and cmdFixesAdd (fixes_add.go).
func mineFindings(corpus *mine.Corpus, minSupport, maxGap, maxLen, order, top int, surprise map[string]float64, cut float64) (findings []*mine.Finding, capped bool) {
	pats, capped := mine.MineSeqs(corpus, mine.SeqOpts{
		MinSupport: minSupport, MaxGap: maxGap, MaxLen: maxLen, MaxPatterns: 10000,
	})
	opts := mine.DefaultRankOpts()
	opts.Order = order
	cards, _ := mine.RankPatterns(corpus, pats, opts)

	perBucket := map[string]int{}
	kept := cards[:0:0]
	for _, card := range cards {
		if top > 0 && perBucket[card.Bucket] >= top {
			continue
		}
		perBucket[card.Bucket]++
		kept = append(kept, card)
	}
	return mine.Findings(corpus, kept, maxGap, surprise, cut), capped
}

// resolveData expands the "~/.ferret" sentinel default the same way
// fromCommonFlags does, for the fixes subcommands that don't carry CommonFlags.
func resolveData(data string) (string, error) {
	if data == "~/.ferret" {
		return defaultData()
	}
	return data, nil
}

// ---- helpers shared by 3+ per-command files ----

// exemplar renders a finding/pattern's example occurrence as "session@seq" —
// used by ngrams, seqs, rank, and report to point at one concrete instance.
func exemplar(c *mine.Corpus, stream, seq int) string {
	key := c.StreamKeys[stream]
	if i := strings.IndexByte(key, '/'); i >= 0 {
		key = key[i+1:]
	}
	if len(key) > 8 && !strings.ContainsAny(key[:8], "@") {
		key = key[:8]
	}
	return key + "@" + strconv.Itoa(seq)
}

// pct is the shared percentage helper used by ingest's health line and
// summary's per-bucket/top-action rows.
func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
