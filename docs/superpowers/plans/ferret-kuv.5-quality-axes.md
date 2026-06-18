# Plan — ferret-kuv.5: Quality axes (efficiency/adaptivity per task) + pass^k consistency per intent-cluster

status: **proposed** (awaiting dk approval — `requires_plan=true`, `has_design_questions=true`)
date: 2026-06-17
bead: ferret-kuv.5 · parent: ferret-kuv · depends on: kuv.2 (✓ segmentation), kuv.4 (✓ conformance)
shared design: `docs/design/ferret-kuv-deterministic-scorers.md` — its three decisions (D1/D2/D3) are **authoritative** and override the bead's older "Sweeper enrichment" NOTES wherever they conflict (see Open Questions).

---

## Goal

Two reference-free, LLM-free deterministic scorers over the kuv.2 `segResult` scaffold:

1. **Per-task quality axes** — `efficiency` and `adaptivity` for each task (`segment`), derived
   from the task's own cost/IO (`InBytes`/`OutBytes`, call count) and its off-plan-churn vs
   unabandoned-loop signal. TRACE framing: efficiency = *friction-free*, adaptivity = *recoverable*.
2. **pass^k consistency per intent-cluster** — group same-shape tasks (the `segment.Shape`
   recurrence key) into clusters; score how *consistently* the agent handles a recurring task
   shape (low variance = predictable = "delightful", per tau-bench pass^k). This reuses the
   existing `mine/surprise` predictability metric idea, it does **not** add a parallel variance
   computation.

Both are deterministic pure functions of the segmentation. ferret stays LLM-free; any
**semantic** intent-clustering (clustering by a stated-intent sentence rather than by `Shape`)
is analyst-side and out of scope (engine/analyst split, design-doc invariant).

## Context / prior art

- **Quality axes — TRACE** (arXiv:2510.02837). The borrowed idea: assess an agent trace on
  reference-free *quality axes* (efficiency / hallucination / adaptivity) rather than only
  task-success. We adopt two axes that are **deterministically computable ferret-side** from the
  segmentation — **efficiency** (cost/IO per unit of task work) and **adaptivity** (the agent
  recovered from off-plan churn vs got stuck in an unabandoned loop). We deliberately **drop the
  hallucination axis**: it needs reference/semantic grounding ferret does not have (that is
  analyst territory). Per the epic CONVENTION (nug `cb1c87f041dc`, memory `ferret-cite-algos-in-code`):
  **cite TRACE (arXiv:2510.02837) in the `internal/score/quality.go` package/doc-comment.** Keep
  the discard rationale (why no hallucination axis) OUT of code — it lives here and in the bead.
- **pass^k consistency — tau-bench** (arXiv:2406.12045). The borrowed idea: pass^k measures the
  probability that an agent succeeds on *all k* independent attempts at the same task — a
  *reliability/consistency* metric, not average quality. "Predictable = delightful" (epic bar).
  We adapt it to a **same-shape intent-cluster**: k tasks sharing a `Shape` are k "attempts" at a
  recurring shape; consistency = how tightly their per-task quality clusters (low variance). Cite
  **tau-bench (arXiv:2406.12045)** in the consistency scorer's doc-comment. Note the framing-risk
  flag in Open Questions (kuv.11 set the precedent of verifying a borrowed framing before trusting
  it — there is no paper-verification bead for kuv.5 yet).
