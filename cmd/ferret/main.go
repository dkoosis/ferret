// ferret mines Claude Code transcripts for repeated behavior:
// scriptable routines, friction loops, and noisy context.
//
// AX-first: dense default output, --format json everywhere, hard output caps.
package main

import (
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
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/sweagent"
	"github.com/dkoosis/ferret/internal/transcript"
)

var (
	errSessionRequired = errors.New("tokens: --session PREFIX required")
	errNoStreamMatch   = errors.New("tokens: no stream matches")
	errBadRange        = errors.New("bad --n range (gram length must be ≥ 2; 1-gram frequency = summary top actions)")
	errBadFormat       = errors.New("bad --format")
	errBadBy           = errors.New("bad --by (want corpus|project|session)")
	errMaxBytesJSON    = errors.New("--max-bytes is not supported with --format json (use --limit)")
	errBadIngestSrc    = errors.New("bad --source (want cc|swe-agent)")
	errNoOutcomes      = errors.New("validate: no outcomes.jsonl — ingest a labeled corpus (e.g. --source swe-agent) first")
)

// shared JSON response keys — every truncating JSON response carries
// keyTotal + keyTruncated (the AX truncation contract)
const (
	fmtJSON      = "json"
	keyLens      = "lens"
	keyTotal     = "total"
	keyTruncated = "truncated"
)

// ---- CLI grammar ----

// defaultData returns ~/.ferret
func defaultData() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ferret")
}

// defaultRoot returns ~/.claude/projects
func defaultRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
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
		Source  string `help:"Input source: cc (Claude Code transcripts) | swe-agent (trajectory JSONL)." default:"cc" name:"source"`
		DryRun  bool   `help:"Scan and report; write nothing." name:"dry-run"`
	} `cmd:"" help:"Build ~/.ferret/events.jsonl from transcripts." name:"ingest"`

	Validate struct {
		CommonFlags
		LensFlags
		MinSupport int    `help:"Min distinct streams containing the pattern." default:"20" name:"min-support"`
		MaxGap     int    `help:"Max positions between consecutive items (1 = adjacent)." default:"3" name:"max-gap"`
		MaxLen     int    `help:"Max pattern length." default:"5" name:"max-len"`
		Order      int    `help:"Gram-model order for cohesion scoring." default:"3" name:"order"`
		MinStreams int    `help:"Drop buckets supported by fewer streams (avoid tiny-n lift)." default:"3" name:"min-streams"`
		Corpus     string `help:"Corpus label for the report header." default:"swe-agent" name:"corpus"`
	} `cmd:"" help:"Rank buckets × ground-truth outcomes (needs outcomes.jsonl)."`

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
}

