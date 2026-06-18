# ferret-2p6 — Gates subcommand spike: deterministic gate extraction + overlap ratio (ω)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

status: **draft** (awaiting dk approval — this bead is `has_design_questions=true`; planned unattended in a /team plan wave)
date: 2026-06-17
bead: ferret-2p6 (P2, task, `requires_plan=true`, `requires_review=false`, `blast_radius=5`)
builds on shared design: `docs/design/ferret-kuv-deterministic-scorers.md` (D1, D2, D3 — **authoritative**, override per-bead notes)
depends on: ✓ ferret-kuv.4 (conformance scorer — the deterministic-scorer template this mirrors)

---

## Goal

A **spike** subcommand `ferret gates` that mines the gate-review half of the corpus
**deterministically, LLM-free**:

1. **Extract** gate checks from the canonical event stream for a known gate-tool list
   (a "gate" = a review tool: `code-review`, `plan-review`, `precommit`, `QA`).
2. **Classify** each gate's decision `APPROVED | NEEDS_REVISION | ESCALATE` by regex
   over the gate's `tool_result` text (`Event.Detail` / `Event.Target`). Unknown gate
   tools fall through to an `UNKNOWN` bucket — the LLM-fallback boundary is *marked, not
   built* in this spike.
3. **Compute the overlap ratio ω** — Jaccard over rejection sets:
   `ω(A,B) = |reject(A) ∩ reject(B)| / |reject(A) ∪ reject(B)|`, where `reject(G)` is the
   set of sessions in which gate `G` returned a non-APPROVED decision. Low ω =
   **complementary** gates (they catch different problems); high ω = **redundant** gate
   (a burn to cut — on-brand cost-leak framing).
4. Treat **gate rejection as ground-truth friction** (no inference): revision sequences
   split at APPROVED boundaries are *confirmed* friction loops — higher precision than
   `mine`'s surprise-inferred friction.

**The spike deliverable is the OUTPUT, not hardened code:** does ω discriminate redundant
gates on the real corpus? That answer informs ferret-kuv.4. A throwaway / explicitly
experimental path is acceptable.

---

## Spike framing — what question this answers, what is out of scope

**The one question:** *On dk's real corpus, does ω separate redundant gate pairs from
complementary ones cleanly enough to act on (cut a redundant gate)?* Everything in this
plan exists to get that number printed against real data.

**Explicitly out of scope (do NOT build in this spike):**

- **No LLM call.** Unknown gate tools are bucketed `UNKNOWN` and counted; the hybrid
  LLM-classify-the-unknowns step belongs analyst-side in a *later* bead, not here
  (bead NOTES "Do NOT touch internal/analyst"). Mark the seam, don't cross it.
- **No production hardening.** No config surface for the gate-tool list beyond a single
  in-code `var` (see Open Question 1), no schema versioning, no perf tuning. One pass
  over `events.jsonl`, in memory.
- **Do NOT fold ω into `conform.Align`.** `conform/` is *frozen* for this epic (D2). Gates
  is a separate deterministic miner (rejection = ground-truth friction), complementary to
  conform's reference-replay scoring. Leave `internal/conform/**`, `internal/mine/**`,
  `internal/analyst/**` untouched.
- **No new `Finding` kind** and no churn to `bucketKind`/`mdSections` (D3). Per-gate
  rejection sets + pairwise ω ride a **new dedicated result struct** rendered by the
  gates output path — they are neither motifs (Finding) nor per-task `segResult` rows;
  they are a corpus-level cross-gate aggregate. (See Open Question 3 — this is the one
  place D3 needs an explicit read.)

---

## Context / prior art

### Decisions the shared design already settled (authoritative)

The bead NOTES predate the `ferret-kuv` shared design doc. Where they conflict, the
shared design **wins**, and the conflict is surfaced as an Open Question.