- **Predictability metric — `internal/mine/surprise.go`** (`ScoreSurprise`, `FrictionCut`). Existing
  n-gram surprisal + a σ-above-mean cut. Per the bead risk-marker ("extend this rather than add a
  parallel variance computation"), the pass^k consistency axis should **reuse the variance/σ idea
  already in `surprise.go`**, not reinvent one. See Open Questions Q3 for how literally to reuse it.
- **Reference scorer pattern — `internal/conform/conform.go`** (kuv.4 / PR#27): the deterministic,
  LLM-free scorer template. Pure `Align(reference, observed) Result`; analyst sets semantic flags,
  Go owns the arithmetic; per-call localization (`Move.Obs`/`Span`). The quality/consistency
  scorers mirror this shape: pure `Score(...)`/`Consistency(...)` over the `segResult` unit. The
  bead maps adaptivity onto conform's recoverable-vs-loop and per-call friction-attribution.
- **CLI pattern — `cmd/ferret/conformance.go`**: `conformSpec` in, `readConformSpec` (file/stdin),
  `writeConformanceText`/`writeConformanceJSON`, `flowerModel` warning helper. But note: quality
  axes consume the **segmentation artifact**, not an analyst spec — so the input path mirrors
  `cmd/ferret/segment.go` (`segmentSession`/`segmentSource`) more than `conformance.go`. See Approach.
- **Wiring — `cmd/ferret/main.go`**: CLI struct (`Conformance` block ~209, `Segments`/`Candidates`),
  usage block (~277), dispatch `switch` (~291). One new subcommand block + usage line + dispatch case.
- **Output sink — `internal/out/out.go:56`** `JSON(w, v)` — reuse for the JSON writer (conformance does).

## Approach

### One subcommand or two? (explicit)

**One new subcommand: `ferret quality --session PREFIX`.** Both scorers consume the *same*
`segResult` for a session: per-task axes need every `segment`; the intent-cluster consistency is
an *aggregation over those same per-task scores grouped by `Shape`*. Splitting them into two
subcommands would re-segment the session twice and split one coherent report. So: one subcommand,
one segmentation pass, output has a **per-task axes block** and a **per-cluster consistency block**.
(Corpus-level pass^k — clustering *across* sessions — is a possible later mode flagged in Q5; v1 is
single-session, mirroring how `candidates` started single-session then grew a corpus mode.)

### Where the code lives

Per design-doc **Decision 2**: the new deterministic per-task scorers live in the **new
`internal/score/` package** — NOT `internal/mine` (the bead NOTES' "likely internal pkg" predates
the shared doc and once gestured at mine; D2 resolves it to `internal/score/`), and NOT
`internal/conform` (frozen for this epic). The reusable scoring logic moves *out* of `cmd/` into
`internal/score/`; `cmd/ferret/quality.go` stays a thin dispatch+render shell (D2: "cmd stays a
thin dispatch shell").

### Per-task axes (deterministic)

For each `segment` (skip the synthetic preamble, `Index==0`):

- **efficiency** — cost/IO normalised against the task's useful work. The deterministic inputs are
  already on `segment`: `InBytes` (input the task spent), `OutBytes` (result payload pulled in),
  and the call span (`FirstCall..LastCall` → call count). A task that spent many calls / many bytes
  to own few distinct shape-tokens is low-efficiency (friction-laden); a tight task is high. Exact
  formula in Q1 — recommend a bounded ratio in [0,1] so it composes with the other axis.
- **adaptivity** — "recovered from off-plan churn" vs "stuck in an unabandoned loop". The
  deterministic proxy available ferret-side without a reference: **repeated/near-repeated
  shape-tokens within the task** (a loop signal) **that do not terminate the task** vs churn that
  the task moved past. The bead points at `conform.Result` (ModelMoves/LogMoves, per-call
  localization) as the parallel — but conform needs an analyst reference, which a pure
  `ferret quality` run does not have. **Recommend (Q2): compute adaptivity from `segment.Shape`
  self-repetition** (loop detection over the task's own shape tokens — does a repeated sub-sequence
  resolve or run to the boundary), keeping it reference-free; optionally enrich with `conform.Result`
  when a conformance spec is *also* supplied (deferred). This keeps `ferret quality` runnable with
  no analyst input, like `segments`.

### pass^k consistency per intent-cluster (deterministic)

- **Intent-cluster formation (deterministic).** Cluster tasks by their `segment.Shape` token
  sequence — the *same* cross-session recurrence key kuv.12 candidates already cluster on. Cluster
  key = a canonical join of the ordered `Shape` slice (recommend exact ordered-token equality for
  v1; near-shape grouping is Q4). Tasks with empty `Shape` (own no calls) are unclustered/excluded.
  This is **purely deterministic and ferret-only** — it needs no analyst sentence. (The *semantic*
  alternative — cluster by an analyst stated-intent sentence — is explicitly analyst-side and out
  of scope; flagged Q4.)
- **pass^k consistency score per cluster.** For a cluster of k same-shape tasks, score how tightly
  their per-task quality (efficiency, and/or a per-task predictability/surprise reading) clusters.
  **Reuse the `mine/surprise` variance machinery** (mean + σ, the `FrictionCut` σ-above-mean idea)
  rather than writing a new variance — per the bead risk-marker. Low spread across the k attempts =
  high consistency = "predictable". Report per cluster: k (attempt count), the shape key, the
  consistency score, and the spread. A k==1 cluster has no consistency signal (report as such, not 1.0).

### Determinism contract

Same `segResult` → same quality report, always. No time, no map-order output, no randomness —
identical to the segmenter's contract (`segment.go` header). Cluster iteration must be sorted by a
stable key (shape join string) before render.

## File-by-file changes

### New: `internal/score/quality.go`
Package home per **Decision 2** (`internal/score/`). This is the **first file in the package** —
it carries the package doc-comment.

- **Package doc-comment** (`package score`): what reference-free per-task scoring is, the
  engine/analyst split (ferret = deterministic axes; semantic intent-clustering = analyst), and that
  this package is the home for the epic's reference-free / self-reference scorers (D2). If another
  leaf (kuv.10 landmark, kuv.8 terminal-action, bbp) lands the package first, this file does NOT
  redeclare the package comment — see Open Questions Q6 (sequencing).
- **Doc-comment on the quality scorer:** cite **TRACE, arXiv:2510.02837** as the borrowed quality-axes
  method (CONVENTION). Discard rationale (no hallucination axis) stays OUT of code.
- Types (names are the produced interface other code/tests rely on — keep stable):
  - `type Task struct { Index int; Calls int; InBytes, OutBytes int; Shape []string }` — the minimal
    per-task input the scorer needs. Built by adapting `segment` (cmd-side) into this score-side type,
    so `internal/score` does not import `package main`. (Alternative: move `segment`/`segResult` into
    `internal/score` per D2's "move reusable scoring logic out of cmd"; this is Q6 — recommend the
    adapter for v1 to keep the blast radius small, with the type-move as a follow-up.)
  - `type Axes struct { Efficiency, Adaptivity float64 }` — both ∈ [0,1].
  - `type TaskQuality struct { Index int; Shape []string; Axes Axes }`.
  - `func ScoreTask(t Task) Axes` — pure per-task scorer (efficiency from cost/IO/call-count;
    adaptivity from Shape self-repetition). Single function so Q1/Q2 formulas tune in one place.
- **Consistency doc-comment:** cite **tau-bench, arXiv:2406.12045** for pass^k.
  - `type Cluster struct { Shape []string; Key string; Tasks []TaskQuality; K int; Consistency float64; Spread float64 }`.
  - `func Cluster(tasks []TaskQuality) []Cluster` — deterministic grouping by canonical `Shape` key,
    sorted by key. (Rename to avoid the `Cluster` type/func collision — e.g. `ClusterByShape`.)
  - `func Consistency(c Cluster) (score, spread float64)` — **reuses `mine`'s mean/σ helpers** (Q3):
    either call into a small exported helper in `mine` or factor the σ math into a shared
    `internal/score` helper that both consume. v1 spread = σ over the cluster's per-task efficiency
    (or per-task surprise if Q3 picks that); consistency = a bounded inverse of spread.

### New: `cmd/ferret/quality.go` (input mirrors `segment.go`, render mirrors `conformance.go`)
- `cmdQuality()` — validate `Format` (`text|json`); require `--session` (reuse
  `errSpineSessionRequired`); resolve root like `cmdSegments`; call `segmentSession(root, session)`
  to get the `segResult`; adapt each `segment` → `score.Task`; run `score.ScoreTask` per task and
  `score.ClusterByShape` + `score.Consistency`; dispatch to the text/json writer.
- `segToTask(seg segment) score.Task` — the cmd-side adapter (Index, call count from
  `FirstCall/LastCall`, InBytes, OutBytes, Shape). Skip `Index==0` preamble.
- `writeQualityText(w, res, perTask, clusters)` — `≡` about-lines (one line each citing the
  efficiency/adaptivity meaning + that pass^k consistency = predictable-per-shape), then a per-task
  block (`task N  eff=… adapt=…  cost=… shape=…`) reusing `humanBytes`, then a per-cluster block
  (`shape=… k=N consistency=… spread=…`), then a roll-up. A warning helper parallel to
  `flowerModel` (e.g. flag a cluster with high spread → "inconsistent handling of a recurring task").
- `writeQualityJSON(w, ...)` — `out.JSON` map with `session`, `project`, a `tasks` array
  (`index`, `efficiency`, `adaptivity`, `shape`, `cost`), and a `clusters` array (`shape`, `k`,
  `consistency`, `spread`). **The JSON schema is the new contract** — keep field names parallel to
  the conformance/segments JSON style (camelCase, `out.JSON`).

### Edit: `cmd/ferret/main.go`
- CLI struct: add a `Quality` block after `Conformance` (~213):
  ```go
  Quality struct {
      Session string `help:"Session ID prefix (required)." required:"" name:"session"`
      Root    string `help:"Transcript root (dir of ~/.claude/projects layout)." name:"root"`
      Format  string `help:"Output format: text|json." default:"text" name:"format"`
  } `cmd:"" help:"Per-task quality axes (efficiency/adaptivity) + pass^k consistency per same-shape intent-cluster."`
  ```
- Usage description: add one line after the conformance line (~280):
  `ferret quality --session PREFIX [--root DIR] [--format text|json]`.
- Dispatch `switch`: add `case "quality": err = cmdQuality()` after the conformance case (~317).

### New tests
- `internal/score/quality_test.go` — table-driven, mirroring `internal/conform/conform_test.go`
  (terse builder + `countX` helpers, `tests := []struct{…}` table):
  - `ScoreTask`: tight task → high efficiency; high cost/IO for few shape-tokens → low efficiency;
    a resolved repeated shape sub-sequence → higher adaptivity than an unabandoned loop that runs to
    the boundary; both axes bounded [0,1]; empty-Shape/zero-cost edge cases.
  - `ClusterByShape`: identical shapes group; different shapes split; ordering of input tasks does
    not change the (sorted) cluster output (determinism); empty-Shape tasks excluded.
  - `Consistency`: k identical-quality tasks → max consistency / zero spread; one outlier widens
    spread; k==1 cluster → "no signal" sentinel, not a spurious 1.0.
- `cmd/ferret/quality_test.go` — mirror `conformance_test.go`: feed a small synthetic transcript
  (or a crafted `segResult` if the adapter is unit-testable directly), golden **text** output
  (per-task + per-cluster rows + warning) and **JSON** shape (required keys present, values correct,
  cluster ordering stable).

## Test strategy

- **Unit (score):** pure-function table tests prove the determinism contract and the bead Done-signal
  axes — efficiency from cost/IO, adaptivity from recoverable-vs-loop, pass^k consistency aggregated
  per `Shape` cluster. Mirror `conform_test.go` shape exactly (the repo's table-driven convention,
  ADR-008). Determinism asserted by shuffling task input order and checking identical sorted output.
- **CLI golden (cmd):** text + JSON fixtures, mirroring `conformance_test.go`/`segment` tests — root
  resolution, missing-`--session` error, golden strings, JSON-key presence + stable cluster order.
- **Gates:** `make check` green (test + race + nilcheck — repo gates). nilaway: return non-nil empty
  slices where the convention requires (recent fix commit `c93398e`). Match the 100% package coverage
  bar conform set in kuv.4 / PR#27.
- **Citation check:** confirm **TRACE (arXiv:2510.02837)** is cited in the quality scorer doc-comment
  and **tau-bench (arXiv:2406.12045)** in the consistency scorer doc-comment; discard rationale kept
  out of code (CONVENTION compliance).

## Open Questions (for dk — bead is `has_design_questions=true`)

**Q1 — efficiency formula.** What exactly normalises cost into efficiency ∈ [0,1]? Inputs available
deterministically: `InBytes`, `OutBytes`, call count, distinct-`Shape`-token count. Candidates:
(a) bytes-per-distinct-shape-token (cheap, work-relative); (b) calls-per-distinct-shape-token;
(c) a blended ratio mapped through a bounded squash. *(Recommend: a bounded ratio of total bytes (or
calls) to distinct useful shape-tokens, squashed to [0,1]; pick the exact denominator with dk. No
corpus normalisation in v1 — keep it single-session and self-contained.)*

**Q2 — adaptivity source: reference-free (Shape self-repetition) vs conform.Result.** The bead NOTES
map adaptivity onto `conform.Result` (recoverable churn vs unabandoned loop) and conform's per-call
localization. But `conform.Align` **requires an analyst-supplied reference**, which a bare
`ferret quality --session` run does not have. **This plan recommends a reference-free adaptivity
proxy from `segment.Shape` self-repetition** (loop sub-sequences that resolve vs run to the boundary),
so `ferret quality` is runnable with zero analyst input — matching how `segments` runs. Optionally,
when a conformance spec is ALSO present, enrich adaptivity from `conform.Result` (ModelMoves/LogMoves)
— defer that to a follow-up. Confirm: reference-free proxy for v1, or require a conformance spec?

**Q3 — how literally to reuse `mine/surprise` for the consistency variance.** The bead risk-marker
says "extend `surprise.go` rather than add a parallel variance computation." Options:
(a) factor the mean/σ math (the `FrictionCut` body) into a shared helper both `mine` and `score`
call (cleanest, no duplication, small refactor of `mine`); (b) export a `mine` helper and call it
from `score`; (c) run `ScoreSurprise` per-task and aggregate its bits per cluster (heaviest reuse —
treats each task's surprisal as its "attempt outcome"). *(Recommend: (a) — extract the σ helper so
"don't reinvent the variance" is satisfied without coupling `score` to the whole corpus model. If dk
wants the consistency signal to literally be surprisal spread, (c).)*

**Q4 — intent-cluster definition: `Shape` (deterministic, ferret-only) vs analyst stated-intent
(semantic).** This is the bead's headline open question. The shared-design invariant (engine/analyst
split) and **Decision 2/3** push the deterministic axes ferret-side, so **this plan clusters by
`segment.Shape`** (kuv.12's recurrence key) — exact ordered-token equality for v1. The semantic
alternative (cluster by an analyst stated-intent sentence) needs the analyst and is **out of scope**
for this deterministic bead. Confirm `Shape`-equality clustering; and decide whether v1 needs
**near-shape** grouping (edit-distance / prefix) or exact-match is enough. *(Recommend: exact `Shape`
equality for v1; near-shape as a later enrichment.)*

**Q5 — single-session vs corpus pass^k.** pass^k is most meaningful with many "attempts" — i.e.
across the whole corpus, not one session (a single session rarely repeats a shape k times). v1 here is
**single-session** (one `--session`, mirroring how `candidates` shipped). Should v1 also support a
corpus mode (omit `--session` → cluster same-shape tasks across all sessions, like
`candidates` corpus mode via `segmentSource` over every transcript)? *(Recommend: ship single-session
v1; add corpus mode as a fast follow if the single-session clusters are too thin to be useful — the
`segmentSource` seam already supports a corpus walk.)*

**Q6 — package bootstrap & the `segResult`-into-`score` move.** `internal/score/` does not exist yet
and several leaves (kuv.5, kuv.10, kuv.8, bbp) all create it. Two coordination points: (a) whichever
leaf lands first writes the `package score` doc-comment — later leaves must not redeclare it; (b)
Decision 2 says "move the reusable scoring logic out of `cmd/ferret/segment.go` into
`internal/score/`." This plan recommends a **cmd-side adapter** (`segToTask`) for v1 rather than
relocating `segment`/`segResult` wholesale, to keep the blast radius small and not collide with
kuv.10/kuv.8/bbp also touching the package. Confirm: adapter now + type-move as a separate follow-up
bead, or do the `segResult` relocation as part of this bead? *(Recommend: adapter now; file a
follow-up for the relocation so it's done once, not raced by four leaves.)*

**Q7 — borrowed-framing verification (kuv.11 precedent).** kuv.11 verified arXiv:2510.09801 before
trusting the re-prompt/pivot framing. The TRACE quality-axes mapping (efficiency→friction-free,
adaptivity→recoverable) and the tau-bench pass^k → same-shape-cluster adaptation are **borrowed
framings applied by analogy**, not yet paper-verified for this exact use. Gate this bead's framing on
a kuv.11-style verification step, or accept the analogy and cite-as-inspiration? *(Recommend: cite the
two papers as the inspiration in doc-comments — the arithmetic is ours and deterministic — and note
in the bead that the *framing* is an adaptation, not a faithful reimplementation. A separate
verification bead only if dk wants the mapping defended.)*

## Acceptance (from the bead Done-signal + design-doc constraints)

- [ ] `make check` green (test + race + nilcheck).
- [ ] New scorer unit tests pass, table-driven per axis, following the `conform_test.go` shape.
- [ ] Per-task axes computed from `segResult`: efficiency (cost/IO) + adaptivity (recoverable-vs-loop).
- [ ] pass^k consistency aggregated per `Shape` intent-cluster; k==1 reported as "no signal".
- [ ] One `ferret quality --session PREFIX` subcommand (NOT two) carrying both blocks; golden text +
      JSON fixtures pass; resolves root like `segments`.
- [ ] Code lives in **`internal/score/`** (Decision 2), NOT `internal/mine` and NOT `internal/conform`;
      `cmd/ferret/quality.go` is a thin shell.
- [ ] `conform.Align`/`Result` **unchanged**, `internal/mine` core unchanged except (Q3-approved) a
      shared σ helper extraction (Decision 2: conform frozen; mine engine not a quality-scoring change).
- [ ] Per-task scores ride the segment/task unit, NOT `Finding`; no new `Finding` kind (Decision 3).
- [ ] TRACE (arXiv:2510.02837) cited in the quality doc-comment; tau-bench (arXiv:2406.12045) cited in
      the consistency doc-comment; discard rationale kept out of code (epic CONVENTION).
- [ ] No persistence, no concurrency, no embed-pipeline touch (bead risk markers; routes generic reviewer).
