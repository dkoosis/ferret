package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	candrec "github.com/dkoosis/ferret/internal/candidate"
	"github.com/dkoosis/ferret/internal/durable"
	"github.com/dkoosis/ferret/internal/friction"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
	"github.com/dkoosis/ferret/internal/spool"
	"github.com/dkoosis/ferret/internal/transcript"
)

// emitNow is the wall-clock seam: emitted_at is stamped through it so a test can
// pin a deterministic time (mirrors the userHomeDir indirection in main.go).
var emitNow = time.Now

// cmdEmit is `ferret emit`: the deterministic, LLM-free sensor pass that turns
// the ingested corpus into candidate spool rows. For each transcript stream it
// windows the token sequence, scores each window's per-span surprisal, hashes its
// Drain fingerprint, counts friction recurrence, threads the transcript path, and
// appends a candidate row to ~/.ferret/spool/candidates-YYYY-MM.jsonl. Output is
// inspectable JSONL a human (and later the trixi-bot distiller) consumes; ferret
// emits WHERE a fact might live, never WHAT it is — no claim text, ever.
func cmdEmit() error {
	data, err := resolveData(CLI.Emit.Data)
	if err != nil {
		return err
	}
	root, err := resolveRoot(CLI.Emit.Root)
	if err != nil {
		return err
	}
	if err := validateFormat(CLI.Emit.Format); err != nil {
		return err
	}
	if CLI.Emit.Order < 1 {
		return errOrder
	}
	if CLI.Emit.Window < 1 {
		return errEmitWindow
	}

	opts := emitOpts{
		data:    data,
		root:    root,
		project: CLI.Emit.Project,
		order:   CLI.Emit.Order,
		window:  CLI.Emit.Window,
		minBits: CLI.Emit.MinBits,
		dryRun:  CLI.Emit.DryRun,
		sigPath: CLI.Emit.Signatures,
	}
	res, cands, err := runEmit(opts)
	if err != nil {
		return err
	}

	if CLI.Emit.Format == fmtJSON {
		return out.JSON(os.Stdout, map[string]any{
			"emitted": res.Written, "derived": res.Derived, "sources": res.Sources,
			"learned": res.Learned, "dry_run": res.DryRun, "candidates": cands,
			keyTotal: len(cands),
		})
	}
	sink := out.NewSink(os.Stdout, 0, 0)
	defer sink.Close()
	mode := "wrote"
	if res.DryRun {
		mode = "would write (dry-run)"
	}
	sink.Head("emit: %s %d candidate row(s) from %d changed source(s); derived=%d learned-signatures=%d spool=%s",
		mode, res.Written, res.Sources, res.Derived, res.Learned, spool.Dir(data))
	for i := range cands {
		c := &cands[i]
		if !sink.Row("%s  %s@%s  seq %d-%d  novelty=%.2f rec=%d",
			c.ID, c.Source.Session, c.Source.Agent, c.Source.SeqStart, c.Source.SeqEnd,
			c.Signals.NoveltyBits, c.Signals.Recurrence) {
			break
		}
	}
	return nil
}

var errEmitWindow = errors.New("--window must be ≥ 1")

// emitOpts bundles the resolved emit parameters so runEmit is one pure-ish
// entry point the CLI wraps and a test drives directly.
type emitOpts struct {
	data    string
	root    string
	project string
	order   int
	window  int
	minBits float64 // < 0 = auto (corpus mean per-token surprisal)
	dryRun  bool
	sigPath string // "" = <data>/friction_signatures.jsonl
}

// emitResult is the run summary.
type emitResult struct {
	Derived int // candidate spans that cleared the threshold
	Written int // rows actually appended (Derived minus already-spooled duplicates)
	Sources int // transcript sources considered changed (cursor-fresh) this run
	Learned int // friction signatures persisted for cross-run recurrence
	DryRun  bool
}

// emitInputs is the assembled model an emit pass windows into candidates: the
// tokenized corpus, its per-token surprisal, the recurrence index, the emit
// threshold, and the friction registry (kept so the write phase can persist it).
type emitInputs struct {
	corpus    *mine.Corpus
	sources   map[string]transcript.Source
	det       *friction.Detector
	sigPath   string
	rec       recIndex
	tokBits   [][]float64
	threshold float64
}

