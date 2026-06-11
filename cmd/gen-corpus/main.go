// Command gen-corpus emits a deterministic synthetic Claude Code transcript
// corpus in the ~/.claude/projects layout, for exercising ferret end-to-end.
//
//	go run ./cmd/gen-corpus -out /tmp/corpus -sessions 24 -seed 7
//	ferret ingest -root /tmp/corpus
//
// Output is reproducible: same flags -> byte-identical files. Timestamps are
// derived from a fixed epoch plus the line sequence, never the wall clock, so
// retry windows and ordering are stable across runs.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", "", "output root (required); written in ~/.claude/projects layout")
	sessions := flag.Int("sessions", 12, "number of sessions to emit")
	seed := flag.Int64("seed", 42, "PRNG seed; same seed+flags => identical corpus")
	project := flag.String("project", "-Users-dev-Projects-demo", "project slug (directory name)")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "gen-corpus: -out is required")
		os.Exit(2)
	}
	if err := run(*out, *project, *sessions, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "gen-corpus:", err)
		os.Exit(1)
	}
}

func run(out, project string, sessions int, seed int64) error {
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // determinism is the point, not security
	projDir := filepath.Join(out, project)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		return err
	}

	// Weighted archetype mix: routines dominate (so n-grams rank them),
	// friction and exploration give retry chains and noise.
	archetypes := []func(*gen){feature, feature, feature, friction, friction, explore, withSubagent}

	for i := range sessions {
		sid := fmt.Sprintf("sess-%04d-%08x", i, rng.Uint32())
		g := newGen(projDir, project, sid, seed+int64(i))
		archetypes[rng.Intn(len(archetypes))](g)
		if err := g.flush(); err != nil {
			return err
		}
	}
	fmt.Printf("gen-corpus: wrote %d sessions to %s/%s\n", sessions, out, project)
	return nil
}

// ---- generator state ----

// gen accumulates lines for one session (and an optional subagent file).
type gen struct {
	projDir, project, session string
	rng                       *rand.Rand
	clock                     time.Time
	n                         int // uuid/tool-id counter
	lines                     []string
	subLines                  []string // subagent transcript, if any
	subAgent                  string
}

func newGen(projDir, project, session string, seed int64) *gen {
	return &gen{
		projDir: projDir, project: project, session: session,
		rng:   rand.New(rand.NewSource(seed)), //nolint:gosec // deterministic fixtures, not security-sensitive
		clock: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC),
	}
}

func (g *gen) tick(d time.Duration) string {
	g.clock = g.clock.Add(d)
	return g.clock.Format(time.RFC3339)
}

func (g *gen) id(prefix string) string {
	g.n++
	return fmt.Sprintf("%s-%s-%06d", prefix, g.session, g.n)
}

func (g *gen) emit(s string)    { g.lines = append(g.lines, s) }
func (g *gen) emitSub(s string) { g.subLines = append(g.subLines, s) }

// prompt emits a genuine user turn (text, no tool_result).
func (g *gen) prompt(text string) {
	ts := g.tick(2 * time.Second)
	g.emit(fmt.Sprintf(
		`{"type":"user","uuid":%q,"sessionId":%q,"timestamp":%q,"version":"1.0.0","message":{"role":"user","content":%q}}`,
		g.id("p"), g.session, ts, text))
}

// tool runs a tool_use + tool_result pair. fail seconds default the gap.
func (g *gen) tool(name, inputJSON string, fail bool) {
	id := g.id("t")
	useTS := g.tick(3 * time.Second)
	g.emit(fmt.Sprintf(
		`{"type":"assistant","uuid":%q,"sessionId":%q,"timestamp":%q,"version":"1.0.0","message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}}`,
		g.id("u"), g.session, useTS, id, name, inputJSON))
	resTS := g.tick(time.Duration(400+g.rng.Intn(4000)) * time.Millisecond)
	g.emit(fmt.Sprintf(
		`{"type":"user","uuid":%q,"sessionId":%q,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":%v}]}}`,
		g.id("r"), g.session, resTS, id, fail))
}

func (g *gen) bash(cmd string, fail bool) { g.tool("Bash", jsonObj("command", cmd), fail) }
func (g *gen) read(path string)           { g.tool("Read", jsonObj("file_path", path), false) }
func (g *gen) edit(path string)           { g.tool("Edit", jsonObj("file_path", path), false) }
func (g *gen) grep(pat string)            { g.tool("Grep", jsonObj("pattern", pat), false) }