| Decision | Shared-design ruling (authoritative) | This plan's adoption |
|---|---|---|
| **D1 — user-turn text** | Capture once in `Event.Text` (`KindPrompt` only) | **N/A to gates.** Gate decisions come from *agent-side* `tool_result` text (`Event.Detail`/`Target`), not human prompts. ferret-2p6 does **not** depend on D1. Noted so the implementer doesn't wire it. |
| **D2 — scorer home** | New `internal/score/` package; `conform`/`mine` frozen | **Conflict with bead NOTES** (which say `internal/gates/`). This plan places code in **`internal/score/`** per D2. → **Open Question 1.** |
| **D3 — Finding model** | Per-task scores ride `segResult`, NOT `Finding`; motifs reuse `Kind=friction` | Gates output is a *corpus-level cross-gate aggregate*, not a per-task row and not a motif. Rides a **new result struct**, not `Finding`, not `segResult`. → **Open Question 3.** |

### The deterministic-scorer template (mirror this)

- `internal/conform/conform.go` — `Align(reference, observed) Result`: a **pure function**,
  LLM-free, analyst supplies semantic tags, ferret owns only arithmetic. Byte-stable:
  same input → same `Result`. **Mirror this split** — deterministic regex-classify + ω
  here; the package doc header carries the algorithm + prior-art citation, exactly like
  `conform.go`'s header cites Adriansyah et al. and van der Aalst/Rozinat.
- `cmd/ferret/conformance.go` — the subcommand shape to copy:
  - `cmdConformance()` validates `--format`, reads input, calls the pure scorer, dispatches
    `writeConformanceJSON` / `writeConformanceText`.
  - `writeConformanceJSON` uses `out.JSON(w, map[string]any{…})` — **the JSON schema is the
    contract** (bead `api-contract` marker: keep it analyst-ingestable).
  - `writeConformanceText` writes a dense `≡`-prefixed legend + body via `bufio.Writer`.
- `cmd/ferret/segment.go` — the corpus-walk seam. `segmentSource(src)` is "the per-source
  seam the corpus half iterates over." Gates is corpus-level, so it walks **all** events,
  not one session.

### How events reach the scorer (verified)

- `internal/event/event.go:9-30` — `Event` carries everything gates needs, **no new field**:
  `Action` (tool name — match the gate-tool list), `Status` (`ok`/`fail`/`cfail`/`none`),
  `Session`, `Target`, `Detail` (raw command segment / result text, truncated). tool_use→
  tool_result pairing is already done at ingest (`internal/event/codec.go`).
- `internal/event/codec.go:145` — `func Read(path string, fn func(*Event) error) error`
  streams the artifact. Precedent callers: `internal/mine/stream.go:68`,
  `internal/mine/stats.go:47`. **Gates reads the corpus this same way.**
- `cmd/ferret/main.go:356` — `(*common).eventsPath()` returns `<data>/events.jsonl`.
- `cmd/ferret/main.go:388,412` — `ensureData()` runs a default ingest when the artifact is
  missing. Gates should call the same guard so `ferret gates` works on a cold cache.

### External prior art (cite in code, keep discard-rationale OUT of code)

- `github.com/mrothroc/claude-code-log-analyzer` — `gate_analyzer.py` (the gate-extraction
  + decision-classify recipe).
- `michael.roth.rocks/research/overlap-ratio` — the overlap-ratio / Jaccard-over-rejections
  method. ω itself is the novel, fully-deterministic adaptation that fits ferret better
  than the embedding-heavy source. **Cite both in the `internal/score/gates.go` package/ω
  doc comments** (mirroring how `conform.go` cites its algorithm sources). Do **not** put
  "we rejected the embedding approach because…" in code — that rationale lives here.

---

## File structure

All NEW except the 3 wiring sites in `main.go`. This is a net-new subcommand modeled on
`conformance`/`segments`.