// loadEmitInputs builds everything the emit pass reads: the corpus + per-token
// surprisal model, the transcript-path index, and the friction recurrence index.
func loadEmitInputs(opts emitOpts) (*emitInputs, error) {
	eventsPath := filepath.Join(opts.data, "events.jsonl")
	corpus, _, err := (&lensOpts{lens: "tool"}).corpus(eventsPath)
	if err != nil {
		return nil, err
	}
	events, err := loadEvents(eventsPath)
	if err != nil {
		return nil, err
	}
	// Session→transcript path map: the Event stream drops Source.Path, so recover
	// it by the same (project/session@agent) key the corpus streams carry.
	sources, err := sourceIndex(opts.root, opts.project)
	if err != nil {
		return nil, err
	}
	// Friction recurrence: seed with known signatures, scan the corpus, keep the
	// learned registry to persist (cross-run recurrence, recurrence.go pattern).
	sigPath := opts.sigPath
	if sigPath == "" {
		sigPath = friction.SigPath(opts.data)
	}
	sigs, err := friction.LoadSignatures(sigPath)
	if err != nil {
		return nil, err
	}
	det := friction.NewDetector(sigs)
	tokBits := mine.TokenSurprise(corpus, opts.order)
	threshold := opts.minBits
	if threshold < 0 {
		threshold = meanFloat2D(tokBits)
	}
	return &emitInputs{
		corpus: corpus, sources: sources, det: det, sigPath: sigPath,
		rec: newRecIndex(det.ScanInto(events)), tokBits: tokBits, threshold: threshold,
	}, nil
}

// runEmit executes the emit pass: build the corpus + per-token surprisal model,
// window each changed transcript stream into candidate spans, and append the
// deduped rows to the spool. It is separated from the CLI so the whole pass is
// testable end-to-end against a fixture data dir.
func runEmit(opts emitOpts) (emitResult, []candrec.Candidate, error) {
	in, err := loadEmitInputs(opts)
	if err != nil {
		return emitResult{}, nil, err
	}

	// Cursor: emit only for sources whose transcript changed since the last run
	// (incremental). Dedup by candidate id is the correctness backstop; the cursor
	// is the cost saver.
	cur := loadEmitCursor(emitCursorPath(opts.data))
	active := cur.changed(in.sources)

	cands := buildCandidates(in, active, opts.window, emitNow())
	res := emitResult{Derived: len(cands), Sources: len(active), DryRun: opts.dryRun}

	if opts.dryRun {
		return dryRunResult(opts, cands, res)
	}
	if err := writeEmit(opts, in, cur, cands, &res); err != nil {
		return emitResult{}, nil, err
	}
	return res, cands, nil
}

// dryRunResult reports what WOULD be written (candidates whose id isn't already
// spooled) without touching disk.
func dryRunResult(opts emitOpts, cands []candrec.Candidate, res emitResult) (emitResult, []candrec.Candidate, error) {
	seen, err := spool.LoadIDs(spool.Dir(opts.data))
	if err != nil {
		return emitResult{}, nil, err
	}
	for i := range cands {
		if _, dup := seen[cands[i].ID]; !dup {
			res.Written++
		}
	}
	return res, cands, nil
}

// writeEmit appends the deduped candidate rows to the spool, persists the learned
// friction registry, and advances the source cursor.
func writeEmit(opts emitOpts, in *emitInputs, cur *emitCursor, cands []candrec.Candidate, res *emitResult) error {
	w, err := spool.NewWriter(spool.Dir(opts.data))
	if err != nil {
		return err
	}
	for i := range cands {
		wrote, werr := w.Append(cands[i])
		if werr != nil {
			return werr
		}
		if wrote {
			res.Written++
		}
	}
	learned, err := friction.PersistLearned(in.sigPath, in.det.Signatures())
	if err != nil {
		return err
	}
	res.Learned = learned

	cur.mark(in.sources)
	return saveEmitCursor(emitCursorPath(opts.data), cur)
}

// buildCandidates windows every active stream into candidate spans. A span clears
// the filter when its mean per-token surprisal is ≥ threshold; below that it is
// routine noise the distiller shouldn't pay to read. Output is sorted
// (session, agent, seq_start) so a golden fixture is stable.
func buildCandidates(in *emitInputs, active map[string]transcript.Source, window int, now time.Time) []candrec.Candidate {
	var cands []candrec.Candidate
	for si, key := range in.corpus.StreamKeys {
		src, ok := active[key]
		if !ok {
			continue
		}
		cands = appendStreamCandidates(cands, in, in.corpus.Streams[si], in.tokBits[si], src, window, now)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i].Source, cands[j].Source
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		if a.Agent != b.Agent {
			return a.Agent < b.Agent
		}
		return a.SeqStart < b.SeqStart
	})
	return cands
}

// appendStreamCandidates windows one stream into candidate spans, keeping only
// the windows whose mean per-token surprisal clears the threshold.
func appendStreamCandidates(cands []candrec.Candidate, in *emitInputs, st []mine.Tok, bits []float64, src transcript.Source, window int, now time.Time) []candrec.Candidate {
	for a := 0; a < len(st); a += window {
		b := min(a+window, len(st))
		toks := st[a:b]
		nb := meanFloat(bits[a:b])
		if nb < in.threshold {
			continue
		}
		cands = append(cands, spanCandidate(in, toks, src, nb, now))
	}
	return cands
}

