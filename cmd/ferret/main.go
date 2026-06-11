// ferret mines Claude Code transcripts for repeated behavior:
// scriptable routines, friction loops, and noisy context.
//
// AX-first: dense default output, -format json everywhere, hard output caps.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/transcript"
)

const usage = `ferret — mine Claude Code transcripts for repeated behavior

  ferret ingest   [-root DIR] [-project SUBSTR] [-dry-run]     build ~/.ferret/events.jsonl
  ferret summary  [-by corpus|project|session]                 corpus health + tool mix
  ferret ngrams   [-lens tool] [-n 2-5] [-min-count 5] [-min-sessions 3]
  ferret graph    [-lens tool] [-min-count 20] [-format text|json|mermaid|dot] [-loops]
  ferret tokens   -session PREFIX [-lens tool]                 one session's token stream (lens debugger)

common: -data DIR (default ~/.ferret) · -format text|json · -limit N · -max-bytes N
lenses: ` + "coarse | tool | target | exact"

var (
	errSessionRequired = errors.New("tokens: -session PREFIX required")
	errNoStreamMatch   = errors.New("tokens: no stream matches")
	errBadRange        = errors.New("bad -n range (gram length must be ≥ 2; 1-gram frequency = summary top actions)")
	errBadFormat       = errors.New("bad -format")
	errBadBy           = errors.New("bad -by (want corpus|project|session)")
	errMaxBytesJSON    = errors.New("-max-bytes is not supported with -format json (use -limit)")
)

const fmtJSON = "json"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = cmdIngest(os.Args[2:])
	case "summary":
		err = cmdSummary(os.Args[2:])
	case "ngrams":
		err = cmdNgrams(os.Args[2:])
	case "graph":
		err = cmdGraph(os.Args[2:])
	case "tokens":
		err = cmdTokens(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ferret:", err)
		os.Exit(1)
	}
}

// ---- shared flags ----

type common struct {
	data     string
	format   string
	limit    int
	maxBytes int
}

func commonFlags(fs *flag.FlagSet, defLimit int) *common {
	c := &common{}
	home, _ := os.UserHomeDir()
	fs.StringVar(&c.data, "data", filepath.Join(home, ".ferret"), "artifact directory")
	fs.StringVar(&c.format, "format", "text", "output format: text|json (graph: +mermaid|dot)")
	fs.IntVar(&c.limit, "limit", defLimit, "max rows (0 = unlimited)")
	fs.IntVar(&c.maxBytes, "max-bytes", 0, "max output bytes (0 = unlimited)")
	return c
}

func (c *common) eventsPath() string { return filepath.Join(c.data, "events.jsonl") }

// validate rejects unknown -format values (a typo must not silently produce
// text output) and -max-bytes with json (no streaming cap — refuse rather
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