| File | Responsibility |
|---|---|
| `internal/score/gates.go` | NEW. Pure, LLM-free: gate-tool membership, decision classifier (regex `APPROVED`/`NEEDS_REVISION`/`ESCALATE`/`UNKNOWN`), rejection-set accumulation per gate, pairwise ω (Jaccard), revision-loop split at APPROVED boundaries. Package + ω doc comments cite the two external sources. |
| `internal/score/gates_test.go` | NEW. Table-driven, mirrors `internal/conform/conform_test.go`. |
| `cmd/ferret/gates.go` | NEW. kong wiring + text/json render, mirrors `cmd/ferret/conformance.go` (`cmdGates`, `writeGatesJSON`, `writeGatesText`). Streams the corpus via `event.Read` + `ensureData`. |
| `cmd/ferret/gates_test.go` | NEW. Mirrors `cmd/ferret/conformance_test.go` shape (render/IO + golden JSON schema). |
| `cmd/ferret/main.go` | MODIFY, 3 sites: (a) CLI struct — add `Gates struct{…}` next to `Conformance` (~:212); (b) usage string (~:279) add a `ferret gates …` line; (c) command switch (~:316) add `case "gates": err = cmdGates()`. |

> **D2 placement note:** bead NOTES name `internal/gates/`; D2 mandates `internal/score/`.
> This plan uses `internal/score/gates.go` (the file name keeps the "gates" identity; the
> package is the shared reference-free-scorer home). See Open Question 1.

---

## Approach — data model & algorithm

### Types (in `internal/score`, package `score`)

```go
// Decision is a gate's verdict, classified deterministically from tool_result text.
type Decision string

const (
	DecisionApproved Decision = "APPROVED"       // gate passed; clears a revision loop boundary
	DecisionRevision Decision = "NEEDS_REVISION" // gate rejected — ground-truth friction
	DecisionEscalate Decision = "ESCALATE"       // gate punted to a human/other gate — friction
	DecisionUnknown  Decision = "UNKNOWN"        // text matched no pattern, or non-listed gate tool
)

// GateCheck is one extracted gate invocation: which gate ran, in which session, at
// which event sequence number, and how it was classified. Span is the analyst-free,
// per-event unit; ferret owns only extraction + arithmetic.
type GateCheck struct {
	Gate     string   `json:"gate"`    // the gate-tool name (Event.Action)
	Session  string   `json:"session"` // Event.Session
	Seq      int      `json:"seq"`     // Event.Seq — ordering within the source
	Decision Decision `json:"decision"`
}

// GatePair is the overlap between two gates' rejection sets.
type GatePair struct {
	A, B    string  `json:"a","b"`
	Shared  int     `json:"shared"` // |reject(A) ∩ reject(B)| in sessions
	Union   int     `json:"union"`  // |reject(A) ∪ reject(B)|
	Omega   float64 `json:"omega"`  // Jaccard; 0 = complementary, 1 = redundant
}

// GatesResult is the whole corpus-level emission (the JSON contract).
type GatesResult struct {
	Checks      []GateCheck         `json:"checks"`              // every classified gate invocation
	Rejections  map[string][]string `json:"rejections"`          // gate -> sorted session ids it rejected (the ω input)
	Pairs       []GatePair          `json:"pairs"`               // pairwise ω, gate-name-sorted for determinism
	Loops       []RevisionLoop      `json:"loops,omitempty"`     // confirmed friction loops (revision runs split at APPROVED)
	Unknown     int                 `json:"unknown,omitempty"`   // checks that classified UNKNOWN (LLM-fallback candidates)
}

// RevisionLoop is a confirmed friction loop: a run of NEEDS_REVISION/ESCALATE checks
// for one gate in one session, bounded by an APPROVED (or end-of-session). Length is
// the count of rejections before the clearing APPROVED — ground-truth, not inferred.
type RevisionLoop struct {
	Gate    string `json:"gate"`
	Session string `json:"session"`
	Length  int    `json:"length"`
	Cleared bool   `json:"cleared"` // true if a final APPROVED closed it; false = still-open/abandoned
}
```

### Pure functions (the testable core — LLM-free, byte-stable)