// spanCandidate assembles one candidate from a token window: its Seq range, the
// sha256 of its Drain fingerprint, and its friction recurrence count.
func spanCandidate(in *emitInputs, toks []mine.Tok, src transcript.Source, nb float64, now time.Time) candrec.Candidate {
	seqStart, seqEnd := toks[0].Seq, toks[len(toks)-1].Seq
	ids := make([]uint32, len(toks))
	for i, tk := range toks {
		ids[i] = tk.ID
	}
	drain := friction.Fingerprint(strings.Join(in.corpus.Tokens(ids), " "))
	occ := in.rec.max(src.Session, src.Agent, seqStart, seqEnd)
	return candrec.New(candrec.Source{
		TranscriptPath: src.Path,
		Project:        src.Project,
		Session:        src.Session,
		Agent:          src.Agent,
		SeqStart:       seqStart,
		SeqEnd:         seqEnd,
	}, now, nb, occ, drain)
}

// sourceIndex maps each transcript stream key (project/session@agent) to its
// Source, so buildCandidates can thread the transcript path onto a candidate. An
// empty project filter admits all; otherwise the project slug must contain it.
func sourceIndex(root, project string) (map[string]transcript.Source, error) {
	srcs, err := transcript.Walk(root)
	if err != nil {
		return nil, err
	}
	out := make(map[string]transcript.Source, len(srcs))
	for _, s := range srcs {
		if project != "" && !strings.Contains(s.Project, project) {
			continue
		}
		out[s.Project+"/"+s.Session+"@"+s.Agent] = s
	}
	return out, nil
}

// ---- recurrence index ----

type seqOcc struct{ seq, occ int }

// recIndex maps a session+agent key to the friction recurrences observed in it,
// so a window can claim the max occurrence count among the friction events inside
// its Seq range.
type recIndex map[string][]seqOcc

func newRecIndex(matches []friction.MatchRecord) recIndex {
	r := recIndex{}
	for _, m := range matches {
		k := m.Session + "@" + m.Agent
		r[k] = append(r[k], seqOcc{seq: m.Seq, occ: m.Occurrence})
	}
	return r
}

// max returns the highest friction occurrence count seen at a Seq within
// [lo,hi] for the session+agent, or 1 when the span carries no recurring
// friction (a span is seen at least once by definition).
func (r recIndex) max(session, agent string, lo, hi int) int {
	best := 1
	for _, so := range r[session+"@"+agent] {
		if so.seq >= lo && so.seq <= hi && so.occ > best {
			best = so.occ
		}
	}
	return best
}

// ---- emit cursor (incremental over transcripts) ----

// emitCursor records the last-seen modtime of each transcript source so a re-run
// re-emits only for changed transcripts. It self-heals: a missing or corrupt
// cursor reads as empty (full scan), never a hard error — a lost cursor costs a
// redundant scan, and the candidate-id dedup keeps the spool duplicate-free.
type emitCursor struct {
	SchemaVersion int               `json:"schema_version"`
	Sources       map[string]string `json:"sources"` // transcript path → RFC3339Nano modtime
}

func emitCursorPath(dataDir string) string {
	return filepath.Join(dataDir, "emit-cursor.json")
}

func loadEmitCursor(path string) *emitCursor {
	cur := &emitCursor{SchemaVersion: candrec.SchemaVersion, Sources: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return cur // missing = empty cursor
	}
	var loaded emitCursor
	if err := json.Unmarshal(b, &loaded); err != nil || loaded.Sources == nil {
		fmt.Fprintf(os.Stderr, "ferret: emit cursor %s unreadable — re-scanning all transcripts\n", path)
		return cur // corrupt = self-heal to empty
	}
	loaded.SchemaVersion = candrec.SchemaVersion
	return &loaded
}

// changed returns the subset of sources whose transcript is new or modified since
// the cursor last saw it (or all of them on a fresh cursor).
func (cur *emitCursor) changed(sources map[string]transcript.Source) map[string]transcript.Source {
	out := make(map[string]transcript.Source, len(sources))
	for key, s := range sources {
		fi, err := os.Stat(s.Path)
		if err != nil {
			continue // unreadable transcript — skip, don't emit a dangling pointer
		}
		prev, ok := cur.Sources[s.Path]
		if ok {
			if t, perr := time.Parse(time.RFC3339Nano, prev); perr == nil && !fi.ModTime().After(t) {
				continue // unchanged since last run
			}
		}
		out[key] = s
	}
	return out
}

// mark records the current modtime for every source considered, so the next run
// treats them as unchanged unless they grow again.
func (cur *emitCursor) mark(sources map[string]transcript.Source) {
	for _, s := range sources {
		if fi, err := os.Stat(s.Path); err == nil {
			cur.Sources[s.Path] = fi.ModTime().UTC().Format(time.RFC3339Nano)
		}
	}
}

func saveEmitCursor(path string, cur *emitCursor) error {
	b, err := json.Marshal(cur)
	if err != nil {
		return err
	}
	if err := durable.WriteTempRename(path, b); err != nil {
		return err
	}
	return durable.SyncDir(filepath.Dir(path))
}

// ---- small float helpers ----

func meanFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

func meanFloat2D(rows [][]float64) float64 {
	sum, n := 0.0, 0
	for _, r := range rows {
		for _, x := range r {
			sum += x
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