// ensureData runs a default ingest when the artifact is missing.
func (c *common) ensureData() error {
	if _, err := os.Stat(c.eventsPath()); err == nil {
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

func lensFlags(fs *flag.FlagSet) *lensOpts {
	lo := &lensOpts{}
	fs.StringVar(&lo.lens, "lens", "tool", "token lens: "+strings.Join(lens.Names(), "|"))
	fs.BoolVar(&lo.noMarkFail, "no-mark-fail", false, "don't append ! to failed-action tokens")
	fs.BoolVar(&lo.noCollapse, "no-collapse", false, "don't run-length collapse repeated tokens")
	fs.BoolVar(&lo.noSidechain, "no-sidechain", false, "exclude sidechain events")
	return lo
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

func cmdIngest(args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	c := commonFlags(fs, 0)
	home, _ := os.UserHomeDir()
	root := fs.String("root", filepath.Join(home, ".claude", "projects"), "transcript root")
	project := fs.String("project", "", "only projects whose slug contains this substring")
	dryRun := fs.Bool("dry-run", false, "scan and report; write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := c.validate("text"); err != nil {
		return err
	}
	return ingest(c.data, *root, *project, *dryRun)
}

func ingest(dataDir, root, project string, dryRun bool) error {
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".claude", "projects")
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
	emit := func(*event.Event) {}
	var w *event.Writer
	if !dryRun {
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return err
		}
		w, err = event.NewWriter(filepath.Join(dataDir, "events.jsonl"))
		if err != nil {
			return err
		}
		emit = func(ev *event.Event) { _ = w.Write(ev) }
	}
	start := time.Now()
	for _, src := range sources {
		if err := b.File(src, emit); err != nil {
			fmt.Fprintf(os.Stderr, "ferret: %s: %v (skipped)\n", src.Path, err)
		}
	}
	if w != nil {
		if err := w.Close(); err != nil {
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

func cmdSummary(args []string) error {
	fs := flag.NewFlagSet("summary", flag.ExitOnError)
	c := commonFlags(fs, 20)
	by := fs.String("by", "corpus", "aggregation grain: corpus|project|session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := c.validate("text", "json"); err != nil {
		return err
	}
	switch *by {
	case "corpus", "project", "session":
	default:
		return fmt.Errorf("%w: %q", errBadBy, *by)
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	s, err := mine.Summarize(c.eventsPath(), *by)
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
			"total": total, "truncated": len(capBuckets) < total,
			"topActions": s.TopActions,
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	sink.Head("summary by=%s buckets=%d", s.By, len(s.Buckets))
	for _, b := range s.Buckets {
		sink.Row("%8d ev %5d sess fail=%.1f%% cfail=%.1f%% retry=%.1f%% unpaired=%.1f%%  %s",
			b.Events, b.Sessions, pct(b.Fails, b.Events), pct(b.CFails, b.Events), pct(b.Retries, b.Events), pct(b.Unpaired, b.Events), b.Key)
	}
	if *by == "corpus" && len(s.TopActions) > 0 {
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

// ---- ngrams ----

func cmdNgrams(args []string) error {
	fs := flag.NewFlagSet("ngrams", flag.ExitOnError)
	c := commonFlags(fs, 30)
	lo := lensFlags(fs)
	nRange := fs.String("n", "2-5", "gram lengths ≥2, e.g. 3 or 2-5")
	minCount := fs.Int("min-count", 5, "min total occurrences")
	minSessions := fs.Int("min-sessions", 3, "min distinct streams")
	if err := fs.Parse(args); err != nil {
		return err
	}
	minN, maxN, err := parseRange(*nRange)
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
	grams := mine.Filter(mine.CountGrams(corpus, minN, maxN), *minCount, *minSessions)

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
			"lens": l.Name(), "n": *nRange, "grams": rows,
			"total": len(grams), "truncated": len(rows) < len(grams),
		})
	}
	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	defer sink.Close()
	sink.Head("ngrams lens=%s n=%s streams=%d grams=%d (min-count=%d min-sessions=%d)",
		l.Name(), *nRange, len(corpus.Streams), len(grams), *minCount, *minSessions)
	for _, g := range grams {
		if !sink.Row("%5dx/%-4ds %s  ex: %s",
			g.Count, g.Sessions, strings.Join(corpus.Tokens(g.IDs), " → "), exemplar(corpus, g.ExStream, g.ExSeq)) {
			break
		}
	}
	return nil
}

// ---- graph ----

func cmdGraph(args []string) error {
	fs := flag.NewFlagSet("graph", flag.ExitOnError)
	c := commonFlags(fs, 40)
	lo := lensFlags(fs)
	minCount := fs.Int("min-count", 20, "min edge count")
	loops := fs.Bool("loops", false, "show A→B→A bounce cycles (friction signatures)")
	if err := fs.Parse(args); err != nil {
		return err
	}
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
		if e.Count >= *minCount {
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
			"lens":  l.Name(),
			"edges": rows, "edgesTotal": totalEdges, "truncated": len(rows) < totalEdges,
			"cycles": cyc, "cyclesTotal": len(f.Cycles),
		})
	case "mermaid", "dot":
		return writeGraph(os.Stdout, c.format, corpus, edges)
	}

	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	sink.Head("graph lens=%s edges=%d (min-count=%d)", l.Name(), len(edges), *minCount)
	for _, e := range edges {
		if !sink.Row("%6dx  %s → %s", e.Count, corpus.Vocab[e.From], corpus.Vocab[e.To]) {
			break
		}
	}
	if err := sink.Close(); err != nil {
		return err
	}
	if *loops {
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

func cmdTokens(args []string) error {
	fs := flag.NewFlagSet("tokens", flag.ExitOnError)
	c := commonFlags(fs, 200)
	lo := lensFlags(fs)
	session := fs.String("session", "", "session id prefix (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *session == "" {
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
		if strings.HasPrefix(short, *session) || strings.Contains(key, *session) {
			matches = append(matches, si)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("%w: %q", errNoStreamMatch, *session)
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
		return out.JSON(os.Stdout, map[string]any{"lens": l.Name(), "streams": streams})
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