```go
// gateTools is the known gate-review tool list. SPIKE: a single in-code var (Open Q1).
// Matched case-insensitively against Event.Action, and against the mcp:server.tool /
// slash-command tail where a gate runs as a command (Open Q2).
var gateTools = map[string]bool{
	"code-review": true, "plan-review": true, "precommit": true, "qa": true,
}

// Classify maps a gate's tool_result text to a Decision by regex, APPROVED-first.
// Patterns are ordered so the strongest signal wins deterministically. Borrowed
// recipe: claude-code-log-analyzer gate_analyzer.py.
func Classify(resultText string, status string) Decision { … }

// Extract walks classified gate checks into a GatesResult: builds per-gate rejection
// sets, computes pairwise ω over every gate pair, and splits revision loops at
// APPROVED boundaries. Pure: same []GateCheck -> same GatesResult, always.
// ω method: michael.roth.rocks/research/overlap-ratio (Jaccard over rejection sets).
func Extract(checks []GateCheck) GatesResult { … }

// omega is Jaccard over two session-id sets. Defined here so the metric has one home
// and one doc comment carrying the citation.
func omega(rejectA, rejectB map[string]bool) (shared, union int, w float64) { … }
```

**Decision-classify regex sketch (spike — refine against real text, Open Q2):**

- `APPROVED`: `(?i)\b(approved|lgtm|no (issues|blockers) found|passed|✅|ready to (merge|ship))\b`
- `NEEDS_REVISION`: `(?i)\b(needs[- ]revision|changes requested|must fix|blocker|rejected|❌|found \d+ (issue|problem))\b`
- `ESCALATE`: `(?i)\b(escalat|defer to (human|dk)|cannot (decide|determine)|needs human)\b`
- else → `UNKNOWN` (also: any `Event.Action` not in `gateTools` is never extracted at all;
  UNKNOWN is reserved for *listed* gates whose text didn't match — those are the
  LLM-fallback candidates).

`Status` is a tiebreak input, not the primary signal: a gate tool with `Status=fail` and
no decisive text leans `NEEDS_REVISION`; the text regex wins when it matches.

### ω discrimination check (the spike's actual deliverable)

`Extract` emits `Pairs` sorted by gate name. The acceptance read is: do the pairs split
into a clean low-ω cluster (complementary) vs high-ω cluster (redundant) on the real
corpus? The text renderer prints each pair as `A × B  ω=0.NN  (shared S / union U)` with a
one-line legend `low ω = complementary · high ω = redundant gate (cut candidate)`.

---

## Task-by-task

### Task 1 — `internal/score/gates.go`: types + decision classifier

**Files:** Create `internal/score/gates.go`, `internal/score/gates_test.go`.

**Produces:** `Decision` consts, `GateCheck`, `Classify(resultText, status string) Decision`,
`gateTools` var, `IsGate(action string) bool`.

- [ ] **Step 1 — Write the failing test** for `Classify` and `IsGate` (table-driven, mirror
  `conform_test.go` style):

```go
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		status string
		want   Decision
	}{
		{"approved lgtm", "LGTM, no blockers found", "ok", DecisionApproved},
		{"approved emoji", "✅ ready to merge", "ok", DecisionApproved},
		{"revision blocker", "found 3 issues; changes requested", "fail", DecisionRevision},
		{"revision rejected", "rejected — must fix the nil deref", "fail", DecisionRevision},
		{"escalate human", "cannot decide; defer to human", "ok", DecisionEscalate},
		{"unknown text", "ran the linter and some other stuff", "ok", DecisionUnknown},
		{"status fail no text leans revision", "", "fail", DecisionRevision},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.text, c.status); got != c.want {
				t.Fatalf("Classify(%q,%q) = %q, want %q", c.text, c.status, got, c.want)
			}
		})
	}
}

func TestIsGate(t *testing.T) {
	if !IsGate("code-review") || !IsGate("Code-Review") {
		t.Fatal("code-review should be a gate (case-insensitive)")
	}
	if IsGate("Read") {
		t.Fatal("Read is not a gate")
	}
}
```