func main() {
	// Resolve dynamic defaults before parsing.
	// kong supports ${...} interpolation only for env vars in default tags,
	// so we patch the struct directly before Parse sees it.
	if CLI.Ingest.Root == "" {
		CLI.Ingest.Root = defaultRoot()
	}
	if CLI.Ingest.Data == "" {
		CLI.Ingest.Data = defaultData()
	}

	k := kong.Parse(&CLI,
		kong.Name("ferret"),
		kong.Description(
			"Mine Claude Code transcripts for repeated behavior:\n"+
				"scriptable routines, friction loops, and noisy context.\n\n"+
				"  ferret ingest   [--root DIR] [--source cc|swe-agent] [--project SUBSTR] [--dry-run]\n"+
				"  ferret validate [--lens tool] [--min-support 20] [--min-streams 3]\n"+
				"  ferret summary  [--by corpus|project|session]\n"+
				"  ferret ngrams   [--lens tool] [--n 2-5] [--min-count 5] [--min-sessions 3]\n"+
				"  ferret seqs     [--lens tool] [--min-support 20] [--max-gap 3] [--max-len 5]\n"+
				"  ferret rank     [--lens tool] [--min-support 20] [--order 3] [--top 10]\n"+
				"  ferret surprise [--lens tool] [--order 3] [--min-toks 20]\n"+
				"  ferret graph    [--lens tool] [--min-count 20] [--format text|json|mermaid|dot] [--loops]\n"+
				"  ferret tokens   --session PREFIX [--lens tool]\n\n"+
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
	case "validate":
		err = cmdValidate()
	case "summary":
		err = cmdSummary()
	case "ngrams":
		err = cmdNgrams()
	case "seqs":
		err = cmdSeqs()
	case "rank":
		err = cmdRank()
	case "surprise":
		err = cmdSurprise()
	case "graph":
		err = cmdGraph()
	case "tokens":
		err = cmdTokens()
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

func fromCommonFlags(cf CommonFlags) *common {
	data := cf.Data
	if data == "~/.ferret" {
		data = defaultData()
	}
	return &common{data: data, format: cf.Format, limit: cf.Limit, maxBytes: cf.MaxBytes}
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

// ensureData runs a default ingest when the artifact is missing or incomplete.
// A bare os.Stat is not sufficient: a 0-byte file (from an interrupted ingest)
// or a file with no companion manifest passes Stat but represents a broken
// corpus. The manifest is written last by every ingest path, so its presence
// is the correct completeness signal.
func (c *common) ensureData() error {
	manifestPath := filepath.Join(c.data, "manifest.json")
	if _, err := os.Stat(manifestPath); err == nil {
		// manifest exists → ingest completed successfully
		return nil
	}
	fmt.Fprintln(os.Stderr, "ferret: no events artifact — running ingest first")
	return ingest(c.data, "", "", false)
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
		data = defaultData()
	}
	root := cmd.Root
	if root == "" {
		root = defaultRoot()
	}
	switch cmd.Source {
	case "cc":
		return ingest(data, root, cmd.Project, cmd.DryRun)
	case "swe-agent":
		return ingestSWE(data, root, cmd.DryRun)
	default:
		return fmt.Errorf("%w: %q", errBadIngestSrc, cmd.Source)
	}
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

// outcomeSink mirrors eventSink for the SWE-agent outcomes sidecar.
// Abort discards the in-progress temp file without sealing the artifact; callers
// must invoke it instead of Close on a partial run so the outcomes file is
// never atomically renamed into place when events are incomplete.
type outcomeSink interface {
	Write(o *event.Outcome) error
	Close() error
	Abort()
}

// newEventWriter / newOutcomeWriter are indirected through vars so a test can
// substitute a failing writer without touching the event package.
var (
	newEventWriter   = func(path string) (eventSink, error) { return event.NewWriter(path) }
	newOutcomeWriter = func(path string) (outcomeSink, error) { return event.NewOutcomeWriter(path) }
)

// errWriteAbort wraps the first per-record write error so ingest can abort the
// loop and refuse to seal a manifest over a partially-written artifact.
var errWriteAbort = errors.New("ingest aborted: record write failed")

// ingestSWE adapts SWE-agent trajectory rows (JSONL — one row per stream)
// into the standard events artifact plus an outcomes sidecar. --root may be a
// single .jsonl file or a directory of them.
//
// Persistence contract: emit is fallible. The first write error aborts the
// ingest loop, no manifest is written, and the error propagates so the process
// exits nonzero. Stats reflect persisted records only — counters advance after
// a successful write, never before — so a sealed manifest can never claim more
// events than reached disk.
func ingestSWE(dataDir, root string, dryRun bool) error {
	files, err := jsonlFiles(root)
	if err != nil {
		return err
	}
	var w eventSink
	var ow outcomeSink
	var emitErr error
	emit := func(*event.Event) error { return nil }
	emitOut := func(*event.Outcome) error { return nil }
	if !dryRun {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		if w, err = newEventWriter(filepath.Join(dataDir, "events.jsonl")); err != nil {
			return err
		}
		// ferret-i6a: if NewOutcomeWriter fails, abort the already-open event
		// writer so the fd is released and the .tmp orphan is cleaned up without
		// sealing any partial artifact.
		if ow, err = newOutcomeWriter(filepath.Join(dataDir, "outcomes.jsonl")); err != nil {
			w.Abort()
			return err
		}
		emit = w.Write
		emitOut = ow.Write
	}

	start := time.Now()
	st := event.NewStats()
	rollouts := map[string]int{} // instance_id → rows seen, across all files
	for _, f := range files {
		if emitErr = ingestSWEFile(f, st, rollouts, emit, emitOut); emitErr != nil {
			break
		}
	}
	if w != nil && ow != nil {
		// Close flushes; surface a flush error the same as a per-record error.
		cerr := w.Close()
		// ferret-2yv: if the event writer had a per-record error, or its Close
		// failed, abort the outcome writer so its temp is discarded without
		// renaming — preventing a mismatched events/outcomes pair on disk.
		// If the event path is clean, close ow normally to seal its artifact.
		var oerr error
		if emitErr != nil || cerr != nil {
			ow.Abort()
		} else {
			oerr = ow.Close()
		}
		if err := errors.Join(emitErr, cerr, oerr); err != nil {
			// Partial run: do NOT write a manifest. The events.jsonl temp file
			// is dropped by Writer.Close on its own error; on a per-record
			// error the atomic artifact simply never gets sealed.
			return err
		}
		m := &event.Manifest{CreatedAt: time.Now(), Root: root, Stats: st}
		if err := event.WriteManifest(filepath.Join(dataDir, "manifest.json"), m); err != nil {
			return err
		}
	}
	fmt.Printf("ingest format=swe-agent files=%d rows=%d events=%d in %s\n",
		st.Files, st.Lines, st.Events, time.Since(start).Round(time.Millisecond))
	fmt.Printf("health streams=%d decode-errs=%d\n", st.Prompts, st.DecodeErrs)
	return nil
}

// ingestSWEFile streams one trajectory JSONL file. A malformed row is counted
// and skipped (loudly to stderr), never fatal. Stats reuse: Lines=rows,
// Prompts=streams emitted (health line surfaces both).
//
// A write error is fatal: it returns up so the caller aborts the whole ingest.
// Per-stream stats (Events/Prompts/ByType) advance only after every record of
// that stream is persisted, so the counters never overcount past a failure.
func ingestSWEFile(path string, st *event.Stats, rollouts map[string]int, emit func(*event.Event) error, emitOut func(*event.Outcome) error) error {
	st.Files++
	var writeErr error
	err := transcript.ReadLines(path, func(line []byte) error {
		if len(strings.TrimSpace(string(line))) == 0 {
			return nil
		}
		st.Lines++
		row, err := sweagent.DecodeRow(line)
		if err != nil {
			st.DecodeErrs++
			fmt.Fprintf(os.Stderr, "ferret: %s: row decode: %v (skipped)\n", path, err)
			return nil
		}
		if row.InstanceID == "" {
			st.DecodeErrs++
			fmt.Fprintf(os.Stderr, "ferret: %s: row missing instance_id (skipped)\n", path)
			return nil
		}
		// The dataset carries many rollouts per instance (different models and
		// attempts); each row is its own stream. First occurrence keeps the
		// bare id, repeats get #2, #3, … so events and outcomes never collide.
		rollouts[row.InstanceID]++
		if n := rollouts[row.InstanceID]; n > 1 {
			row.InstanceID = fmt.Sprintf("%s#%d", row.InstanceID, n)
		}
		evs := sweagent.Events(row)
		for _, ev := range evs {
			if err := emit(ev); err != nil {
				writeErr = fmt.Errorf("%w: %s: %w", errWriteAbort, path, err)
				return writeErr // stop ReadLines
			}
			st.ByType[ev.Kind]++
		}
		if err := emitOut(&event.Outcome{
			Stream:     sweagent.Project + "/" + row.InstanceID + "@",
			Target:     row.Target,
			ExitStatus: row.ExitStatus,
		}); err != nil {
			writeErr = fmt.Errorf("%w: %s: %w", errWriteAbort, path, err)
			return writeErr
		}
		st.Events += len(evs)
		st.Prompts++ // reused counter: streams emitted
		return nil
	})
	if writeErr != nil {
		return writeErr
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ferret: %s: %v (skipped)\n", path, err)
	}
	return nil
}

// jsonlFiles returns the .jsonl files at root (a file returns itself).
func jsonlFiles(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{root}, nil
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries skipped, not fatal
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func ingest(dataDir, root, project string, dryRun bool) error {
	if root == "" {
		root = defaultRoot()
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
		cerr := w.Close()
		if err := errors.Join(emitErr, cerr); err != nil {
			// Partial run: refuse to write a manifest. The atomic Writer never
			// seals events.jsonl, so no later mine runs on silently-truncated data.
			return err
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
	c := fromCommonFlags(cmd.CommonFlags)
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
	c := fromCommonFlags(cmd.CommonFlags)
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
	c := fromCommonFlags(cmd.CommonFlags)
	if c.limit == 0 {
		c.limit = 30
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
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
	c := fromCommonFlags(cmd.CommonFlags)
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
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

// ---- validate (rank buckets × ground-truth outcomes) ----

func cmdValidate() error {
	cmd := &CLI.Validate
	c := fromCommonFlags(cmd.CommonFlags)
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	outcomes, err := event.ReadOutcomes(filepath.Join(c.data, "outcomes.jsonl"))
	if err != nil {
		return err
	}
	if len(outcomes) == 0 {
		return errNoOutcomes
	}
	cor, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	pats, _ := mine.MineSeqs(cor, mine.SeqOpts{
		MinSupport: cmd.MinSupport, MaxGap: cmd.MaxGap, MaxLen: cmd.MaxLen, MaxPatterns: 10000,
	})
	opts := mine.DefaultRankOpts()
	opts.Order = cmd.Order
	cards, _ := mine.RankPatterns(cor, pats, opts)
	v := mine.Validate(cor, cards, outcomes, cmd.Corpus, cmd.MinStreams, cmd.MaxGap)

	if c.format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(), "corpus": v.Corpus, "streams": v.Streams,
			"baseFail": v.BaseFail, "buckets": v.Buckets,
			keyTotal: len(v.Buckets), keyTruncated: false,
		})
	}
	sink := out.NewSink(os.Stdout, 0, c.maxBytes)
	defer sink.Close()
	about(sink,
		"≡ validate: do rank buckets predict task failure? fail-share = % of a bucket's sessions",
		"≡ that failed; lift = fail-share ÷ base-fail (1.0 = no signal). Needs a labeled corpus.")
	sink.Head("validate corpus=%s lens=%s streams=%d base-fail=%.1f%%",
		v.Corpus, l.Name(), v.Streams, v.BaseFail)
	for _, b := range v.Buckets {
		if !sink.Row("%-9s fail-share=%5.1f%%  lift=%4.2f   (n=%d patterns, %d streams)",
			strings.ToUpper(b.Bucket), b.FailShare, b.Lift, b.Patterns, b.Streams) {
			break
		}
	}
	return nil
}

// ---- surprise (PPM-lite) ----

func cmdSurprise() error {
	cmd := &CLI.Surprise
	c := fromCommonFlags(cmd.CommonFlags)
	if c.limit == 0 {
		c.limit = 20
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", fmtJSON); err != nil {
		return err
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
	half := c.limit / 2
	if half < 1 {
		half = 10
	}
	lo2hi := scores
	routine := lo2hi
	if len(routine) > half {
		routine = routine[:half]
	}
	thrash := lo2hi
	if len(thrash) > half {
		thrash = thrash[len(thrash)-half:]
	}

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

// ---- graph ----

func cmdGraph() error {
	cmd := &CLI.Graph
	c := fromCommonFlags(cmd.CommonFlags)
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
	c := fromCommonFlags(cmd.CommonFlags)
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
