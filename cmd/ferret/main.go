// ferret mines Claude Code transcripts for repeated behavior:
// scriptable routines, friction loops, and noisy context.
//
// AX-first: dense default output, --format json everywhere, hard output caps.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/fixes"
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
)

var (
	errSessionRequired = errors.New("tokens: --session PREFIX required")
	errNoStreamMatch   = errors.New("tokens: no stream matches")
	errBadRange        = errors.New("bad --n range (gram length must be ≥ 2; 1-gram frequency = summary top actions)")
	errBadFormat       = errors.New("bad --format")
	errBadBy           = errors.New("bad --by (want corpus|project|session)")
	errMaxBytesJSON    = errors.New("--max-bytes is not supported with --format json (use --limit)")
	errMaxBytesMD      = errors.New("--max-bytes is not supported with --format md (whole-document output; use --limit)")
	errMinSupport      = errors.New("--min-support must be ≥ 1 (0 or negative grows the pattern lattice unbounded)")
	errMaxGap          = errors.New("--max-gap must be ≥ 1")
	errMaxLen          = errors.New("--max-len must be ≥ 1")
	errOrder           = errors.New("--order must be ≥ 1")
)

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
	keyTotal     = "total"
	keyTruncated = "truncated"
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

// CommonFlags are shared across all analysis subcommands.
type CommonFlags struct {
	Data     string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
	Format   string `help:"Output format: text|json (graph: +mermaid|dot)." default:"text" name:"format"`
	Limit    int    `help:"Max rows (0 = unlimited)." default:"0" name:"limit"`
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
		Data    string `help:"Artifact directory." default:"~/.ferret" env:"FERRET_DATA" name:"data"`
		Root    string `help:"Transcript root (dir or .jsonl file)." name:"root"`
		Project string `help:"Only projects whose slug contains this substring." name:"project"`
		DryRun  bool   `help:"Scan and report; write nothing." name:"dry-run"`
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

	Candidates struct {
		Session     string `help:"Session ID prefix. Omit for corpus-recurrence mode (rank task-shapes across all sessions)." name:"session"`
		Root        string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Format      string `help:"Output format: text|json." default:"text" name:"format"`
		Top         int    `help:"Max candidate tasks/shapes (0 = all)." default:"10" name:"top"`
		MinSessions int    `help:"Corpus mode: min distinct sessions a shape must recur in." default:"2" name:"min-sessions"`
	} `cmd:"" help:"Rank a session's tasks (--session), or recurring task-shapes across the whole corpus (no --session), as cost-leak candidates for the analyst proposal loop."`

	Conformance struct {
		Spec   string `help:"JSON spec file (reference plan + observed labeled calls); '-' or empty = stdin." name:"spec"`
		Format string `help:"Output format: text|json." default:"text" name:"format"`
	} `cmd:"" help:"Score a task's calls against a reference plan: fitness/precision + alignment localizes the deviating call."`

	Gates struct {
		CommonFlags
	} `cmd:"" help:"Mine review gates (code-review/plan-review/precommit/QA): per-gate rejection sets + overlap ratio ω (high ω = redundant gate) + confirmed friction loops."`

	Adjudicate struct {
		Session    string        `help:"Session ID prefix (required)." required:"" name:"session"`
		Root       string        `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
		Model      string        `help:"Claude model ID (default: claude-sonnet-4-6; use claude-opus-4-8 for calibration)." name:"model"`
		Format     string        `help:"Output format: text|json." default:"text" name:"format"`
		EmitPrompt bool          `help:"Assemble + print the prompt without calling the model (no API key needed)." name:"emit-prompt"`
		Propose    bool          `help:"Propose mode: feed the cost-leak candidates + spine and return one fix per task (automate/de-context) instead of mismatch verdicts." name:"propose"`
		Top        int           `help:"Propose mode: max candidate tasks fed to the analyst (0 = all)." default:"10" name:"top"`
		Timeout    time.Duration `help:"Operator deadline for the analyst call across all retries (0 = SDK defaults)." default:"5m" name:"timeout"`
	} `cmd:"" help:"LLM analyst: flag tool-for-intent mismatches in a session, or --propose cost-cutting fixes over the candidates (precision layer; dk validates)."`

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
	} `cmd:"" help:"Fix ledger: record motif→fix, then 'report --since-fixes' computes burn-delta."`
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
				"  ferret ingest   [--root DIR] [--project SUBSTR] [--dry-run]\n"+
				"  ferret summary  [--by corpus|project|session]\n"+
				"  ferret ngrams   [--lens tool] [--n 2-5] [--min-count 5] [--min-sessions 3]\n"+
				"  ferret seqs     [--lens tool] [--min-support 20] [--max-gap 3] [--max-len 5]\n"+
				"  ferret rank     [--lens tool] [--min-support 20] [--order 3] [--top 10]\n"+
				"  ferret report   [--lens tool] [--kind routine|friction|loop|noise] [--since-fixes] [--format json|md]\n"+
				"  ferret surprise [--lens tool] [--order 3] [--min-toks 20]\n"+
				"  ferret graph    [--lens tool] [--min-count 20] [--format text|json|mermaid|dot] [--loops]\n"+
				"  ferret tokens   --session PREFIX [--lens tool]\n"+
				"  ferret spine    --session PREFIX [--root DIR]\n"+
				"  ferret segments --session PREFIX [--root DIR] [--format text|json]\n"+
				"  ferret dialogue --session PREFIX [--root DIR] [--format text|json]\n"+
				"  ferret candidates [--session PREFIX | (corpus) --min-sessions 2] [--root DIR] [--top 10] [--format text|json]\n"+
				"  ferret conformance [--spec FILE] [--format text|json]   (reads stdin if no --spec)\n"+
				"  ferret gates    [--data DIR] [--format text|json]   (overlap ratio ω over review-gate rejections)\n"+
				"  ferret adjudicate  --session PREFIX [--model ID] [--emit-prompt] [--propose] [--top 10] [--format text|json]\n"+
				"  ferret fixes add  --motif \"Edit!,Read\" --fix \"hookify read-before-edit\" [--note ...]\n"+
				"  ferret fixes list [--format json]\n\n"+
				"common: --data DIR (default ~/.ferret)  --format text|json  --limit N  --max-bytes N\n"+
				"lenses: coarse | tool | target | exact",
		),
		kong.UsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
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
	case "candidates":
		err = cmdCandidates()
	case "conformance":
		err = cmdConformance()
	case "gates":
		err = cmdGates()
	case "adjudicate":
		err = cmdAdjudicate()
	case "fixes add":
		err = cmdFixesAdd()
	case "fixes list":
		err = cmdFixesList()
	default:
		k.Fatalf("unknown command %q", k.Command())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferret:", err)
		os.Exit(1)
	}
}

// ---- shared helpers ----

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
	return ingest(c.data, "", "", false)
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

// ---- ingest ----

func cmdIngest() error {
	cmd := &CLI.Ingest
	data := cmd.Data
	if data == "~/.ferret" {
		d, err := defaultData()
		if err != nil {
			return err
		}
		data = d
	}
	root := cmd.Root
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	return ingest(data, root, cmd.Project, cmd.DryRun)
}

// eventSink is the persistence seam for ingest: the real implementation is
// *event.Writer, but tests inject a writer that fails after K records to prove
// a mid-ingest write error aborts the run and suppresses the manifest.
// Abort discards the in-progress temp file without sealing the artifact.
type eventSink interface {
	Write(ev *event.Event) error
	Close() error
	Abort()
}

// newEventWriter is indirected through a var so a test can substitute a failing
// writer without touching the event package.
var newEventWriter = func(path string) (eventSink, error) { return event.NewWriter(path) }

// errWriteAbort wraps the first per-record write error so ingest can abort the
// loop and refuse to seal a manifest over a partially-written artifact.
var errWriteAbort = errors.New("ingest aborted: record write failed")

func ingest(dataDir, root, project string, dryRun bool) error {
	if root == "" {
		r, err := defaultRoot()
		if err != nil {
			return err
		}
		root = r
	}
	sources, err := transcript.Walk(root)
	if err != nil {
		return err
	}
	if project != "" {
		var keep []transcript.Source
		for _, s := range sources {
			if strings.Contains(s.Project, project) {
				keep = append(keep, s)
			}
		}
		sources = keep
	}

	b := event.NewBuilder()
	// Builder.File takes a non-fallible emit; capture the first write error in a
	// closure-scoped var instead. Once set, the outer loop stops and the run is
	// treated as partial — no manifest gets sealed over a truncated artifact.
	var emitErr error
	emit := func(*event.Event) {}
	var w eventSink
	if !dryRun {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		// Serialize concurrent ingests on the same data dir (ferret-0vz): without
		// this, two ferret processes both triggered by ensureData would write the
		// artifact at once.
		release, lerr := lockData(dataDir)
		if lerr != nil {
			return lerr
		}
		defer release()
		w, err = newEventWriter(filepath.Join(dataDir, "events.jsonl"))
		if err != nil {
			return err
		}
		emit = func(ev *event.Event) {
			if emitErr != nil {
				return // already failed; drain remaining emits cheaply
			}
			if werr := w.Write(ev); werr != nil {
				emitErr = fmt.Errorf("%w: %w", errWriteAbort, werr)
			}
		}
	}
	start := time.Now()
	for _, src := range sources {
		if err := b.File(src, emit); err != nil {
			fmt.Fprintf(os.Stderr, "ferret: %s: %v (skipped)\n", src.Path, err)
		}
		if emitErr != nil {
			break
		}
	}
	if w != nil {
		if emitErr != nil {
			// Mid-write failure: ABORT (close-without-rename) rather than Close.
			// Close would flush the bytes written so far, fsync, and rename the
			// TRUNCATED tmp onto events.jsonl — publishing a partial corpus whose
			// only safety net is the suppressed manifest (ferret-0m7). Abort drops
			// the tmp so no partial artifact ever lands.
			w.Abort()
			return emitErr
		}
		if cerr := w.Close(); cerr != nil {
			// Close failed: the atomic Writer never sealed events.jsonl, so no
			// later mine runs on silently-truncated data.
			return cerr
		}
		m := &event.Manifest{CreatedAt: time.Now(), Root: root, Stats: b.Stats}
		if err := event.WriteManifest(filepath.Join(dataDir, "manifest.json"), m); err != nil {
			return err
		}
	}

	st := b.Stats
	fmt.Printf("ingest files=%d lines=%d events=%d prompts=%d in %s\n",
		st.Files, st.Lines, st.Events, st.Prompts, time.Since(start).Round(time.Millisecond))
	fmt.Printf("health unpaired=%.1f%% shell-fallback=%d deduped=%d decode-errs=%d\n",
		pct(st.Unpaired, st.Events), st.Fallback, st.Deduped, st.DecodeErrs)
	types := make([]string, 0, len(st.ByType))
	for t := range st.ByType {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return st.ByType[types[i]] > st.ByType[types[j]] })
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%s:%d", t, st.ByType[t]))
	}
	fmt.Println("types", strings.Join(parts, " "))
	return nil
}

// ---- summary ----

func cmdSummary() error {
	cmd := &CLI.Summary
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 20
	}
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	switch cmd.By {
	case "corpus", "project", "session":
	default:
		return fmt.Errorf("%w: %q", errBadBy, cmd.By)
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	s, err := mine.Summarize(c.eventsPath(), cmd.By)
	if err != nil {
		return err
	}
	if c.format == fmtJSON {
		total := len(s.Buckets)
		capBuckets := s.Buckets
		if c.limit > 0 && len(capBuckets) > c.limit {
			capBuckets = capBuckets[:c.limit]
		}
		return out.JSON(os.Stdout, map[string]any{
			"by": s.By, "buckets": capBuckets,
			keyTotal: total, keyTruncated: len(capBuckets) < total,
			"topActions": s.TopActions,
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ summary: corpus health — event volume, failure and retry rates per "+cmd.By+".",
		"≡ fail = action errored · cfail = inside a failed compound · unpaired = call without result.")
	sink.Head("summary by=%s buckets=%d", s.By, len(s.Buckets))
	for _, b := range s.Buckets {
		sink.Row("%8d ev %5d sess fail=%.1f%% cfail=%.1f%% retry=%.1f%% unpaired=%.1f%%  %s",
			b.Events, b.Sessions, pct(b.Fails, b.Events), pct(b.CFails, b.Events), pct(b.Retries, b.Events), pct(b.Unpaired, b.Events), b.Key)
	}
	if cmd.By == "corpus" && len(s.TopActions) > 0 {
		sink.Head("top actions:")
		for i, a := range s.TopActions {
			if i >= 15 {
				break
			}
			sink.Row("%8dx fail=%.1f%%  %s", a.Count, pct(a.Fails, a.Count), a.Action)
		}
	}
	return nil
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

// ---- ngrams ----

func cmdNgrams() error {
	cmd := &CLI.Ngrams
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 30
	}
	lo := fromLensFlags(cmd.LensFlags)
	minN, maxN, err := parseRange(cmd.N)
	if err != nil {
		return err
	}
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	grams := mine.Filter(mine.CountGrams(corpus, minN, maxN), cmd.MinCount, cmd.MinSessions)

	if c.format == fmtJSON {
		type jg struct {
			Tokens   []string `json:"tokens"`
			Count    int      `json:"count"`
			Sessions int      `json:"sessions"`
			Exemplar string   `json:"exemplar"`
		}
		rows := make([]jg, 0, len(grams))
		for i, g := range grams {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, jg{corpus.Tokens(g.IDs), g.Count, g.Sessions, exemplar(corpus, g.ExStream, g.ExSeq)})
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "n": cmd.N, "grams": rows,
			keyTotal: len(grams), keyTruncated: len(rows) < len(grams),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ ngrams: exact action sequences repeated verbatim (no gaps). High count across many",
		"≡ sessions = a habitual routine — script/skill candidate. Nx/Ms = N occurrences in M sessions.",
		legendMarks)
	sink.Head("ngrams lens=%s n=%s streams=%d grams=%d (min-count=%d min-sessions=%d)",
		l.Name(), cmd.N, len(corpus.Streams), len(grams), cmd.MinCount, cmd.MinSessions)
	for _, g := range grams {
		if !sink.Row("%5dx/%-4ds %s  ex: %s",
			g.Count, g.Sessions, strings.Join(corpus.Tokens(g.IDs), " → "), exemplar(corpus, g.ExStream, g.ExSeq)) {
			break
		}
	}
	return nil
}

// ---- seqs (PrefixSpan) ----

func cmdSeqs() error {
	cmd := &CLI.Seqs
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 30
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if err := validateSeqParams(cmd.MinSupport, cmd.MaxGap, cmd.MaxLen); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	pats, capped := mine.MineSeqs(corpus, mine.SeqOpts{
		MinSupport: cmd.MinSupport, MaxGap: cmd.MaxGap, MaxLen: cmd.MaxLen, MaxPatterns: 10000,
	})

	if c.format == fmtJSON {
		type jp struct {
			Tokens   []string `json:"tokens"`
			Support  int      `json:"support"`
			Exemplar string   `json:"exemplar"`
		}
		rows := make([]jp, 0, len(pats))
		for i, p := range pats {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, jp{corpus.Tokens(p.IDs), p.Support, exemplar(corpus, p.ExStream, p.ExSeq)})
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "maxGap": cmd.MaxGap, "patterns": rows,
			keyTotal: len(pats), keyTruncated: len(rows) < len(pats) || capped,
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ seqs: ordered subsequences that recur with up to max-gap other actions between steps",
		"≡ (PrefixSpan) — habits that survive interruptions. Ns = pattern appears in N sessions. ⇝ = gap allowed.",
		legendMarks)
	sink.Head("seqs lens=%s streams=%d patterns=%d (min-support=%d max-gap=%d max-len=%d)",
		l.Name(), len(corpus.Streams), len(pats), cmd.MinSupport, cmd.MaxGap, cmd.MaxLen)
	if capped {
		sink.Head("‡ search hit the 10000-pattern cap — raise --min-support")
	}
	for _, p := range pats {
		if !sink.Row("%5ds %s  ex: %s",
			p.Support, strings.Join(corpus.Tokens(p.IDs), " ⇝ "), exemplar(corpus, p.ExStream, p.ExSeq)) {
			break
		}
	}
	return nil
}

// ---- rank (cohesion-scored review queue) ----

func cmdRank() error {
	cmd := &CLI.Rank
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if err := validateSeqParams(cmd.MinSupport, cmd.MaxGap, cmd.MaxLen); err != nil {
		return err
	}
	if cmd.Order < 1 {
		return errOrder
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	pats, capped := mine.MineSeqs(corpus, mine.SeqOpts{
		MinSupport: cmd.MinSupport, MaxGap: cmd.MaxGap, MaxLen: cmd.MaxLen, MaxPatterns: 10000,
	})
	opts := mine.DefaultRankOpts()
	opts.Order = cmd.Order
	cards, noise := mine.RankPatterns(corpus, pats, opts)

	byBucket := map[string][]*mine.Card{}
	overflow := 0
	for _, card := range cards {
		if cmd.Top > 0 && len(byBucket[card.Bucket]) >= cmd.Top {
			overflow++
			continue
		}
		byBucket[card.Bucket] = append(byBucket[card.Bucket], card)
	}

	if c.format == fmtJSON {
		type jc struct {
			Tokens   []string `json:"tokens"`
			Support  int      `json:"support"`
			Bits     float64  `json:"bits"`
			Score    float64  `json:"score"`
			Folded   int      `json:"folded"`
			Variants int      `json:"variants"`
			Exemplar string   `json:"exemplar"`
		}
		buckets := map[string][]jc{}
		for _, b := range mine.Buckets {
			rows := make([]jc, 0, len(byBucket[b]))
			for _, card := range byBucket[b] {
				rows = append(rows, jc{corpus.Tokens(card.IDs), card.Support, card.Bits,
					card.Score, card.Folded, card.Variants, exemplar(corpus, card.ExStream, card.ExSeq)})
			}
			buckets[b] = rows
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "order": cmd.Order, "buckets": buckets,
			"noise": noise, keyTotal: len(cards),
			keyTruncated: overflow > 0 || capped,
		})
	}
	sink := out.NewSink(os.Stdout, 0, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ rank: mined seqs deduped + scored into review buckets. Columns: sessions · bits",
		"≡ (predictability of the chain — lower = tighter habit) · score (review priority).",
		legendMarks)
	sink.Head("rank lens=%s patterns=%d → cards=%d noise=%d (min-support=%d order=%d top=%d)",
		l.Name(), len(pats), len(cards), noise, cmd.MinSupport, cmd.Order, cmd.Top)
	if capped {
		sink.Head("‡ seqs hit the 10000-pattern cap — raise --min-support")
	}
	desc := map[string]string{
		mine.BucketFriction: "fail-marked",
		mine.BucketLoop:     "revisits a step",
		mine.BucketScript:   "low-entropy chains — automation candidates",
		mine.BucketWatch:    "frequent, not yet classifiable",
	}
	for _, b := range mine.Buckets {
		if len(byBucket[b]) == 0 {
			continue
		}
		sink.Head("%s (%s):", strings.ToUpper(b), desc[b])
		for _, card := range byBucket[b] {
			fold := ""
			if card.Variants > 0 {
				fold = fmt.Sprintf(" (+%d variants)", card.Variants)
			} else if card.Folded > 0 {
				fold = fmt.Sprintf(" (+%d folded)", card.Folded)
			}
			if !sink.Row("%5ds %4.1fb %6.1f  %s%s  ex: %s",
				card.Support, card.Bits, card.Score,
				strings.Join(corpus.Tokens(card.IDs), " ⇝ "), fold,
				exemplar(corpus, card.ExStream, card.ExSeq)) {
				break
			}
		}
	}
	if overflow > 0 {
		sink.Head("… %d more cards past --top %d", overflow, cmd.Top)
	}
	return nil
}

// ---- report (Finding projection) ----

var errBadKind = errors.New("bad --kind (want routine|friction|loop|noise)")

func cmdReport() error {
	cmd := &CLI.Report
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 30
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON, fmtMD); err != nil {
		return err
	}
	if c.format == fmtMD && c.maxBytes > 0 {
		return errMaxBytesMD
	}
	switch cmd.Kind {
	case "", string(mine.KindRoutine), string(mine.KindFriction), string(mine.KindLoop), string(mine.KindNoise):
	default:
		return fmt.Errorf("%w: %q", errBadKind, cmd.Kind)
	}
	if err := validateSeqParams(cmd.MinSupport, cmd.MaxGap, cmd.MaxLen); err != nil {
		return err
	}
	if cmd.Order < 1 {
		return errOrder
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	// Surprise (per-session predictability) splits the routine bucket: a recurring
	// motif whose host sessions are outlying-surprising (≥ 1σ above the corpus mean)
	// is friction to fix, not a routine to script. Computed once over the same corpus
	// and routed into Findings' kind assignment — same backoff model the cohesion
	// scorer trains. FrictionCut's σ margin keeps average-surprise routines (which a
	// bare mean would mislabel) on the routine side.
	sscores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: cmd.Order, MinToks: reportSurpriseMinToks})
	surpriseIdx := mine.SurpriseIndex(sscores)
	surpriseCut := mine.FrictionCut(sscores)
	findings, capped := mineFindings(corpus, cmd.MinSupport, cmd.MaxGap, cmd.MaxLen, cmd.Order, cmd.Top, surpriseIdx, surpriseCut)
	if cmd.Kind != "" {
		filtered := findings[:0:0]
		for _, f := range findings {
			if string(f.Kind) == cmd.Kind {
				filtered = append(filtered, f)
			}
		}
		findings = filtered
	} else {
		// Default view drops noise — it's frequent but not actionable.
		drop := findings[:0:0]
		for _, f := range findings {
			if f.Kind != mine.KindNoise {
				drop = append(drop, f)
			}
		}
		findings = drop
	}

	// --since-fixes joins each finding to the fix ledger by motif key, turning
	// the report from a fresh snapshot into a before→after on recorded fixes.
	// A nil index means the flag is off; an empty (non-nil) index means it is on
	// but no fix has been recorded yet.
	var fixIdx map[string]fixes.Entry
	if cmd.SinceFixes {
		entries, err := fixes.Load(fixes.Path(c.data))
		if err != nil {
			return err
		}
		fixIdx = fixes.Index(entries)
	}

	// A wontfix/watch verdict suppresses its motif from the actionable list: the
	// motif was adjudicated and deliberately not fixed, so re-surfacing it as a
	// fresh candidate every scan would re-litigate a closed call. They are pulled
	// out here (and shown separately with their reason) so the loop's memory
	// holds across scans. Only meaningful under --since-fixes (nil index = off).
	var suppressed []*mine.Finding
	if fixIdx != nil {
		keep := findings[:0:0]
		for _, f := range findings {
			if e, ok := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]; ok && e.Suppressed() {
				suppressed = append(suppressed, f)
				continue
			}
			keep = append(keep, f)
		}
		findings = keep
		warnLensDivergence(fixIdx, corpus, findings, suppressed, l.Name())
	}

	if c.format == fmtJSON {
		type jf struct {
			Motif        []string `json:"motif"`
			Kind         string   `json:"kind"`
			Action       string   `json:"action"`
			Count        int      `json:"count"`
			Sessions     int      `json:"sessions"`
			FailRate     float64  `json:"failRate"`
			Burn         int      `json:"burn"`
			Surprise     float64  `json:"surprise"`
			Evidence     string   `json:"evidence"`
			Fixed        bool     `json:"fixed,omitempty"`
			Fix          string   `json:"fix,omitempty"`
			FixedAt      string   `json:"fixedAt,omitempty"`
			BaselineBurn int      `json:"baselineBurn,omitempty"`
		}
		rows := make([]jf, 0, len(findings))
		for i, f := range findings {
			if c.limit > 0 && i >= c.limit {
				break
			}
			row := jf{
				Motif: corpus.Tokens(f.IDs), Kind: string(f.Kind), Action: string(f.Action),
				Count: f.Count, Sessions: f.Sessions, FailRate: f.FailRate,
				Burn: f.Burn, Surprise: f.Surprise, Evidence: exemplar(corpus, f.ExStream, f.ExSeq),
			}
			if e, ok := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]; ok {
				row.Fixed, row.Fix = true, e.Fix
				row.FixedAt, row.BaselineBurn = e.AddedAt.Format("2006-01-02"), e.BaselineBurn
			}
			rows = append(rows, row)
		}
		payload := map[string]any{
			keyLens: l.Name(), "findings": rows,
			keyTotal: len(findings), keyTruncated: len(rows) < len(findings) || capped,
		}
		if sup := suppressedRows(fixIdx, corpus, suppressed); len(sup) > 0 {
			payload["suppressed"] = sup
		}
		return out.JSON(os.Stdout, payload)
	}

	// --format md renders the human cost report: the same findings projected into
	// out.MDFinding (motif + exemplar resolved to strings) and grouped into the
	// 💸/🔁/✅ sections. The renderer owns layout; main only flattens + caps.
	if c.format == fmtMD {
		rows := make([]out.MDFinding, 0, len(findings))
		for i, f := range findings {
			if c.limit > 0 && i >= c.limit {
				break
			}
			rows = append(rows, out.MDFinding{
				Motif: corpus.Tokens(f.IDs), Kind: string(f.Kind), Action: string(f.Action),
				Count: f.Count, Sessions: f.Sessions, FailRate: f.FailRate,
				Burn: f.Burn, Evidence: exemplar(corpus, f.ExStream, f.ExSeq),
			})
		}
		return out.Markdown(os.Stdout, l.Name(), len(findings), rows)
	}

	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ report: motifs classified into an action verb, ranked by burn — measured tokens of",
		"≡ context the motif's occurrences cost across the corpus. burn×nothing else; it's the leak size.",
		"≡ surp = mean bits/tok of the sessions a motif recurs in: a high-surp routine is friction (fix it), low-surp is routine (script it).",
		legendMarks)
	if fixIdx != nil {
		sink.Head("≡ since-fixes: [fixed DATE burn BASE→NOW ↓/↑/=] annotates motifs in the ledger (↓ = fix landed).")
	}
	sink.Head("report lens=%s findings=%d (min-support=%d order=%d)",
		l.Name(), len(findings), cmd.MinSupport, cmd.Order)
	if capped {
		sink.Head("‡ seqs hit the 10000-pattern cap — raise --min-support")
	}
	for _, f := range findings {
		row := fmt.Sprintf("%-8s %-8s burn=%-8d n=%-5d sess=%-4d fail=%2.0f%% surp=%4.1f  %s  ex: %s",
			f.Kind, f.Action, f.Burn, f.Count, f.Sessions, f.FailRate*100, f.Surprise,
			strings.Join(corpus.Tokens(f.IDs), " ⇝ "), exemplar(corpus, f.ExStream, f.ExSeq))
		if ann, ok := sinceFixAnnotation(fixIdx, corpus.Tokens(f.IDs), f.Burn); ok {
			row += ann
		}
		if !sink.Row("%s", row) {
			break
		}
	}
	if len(suppressed) > 0 {
		sink.Head("⊘ suppressed=%d (adjudicated wontfix/watch — not actionable, kept for memory)", len(suppressed))
		for _, f := range suppressed {
			e := fixIdx[fixes.MotifKey(corpus.Tokens(f.IDs))]
			if !sink.Row("⊘ %-8s %s  [%s]", e.Disp(),
				strings.Join(corpus.Tokens(f.IDs), " ⇝ "), suppressReason(e)) {
				break
			}
		}
	}
	return nil
}

// suppressReason renders a wontfix/watch entry's recorded justification: the Fix
// field (which holds the reason for a non-fix verdict) plus any Note.
func suppressReason(e fixes.Entry) string {
	if e.Note == "" {
		return e.Fix
	}
	return e.Fix + " — " + e.Note
}

// suppressedRows projects the suppressed findings into JSON rows carrying the
// motif, its verdict, and the recorded reason — the actionable list omits them,
// but the report still reports what was adjudicated and why.
func suppressedRows(idx map[string]fixes.Entry, corpus *mine.Corpus, suppressed []*mine.Finding) []map[string]any {
	if len(suppressed) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(suppressed))
	for _, f := range suppressed {
		toks := corpus.Tokens(f.IDs)
		e := idx[fixes.MotifKey(toks)]
		rows = append(rows, map[string]any{
			"motif":       toks,
			"disposition": e.Disp(),
			"reason":      suppressReason(e),
			"fixedAt":     e.AddedAt.Format("2006-01-02"),
		})
	}
	return rows
}

// mineFindings runs the shared seqs→rank→cap→project pipeline that both the
// report and the fix-ledger baseline capture depend on, so the burn a fix
// records at add time is measured the same way the report measures it later.
// Cards are capped per bucket (parity with rank --top) before projection.
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

// ---- fixes (the loop-closing ledger) ----

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

var errFixMotifRequired = errors.New("fixes add: --motif must not be empty")

// resolveData expands the "~/.ferret" sentinel default the same way
// fromCommonFlags does, for the fixes subcommands that don't carry CommonFlags.
func resolveData(data string) (string, error) {
	if data == "~/.ferret" {
		return defaultData()
	}
	return data, nil
}

// cmdFixesAdd records motif→fix in the ledger, capturing the motif's CURRENT
// burn as the baseline. The baseline is measured through the same findings
// pipeline the report uses, so the later 'report --since-fixes' delta is a true
// before→after rather than an eyeballed guess. A motif that isn't currently a
// finding records a 0 baseline (with a stderr note) — the user is recording a
// fix for friction the corpus no longer shows.
func cmdFixesAdd() error {
	cmd := &CLI.Fixes.Add
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cmd.Motif) == "" {
		return errFixMotifRequired
	}
	// Canonicalize the user's --motif through the SAME key path the report uses
	// (split on comma, trim each token, escape, rejoin) so a spaced or
	// comma-bearing motif stores the exact key the report later computes from the
	// corpus tokens — the join cannot drift between write and read.
	motif := fixes.MotifKey(fixes.ParseMotif(cmd.Motif))

	disp := cmd.Disposition
	e := fixes.Entry{Motif: motif, Fix: cmd.Fix, Note: cmd.Note, AddedAt: time.Now(), Disposition: disp, Lens: cmd.Lens}

	// Only a fix captures a baseline: a wontfix/watch verdict suppresses the
	// motif from the report rather than measuring a delta, so mining its current
	// burn would be wasted work (and a misleading non-zero baseline on a row that
	// never computes a delta).
	if e.Suppressed() {
		if err := fixes.Append(fixes.Path(data), e); err != nil {
			return err
		}
		fmt.Printf("recorded %s: %s — %s (suppressed from report, no baseline)\n", disp, motif, cmd.Fix)
		return nil
	}

	c := &common{data: data, format: "text"}
	if err := c.ensureData(); err != nil {
		return err
	}
	lo := &lensOpts{lens: cmd.Lens}
	corpus, _, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	// nil surprise index: the baseline only needs each motif's burn (surprise-
	// independent), so the routine/friction split is irrelevant here.
	findings, _ := mineFindings(corpus, reportMinSupport, reportMaxGap, reportMaxLen, reportOrder, reportTop, nil, 0)
	for _, f := range findings {
		if fixes.MotifKey(corpus.Tokens(f.IDs)) == motif {
			e.BaselineBurn = f.Burn
			break
		}
	}

	if err := fixes.Append(fixes.Path(data), e); err != nil {
		return err
	}
	if e.BaselineBurn == 0 {
		fmt.Fprintf(os.Stderr,
			"ferret: motif %q is not a current finding (lens=%s) — baseline burn recorded as 0\n", motif, cmd.Lens)
	}
	fmt.Printf("recorded fix: %s → %s (baseline burn %d toks)\n", motif, cmd.Fix, e.BaselineBurn)
	return nil
}

// cmdFixesList prints the recorded fixes, newest concerns last (append order).
func cmdFixesList() error {
	cmd := &CLI.Fixes.List
	data, err := resolveData(cmd.Data)
	if err != nil {
		return err
	}
	if cmd.Format != "text" && cmd.Format != fmtJSON {
		return fmt.Errorf("%w: %q (want text|json)", errBadFormat, cmd.Format)
	}
	entries, err := fixes.Load(fixes.Path(data))
	if err != nil {
		return err
	}
	if cmd.Format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			"fixes": entries, keyTotal: len(entries),
		})
	}
	sink := out.NewSink(os.Stdout, 0, 0)
	defer sink.Close()
	sink.Head("fixes recorded=%d (ledger %s)", len(entries), fixes.Path(data))
	for _, e := range entries {
		note := ""
		if e.Note != "" {
			note = "  — " + e.Note
		}
		sink.Row("%s  %-8s burn@fix=%-8d  %s → %s%s",
			e.AddedAt.Format("2006-01-02"), e.Disp(), e.BaselineBurn, e.Motif, e.Fix, note)
	}
	return nil
}

// warnLensDivergence flags fix-ledger entries that matched no finding this run
// AND were recorded under a different lens than the report is using. Lens
// transforms change the token strings (mark-fail appends '!', collapse merges
// runs), so the join key shifts and the fix silently fails to annotate — the
// exact re-litigation the ledger exists to prevent. A single stderr line points
// the operator at the lens mismatch rather than leaving the miss invisible.
func warnLensDivergence(idx map[string]fixes.Entry, corpus *mine.Corpus, kept, suppressed []*mine.Finding, lensName string) {
	matched := make(map[string]bool, len(kept)+len(suppressed))
	for _, f := range kept {
		matched[fixes.MotifKey(corpus.Tokens(f.IDs))] = true
	}
	for _, f := range suppressed {
		matched[fixes.MotifKey(corpus.Tokens(f.IDs))] = true
	}
	n := 0
	for key, e := range idx {
		if !matched[key] && e.Lens != "" && e.Lens != lensName {
			n++
		}
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr,
			"ferret: %d fix-ledger entr%s recorded under a different lens matched no finding under lens=%s — "+
				"re-run report with the lens you recorded the fix under, or re-record the fix\n",
			n, plural(n, "y", "ies"), lensName)
	}
}

// plural picks the singular or plural suffix for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// sinceFixAnnotation joins one finding's motif to the fix ledger index,
// returning the human-readable annotation suffix and whether a fix matched.
// Pure (no corpus/disk) so the motif-keyed join + burn-delta formatting is
// unit-testable. A nil index (flag off) yields no match.
func sinceFixAnnotation(idx map[string]fixes.Entry, motif []string, currentBurn int) (string, bool) {
	e, ok := idx[fixes.MotifKey(motif)]
	if !ok {
		return "", false
	}
	a := fixes.Annotation{Entry: e, Current: currentBurn}
	return fmt.Sprintf("  [fixed %s burn %s→%s %s]",
		e.AddedAt.Format("2006-01-02"), compactBurn(e.BaselineBurn), compactBurn(currentBurn), a.Arrow()), true
}

// compactBurn renders a token count compactly for inline annotations: 253000 →
// "253k", 990 → "990". Lossy by design — an annotation wants a glance-readable
// magnitude, not an exact figure (the JSON output carries the precise numbers).
func compactBurn(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa(n/1000) + "k"
}

// ---- surprise (PPM-lite) ----

func cmdSurprise() error {
	cmd := &CLI.Surprise
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 20
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if cmd.Order < 1 {
		return errOrder
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	scores := mine.ScoreSurprise(corpus, mine.SurpriseOpts{Order: cmd.Order, MinToks: cmd.MinToks})

	mean := 0.0
	for _, s := range scores {
		mean += s.Bits
	}
	if len(scores) > 0 {
		mean /= float64(len(scores))
	}
	routine, thrash := splitSurprise(scores, c.limit)

	if c.format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "order": cmd.Order, "meanBits": mean,
			"routine": routine, "thrash": thrash,
			keyTotal: len(scores), keyTruncated: len(routine)+len(thrash) < len(scores),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit+2, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ surprise: how predictable each session is to a model trained on all your sessions",
		"≡ (order-N context model). Low bits/tok = rote routine worth scripting; high = novel work or thrash.")
	sink.Head("surprise lens=%s order=%d streams=%d mean=%.2f bits/tok (low=routine/scriptable, high=thrash)",
		l.Name(), cmd.Order, len(scores), mean)
	sink.Head("most routine:")
	for _, s := range routine {
		if !sink.Row("%6.2f bits %5d toks  %s", s.Bits, s.Toks, s.Stream) {
			break
		}
	}
	sink.Head("most surprising:")
	for _, s := range slices.Backward(thrash) {
		if !sink.Row("%6.2f bits %5d toks  %s", s.Bits, s.Toks, s.Stream) {
			break
		}
	}
	return nil
}

// splitSurprise partitions the lo→hi sorted surprise scores into the most
// routine (low bits/tok) and most surprising (high bits/tok) sections,
// capping each at limit/2. The two sections must never overlap: on a small
// corpus the naive "first half" / "last half" slices share their middle, so
// the same streams render under both "most routine" and "most surprising"
// (ferret-045). Both the text and JSON paths consume this, so they stay in
// parity by construction.
func splitSurprise(scores []mine.StreamScore, limit int) (routine, thrash []mine.StreamScore) {
	half := limit / 2
	if half < 1 {
		half = 10
	}
	// Partition at the midpoint so routine ⊆ [0,mid) and thrash ⊆ [mid,n):
	// the two slices can never share an element, even when the corpus is
	// smaller than the limit. Each side is then capped at half.
	mid := len(scores) / 2
	routine = scores[:mid]
	if len(routine) > half {
		routine = routine[:half]
	}
	thrash = scores[mid:]
	if len(thrash) > half {
		thrash = thrash[len(thrash)-half:]
	}
	return routine, thrash
}

// ---- graph ----

func cmdGraph() error {
	cmd := &CLI.Graph
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 40
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", "json", "mermaid", "dot"); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	f := mine.BuildFollows(corpus)

	edges := f.Edges[:0:0]
	for _, e := range f.Edges {
		if e.Count >= cmd.MinCount {
			edges = append(edges, e)
		}
	}
	totalEdges := len(edges)
	if c.limit > 0 && len(edges) > c.limit {
		edges = edges[:c.limit]
	}

	switch c.format {
	case fmtJSON:
		type je struct {
			From  string `json:"from"`
			To    string `json:"to"`
			Count int    `json:"count"`
		}
		type jc struct {
			A, B  string
			Count int
		}
		rows := make([]je, len(edges))
		for i, e := range edges {
			rows[i] = je{corpus.Vocab[e.From], corpus.Vocab[e.To], e.Count}
		}
		var cyc []jc
		for i, cy := range f.Cycles {
			if i >= 20 {
				break
			}
			cyc = append(cyc, jc{corpus.Vocab[cy.A], corpus.Vocab[cy.B], cy.Count})
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(),
			"edges": rows, "edgesTotal": totalEdges, keyTruncated: len(rows) < totalEdges,
			"cycles": cyc, "cyclesTotal": len(f.Cycles),
		})
	case "mermaid", "dot":
		return writeGraph(os.Stdout, c.format, corpus, edges)
	}

	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	about(sink,
		"≡ graph: action→action transition counts (what typically follows what).",
		"≡ --loops adds A⇄B bounce cycles — back-and-forth churn, often friction.")
	sink.Head("graph lens=%s edges=%d (min-count=%d)", l.Name(), len(edges), cmd.MinCount)
	for _, e := range edges {
		if !sink.Row("%6dx  %s → %s", e.Count, corpus.Vocab[e.From], corpus.Vocab[e.To]) {
			break
		}
	}
	if err := sink.Close(); err != nil {
		return err
	}
	if cmd.Loops {
		// cycles get their own budget — they're the friction report, not overflow
		ls := out.NewSink(os.Stdout, 20, c.maxBytes)
		ls.Head("bounce cycles (A→B→A):")
		for _, cy := range f.Cycles {
			if !ls.Row("%6dx  %s ⇄ %s", cy.Count, corpus.Vocab[cy.A], corpus.Vocab[cy.B]) {
				break
			}
		}
		return ls.Close()
	}
	return nil
}

func writeGraph(w *os.File, format string, c *mine.Corpus, edges []mine.Edge) error {
	nodeID := map[uint32]string{}
	id := func(t uint32) string {
		if n, ok := nodeID[t]; ok {
			return n
		}
		n := fmt.Sprintf("n%d", len(nodeID))
		nodeID[t] = n
		return n
	}
	if format == "mermaid" {
		fmt.Fprintln(w, "flowchart LR")
		for _, e := range edges {
			fmt.Fprintf(w, "  %s[\"%s\"] -->|%d| %s[\"%s\"]\n",
				id(e.From), mermaidLabel(c.Vocab[e.From]), e.Count, id(e.To), mermaidLabel(c.Vocab[e.To]))
		}
		return nil
	}
	fmt.Fprintln(w, "digraph ferret {")
	fmt.Fprintln(w, "  rankdir=LR;")
	for _, e := range edges {
		fmt.Fprintf(w, "  %q -> %q [label=%d];\n", c.Vocab[e.From], c.Vocab[e.To], e.Count)
	}
	fmt.Fprintln(w, "}")
	return nil
}

// mermaidLabel escapes characters that break a quoted mermaid node label.
// Exact-lens tokens carry raw targets (paths, patterns) that can contain any of them.
func mermaidLabel(s string) string {
	r := strings.NewReplacer(`"`, "#quot;", "[", "#91;", "]", "#93;", "{", "#123;", "}", "#125;")
	return r.Replace(s)
}

// ---- tokens ----

func cmdTokens() error {
	cmd := &CLI.Tokens
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 200
	}
	lo := fromLensFlags(cmd.LensFlags)
	if cmd.Session == "" {
		return errSessionRequired
	}
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	var matches []int
	for si, key := range corpus.StreamKeys {
		short := key[strings.IndexByte(key, '/')+1:]
		if strings.HasPrefix(short, cmd.Session) || strings.Contains(key, cmd.Session) {
			matches = append(matches, si)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("%w: %q", errNoStreamMatch, cmd.Session)
	}
	if c.format == fmtJSON {
		type jt struct {
			Seq   int    `json:"seq"`
			Token string `json:"token"`
		}
		type js struct {
			Stream    string `json:"stream"`
			Total     int    `json:"total"`
			Truncated bool   `json:"truncated"`
			Tokens    []jt   `json:"tokens"`
		}
		streams := make([]js, 0, len(matches))
		for _, si := range matches {
			toks := corpus.Streams[si]
			total := len(toks)
			if c.limit > 0 && len(toks) > c.limit {
				toks = toks[:c.limit]
			}
			s := js{Stream: corpus.StreamKeys[si], Total: total, Truncated: len(toks) < total, Tokens: make([]jt, len(toks))}
			for i, t := range toks {
				s.Tokens[i] = jt{t.Seq, corpus.Vocab[t.ID]}
			}
			streams = append(streams, s)
		}
		return out.JSON(os.Stdout, map[string]any{keyLens: l.Name(), "streams": streams})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	for _, si := range matches {
		sink.Head("stream %s lens=%s toks=%d", corpus.StreamKeys[si], l.Name(), len(corpus.Streams[si]))
		for _, t := range corpus.Streams[si] {
			if !sink.Row("%6d  %s", t.Seq, corpus.Vocab[t.ID]) {
				break
			}
		}
	}
	return nil
}

// ---- helpers ----

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

func parseRange(s string) (int, int, error) {
	if a, b, ok := strings.Cut(s, "-"); ok {
		lo, err1 := strconv.Atoi(a)
		hi, err2 := strconv.Atoi(b)
		if err1 != nil || err2 != nil || lo < 2 || hi < lo {
			return 0, 0, fmt.Errorf("%w: %q", errBadRange, s)
		}
		return lo, hi, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 2 {
		return 0, 0, fmt.Errorf("%w: %q", errBadRange, s)
	}
	return n, n, nil
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