- [ ] **Step 2 — Run it, verify it fails** (`go test ./internal/score/ -run 'TestClassify|TestIsGate'` → undefined symbols).
- [ ] **Step 3 — Implement** the types, `gateTools`, `IsGate`, `Classify` (compiled regexes as package vars; APPROVED-first ordering; `Status=fail` fallback to `NEEDS_REVISION`). Package doc header cites `claude-code-log-analyzer gate_analyzer.py` for the classify recipe — mirror `conform.go`'s header style.
- [ ] **Step 4 — Run tests, verify pass.**
- [ ] **Step 5 — Commit** (`internal/score/gates.go` + test).

### Task 2 — `internal/score/gates.go`: ω + Extract aggregation

**Files:** Modify `internal/score/gates.go`, `internal/score/gates_test.go`.

**Consumes:** Task 1's `GateCheck`, `Decision`.
**Produces:** `GatePair`, `GatesResult`, `RevisionLoop`, `Extract([]GateCheck) GatesResult`, `omega(...)`.

- [ ] **Step 1 — Write the failing tests** for ω discrimination + loop split:

```go
func TestExtractOmega(t *testing.T) {
	// Gate A rejects sessions {s1,s2}; B rejects {s2,s3}; C rejects {s1,s2} (== A).
	checks := []GateCheck{
		{"A", "s1", 1, DecisionRevision}, {"A", "s2", 2, DecisionRevision},
		{"B", "s2", 3, DecisionRevision}, {"B", "s3", 4, DecisionRevision},
		{"C", "s1", 5, DecisionRevision}, {"C", "s2", 6, DecisionRevision},
		{"A", "s4", 7, DecisionApproved}, // approvals don't enter rejection sets
	}
	res := Extract(checks)
	pair := findPair(res.Pairs, "A", "B") // complementary
	if pair.Omega >= 0.5 {
		t.Fatalf("A×B ω = %.2f, want low (complementary): shared 1 / union 3", pair.Omega)
	}
	red := findPair(res.Pairs, "A", "C") // identical rejection sets => redundant
	if red.Omega != 1.0 {
		t.Fatalf("A×C ω = %.2f, want 1.0 (redundant)", red.Omega)
	}
}

func TestExtractRevisionLoop(t *testing.T) {
	// Two rejections then an APPROVED in one session => a cleared loop of length 2.
	checks := []GateCheck{
		{"A", "s1", 1, DecisionRevision},
		{"A", "s1", 2, DecisionRevision},
		{"A", "s1", 3, DecisionApproved},
	}
	res := Extract(checks)
	if len(res.Loops) != 1 || res.Loops[0].Length != 2 || !res.Loops[0].Cleared {
		t.Fatalf("want one cleared loop length 2, got %+v", res.Loops)
	}
}
```

- [ ] **Step 2 — Run, verify fail** (undefined `Extract`/`findPair`; add `findPair` test helper).
- [ ] **Step 3 — Implement** `Extract`: build `map[gate]map[session]bool` rejection sets; emit `Pairs` for every unordered gate pair in **gate-name-sorted order** (determinism); `omega` = Jaccard; walk per-(gate,session) `Seq`-ordered runs, splitting `RevisionLoop`s at APPROVED. The ω doc comment cites `michael.roth.rocks/research/overlap-ratio`. Sort `Rejections` session lists for byte-stable JSON.
- [ ] **Step 4 — Run tests, verify pass.**
- [ ] **Step 5 — Commit.**

### Task 3 — `cmd/ferret/gates.go`: corpus read + render + CLI wiring

**Files:** Create `cmd/ferret/gates.go`, `cmd/ferret/gates_test.go`; modify `cmd/ferret/main.go` (3 sites).

**Consumes:** Task 2's `score.Extract`, `score.GatesResult`; `event.Read`; `ensureData`/`eventsPath`; `out.JSON`.

- [ ] **Step 1 — Write the failing render test** (mirror conformance render tests — feed a built `GatesResult`, assert JSON keys + a text legend line):