// noise emits a meta line and a summary line — decoder drop-paths.
func (g *gen) noise() {
	g.emit(fmt.Sprintf(
		`{"type":"user","uuid":%q,"isMeta":true,"message":{"role":"user","content":"<system-reminder>injected context</system-reminder>"}}`,
		g.id("m")))
	g.emit(`{"type":"summary","summary":"prior conversation compacted"}`)
}

func (g *gen) flush() error {
	// Write the subagent file (the dependency) FIRST, then the session file
	// that references it LAST. This keeps the two-artifact write rollback-safe:
	// a subagent MkdirAll/WriteFile failure aborts before the session .jsonl
	// exists, so no orphaned session referencing a missing subagent is left on
	// disk to poison a consumer (ferret ingest -source cc) of the corpus.
	if len(g.subLines) > 0 {
		dir := filepath.Join(g.projDir, g.session, "subagents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		sub := strings.Join(g.subLines, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "agent-"+g.subAgent+".jsonl"), []byte(sub), 0o600); err != nil {
			return err
		}
	}
	body := strings.Join(g.lines, "\n") + "\n"
	return os.WriteFile(filepath.Join(g.projDir, g.session+".jsonl"), []byte(body), 0o600)
}

// ---- archetypes ----

// feature: the canonical edit/test/commit routine, repeated a few times.
// This is the pattern n-grams and seqs should surface as the top routine.
func feature(g *gen) {
	files := []string{"internal/store/store.go", "internal/api/handler.go", "cmd/app/main.go"}
	g.prompt("add the new field and wire it through")
	reps := 2 + g.rng.Intn(2)
	for i := range reps {
		f := files[g.rng.Intn(len(files))]
		g.read(f)
		g.edit(f)
		g.bash("go test ./...", false)
		g.bash("go build ./...", false)
		g.bash("git add -A", false)
		g.bash(`git commit -m "wire field"`, false)
		if i == 0 {
			g.noise()
		}
	}
}

// friction: a retry chain — test fails, edit, test fails, edit, test passes.
// Exercises StatusFail, Retry attribution, and the retry-window close on OK.
func friction(g *gen) {
	g.prompt("the build is broken, fix it")
	f := "internal/parse/parse.go"
	g.read(f)
	g.bash("go test ./...", true)
	g.edit(f)
	g.bash("go test ./...", true)
	g.edit(f)
	g.bash("go test ./...", false)
	// compound: a failed chain tokenizes as cfail on every segment
	g.bash("go vet ./... && go test ./...", true)
}

// explore: grep-heavy, repeated reads, ls trivia — the noisy-context shape.
func explore(g *gen) {
	g.prompt("where is the retry logic")
	for range 3 + g.rng.Intn(3) {
		g.grep("retry")
		g.read("internal/event/build.go")
		g.bash("ls -la internal", false)
		g.bash("cat go.mod", false)
	}
}

// withSubagent: parent spawns a Task; the subagent gets its own transcript,
// keyed (session, agent) so it never interleaves into the parent stream.
func withSubagent(g *gen) {
	g.prompt("explore the codebase and report")
	g.tool("Task", jsonObj("subagent_type", "Explore"), false)
	g.subAgent = "explore-01"
	// subagent stream: isSidechain lines in its own file
	for range 4 {
		id := g.id("st")
		ts := g.tick(3 * time.Second)
		g.emitSub(fmt.Sprintf(
			`{"type":"assistant","uuid":%q,"sessionId":%q,"isSidechain":true,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"Grep","input":%s}]}}`,
			g.id("su"), g.session, ts, id, jsonObj("pattern", "func ")))
		rts := g.tick(800 * time.Millisecond)
		g.emitSub(fmt.Sprintf(
			`{"type":"user","uuid":%q,"sessionId":%q,"isSidechain":true,"timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":false}]}}`,
			g.id("sr"), g.session, rts, id))
	}
	g.bash("git add -A", false)
	g.bash(`git commit -m "report"`, false)
}

// jsonObj builds a one-key JSON object with a properly escaped string value.
func jsonObj(key, val string) string {
	return fmt.Sprintf(`{%q:%q}`, key, val)
}