```go
func TestWriteGatesJSON(t *testing.T) {
	res := score.GatesResult{
		Rejections: map[string][]string{"A": {"s1"}, "B": {"s1"}},
		Pairs:      []score.GatePair{{A: "A", B: "B", Shared: 1, Union: 1, Omega: 1.0}},
		Unknown:    2,
	}
	var b bytes.Buffer
	if err := writeGatesJSON(&b, res); err != nil { t.Fatal(err) }
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil { t.Fatal(err) }
	for _, k := range []string{"pairs", "rejections", "unknown"} {
		if _, ok := got[k]; !ok { t.Fatalf("JSON missing key %q", k) }
	}
}
```

- [ ] **Step 2 — Run, verify fail.**
- [ ] **Step 3 — Implement** `cmd/ferret/gates.go`:
  - `cmdGates()` — validate `--format` (text|json, reuse `errBadFormat`); resolve data dir + `ensureData`; `event.Read(eventsPath, …)` collecting `GateCheck`s where `score.IsGate(ev.Action)` (classify via `score.Classify(ev.Detail+" "+ev.Target, ev.Status)`); call `score.Extract`; dispatch JSON/text.
  - `writeGatesJSON(w, res)` — `out.JSON(w, map[string]any{"checks":…,"rejections":…,"pairs":…,"loops":…,"unknown":…})` (analyst-ingestable contract).
  - `writeGatesText(w, res)` — `≡` legend (`low ω = complementary · high ω = redundant gate (cut candidate)`), per-gate rejection counts, one row per pair `A × B  ω=0.NN  (shared S / union U)`, then confirmed-loop roll-up + `UNKNOWN` count (LLM-fallback candidates).
- [ ] **Step 4 — Wire `main.go`** (3 sites): `Gates struct{ Format string ... }` with a `cmd:""` help line next to `Conformance`; usage-string line `ferret gates [--data DIR] [--format text|json]`; `case "gates": err = cmdGates()`.
- [ ] **Step 5 — Run tests, verify pass.**
- [ ] **Step 6 — Commit.**

### Task 4 — `make check` green + spike output capture

- [ ] **Step 1 — Run `make check`** (build + vet + lint + race + nilcheck — the repo's full gate). Fix any nilaway/unparam fallout (precedent: keep empty slices non-nil where nilcheck requires).
- [ ] **Step 2 — Run the spike against the real corpus:** `ferret gates --format text` and `ferret gates --format json`. Capture the ω pairs table — **this output is the deliverable.** Eyeball: do redundant gate pairs separate from complementary ones?
- [ ] **Step 3 — Record the finding** (does ω discriminate?) for ferret-kuv.4, via `bd comment ferret-2p6` or a `bd remember` note. This closes the spike.
- [ ] **Step 4 — Commit** any final cleanup.

---

## Test strategy

- **Unit, table-driven, mirror `internal/conform/conform_test.go`.** The pure core
  (`Classify`, `omega`, `Extract`, loop-split) is fully deterministic → exhaustive table
  cases, no fixtures needed for the algorithm.
- **ω discrimination is the load-bearing assertion** (bead done-signal): an explicit
  low-ω-complementary case AND a high-ω-redundant (identical rejection sets → ω=1.0) case.
- **Loop split:** a multi-rejection-then-APPROVED case asserting `Length` + `Cleared`.
- **Render/IO** (`cmd/ferret/gates_test.go`): feed a constructed `GatesResult`, assert the
  JSON contract keys are present and the text legend renders — same shape as the
  conformance render tests. No live `events.jsonl` needed in unit tests.
- **make check** must be green (build/vet/lint/race/nilcheck), per the bead done-signal.
- Spike corpus run (Task 4) is **manual validation**, not an automated test — its output
  is the deliverable that informs kuv.4.

---

## Open Questions (for dk)

1. **D2 vs bead NOTES — package home.** Bead NOTES say `internal/gates/gates.go`; the
   **shared design D2 says `internal/score/`** (the shared home for all 5 reference-free
   per-task scorers) and freezes `conform`/`mine`. This plan follows **D2** →
   `internal/score/gates.go`. *Confirm `internal/score/` (not a standalone `internal/gates/`).*
   Note gates is corpus-level (cross-gate aggregate), slightly off D2's "per-task" framing —
   but D2's intent (a third home that isn't `conform`/`mine`) still fits; `internal/score/`
   is the right package, gates is just its first *corpus-level* member.

2. **Gate-tool identity + decision regex (the core spike unknowns the bead flags).**
   (a) Is the gate-tool list `{code-review, plan-review, precommit, QA}` matched on raw
   `Event.Action`, or on the **mcp:/slash-command tail** (these gates often run as
   `/code-review`, `mcp__…__review`, or a subagent — so `Event.Action` may be `Task` or a
   slash-command envelope, not the literal `code-review`)? This determines whether
   extraction finds anything at all on the real corpus. (b) Are the proposed
   APPROVED/NEEDS_REVISION/ESCALATE regex patterns right for dk's actual gate output text?
   These are spike guesses to refine in Task 4 against real `Event.Detail`/`Target`.

3. **D3 — where gates output rides.** D3 settles per-task scores onto `segResult` and
   motifs onto `Finding`. Gates ω is **neither** — it's a corpus-level cross-gate
   aggregate. This plan gives it a **new dedicated `GatesResult` struct + gates output
   path** (not `Finding`, not `segResult`), which honors D3's "don't force it into Finding"
   spirit. *Confirm a new result struct is acceptable rather than shoehorning into an
   existing surface.*

4. **UNKNOWN → LLM fallback boundary.** This spike buckets unlisted-pattern gate results as
   `UNKNOWN` and counts them, but does **not** call the analyst (NOTES: "LLM only for
   UNKNOWN gate tools … belongs analyst-side later, not in this spike"). *Confirm the
   marked-but-not-built seam is the right spike scope, and that the analyst-side
   classify-the-unknowns step is a follow-up bead.*

5. **Spike disposition.** Done-signal allows "a throwaway/marked-experimental path." After
   Task 4 produces the ω-discrimination finding, should the code **stay** (promote to a real
   subcommand) or be **marked experimental** pending the kuv.4 read? Default assumption:
   keep it (it's small, tested, `make check`-green) and let the corpus finding drive
   whether kuv.4 adopts ω.

---

## Acceptance / exit criteria (from bead done-signal)

- [ ] `make check` green (build/vet/lint/race/nilcheck).
- [ ] `internal/score/gates_test.go` table cases pass: extraction classifies a known
      APPROVED/NEEDS_REVISION pair; ω computes Jaccard over two gates' rejection sets —
      **verified low-ω = complementary, high-ω = redundant**.
- [ ] `ferret gates` emits per-gate rejection sets + pairwise ω; revision sequences split
      at APPROVED boundaries surface as confirmed friction loops.
- [ ] JSON output is analyst-ingestable (the `api-contract` marker), shaped like
      `writeConformanceJSON`.
- [ ] **Spike deliverable:** the corpus-run ω table captured + a recorded finding —
      *does ω discriminate redundant gates on the real corpus?* — to inform ferret-kuv.4.
- [ ] `internal/conform/**`, `internal/mine/**`, `internal/analyst/**` untouched; no new
      `Finding` kind; no `bucketKind`/`mdSections` churn.

---

## Self-review

- **Spec coverage:** every bead done-signal bullet maps to an acceptance item; every "Do NOT
  touch" maps to an out-of-scope fence. ✓
- **D1/D2/D3:** D1 noted N/A (gates reads agent-side result text, not human prompts); D2
  resolved to `internal/score/` with the bead-NOTES conflict raised as Open Q1; D3 resolved
  to a new result struct with the corpus-level mismatch raised as Open Q3. ✓
- **Type consistency:** `GateCheck`/`Decision`/`Extract`/`GatesResult`/`GatePair`/
  `RevisionLoop` names are used identically across Tasks 1–3 and the test code. ✓
- **No placeholders:** every code step shows the test or the signature it introduces. The
  decision regex is a sketch *explicitly* flagged for real-corpus refinement (Open Q2) —
  that's a spike unknown, not a plan placeholder. ✓
