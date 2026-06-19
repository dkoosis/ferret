# Plan — ferret-kuv.5: Quality axes (efficiency/adaptivity per task) + pass^k consistency per intent-cluster

status: **proposed** (awaiting dk approval — `requires_plan=true`, `has_design_questions=true`)
date: 2026-06-19 (supersedes the 2026-06-17 draft; re-anchored on the now-built `internal/score/` substrate)
bead: ferret-kuv.5 · parent: ferret-kuv · depends on: kuv.2 (✓ segmentation), kuv.4 (✓ conformance), score-boot (✓), kuv.8 (✓ outcome)
shared design: `docs/design/ferret-kuv-deterministic-scorers.md` — D1/D2/D3 are authoritative.

---

## What changed since the 2026-06-17 draft (read this first)

The substrate the original plan only proposed has since **shipped**, which resolves the
biggest open question and changes the type design:

- **`internal/score/` exists** with the package doc-comment already written (`doc.go`), and
  the deterministic segmenter was **relocated out of `cmd/ferret` into it** (Decision 2's
  "move reusable scoring logic out of cmd" is done). So `score.Result` and `score.Segment`
  are **native types in this package** — the original Q6 "use a cmd-side adapter / defer the
  relocation" is **moot**. No adapter; the scorer takes `score.Segment` directly.
- **`outcome.go` (kuv.8) shipped** and is the canonical leaf-scorer pattern to mirror: a pure
  function over `res.Segments[*].Shape`, annotating each segment **in place** (`*Outcome`, nil
  = silence), byte-stable, prior-art cited in the doc-comment, reusing `internal/lens`.
  → **kuv.5 mirrors `LabelOutcomes` exactly**, not `conform.Align`.
- `Segment` already carries every input the axes need: `InBytes`, `OutBytes`,
  `FirstCall`/`LastCall` (→ call count), `Shape` (the recurrence key), and now `Outcome`
  (the weak "shipped" flag the adaptivity axis can lean on).

## Goal

Two reference-free, LLM-free deterministic scorers over `score.Result`:

1. **Per-task quality axes** — `Efficiency` and `Adaptivity` per `Segment`, from the task's own
   cost/IO (`InBytes`/`OutBytes`, call count) and its loop/recovery signal. TRACE framing:
   efficiency = *friction-free*, adaptivity = *recoverable*.
2. **pass^k consistency per intent-cluster** — group same-`Shape` tasks into clusters; score how
   *consistently* the agent handles a recurring shape (low spread = predictable = "delightful",
   per tau-bench pass^k). Reuse the `mine/surprise` σ machinery, do not reinvent a variance.

Both are pure functions of the segmentation. Semantic intent-clustering (by a stated-intent
sentence rather than by `Shape`) is analyst-side and out of scope (engine/analyst split).

## Context / prior art (citations are CONVENTION — keep in doc-comments, rationale out of code)

- **Quality axes — TRACE** (arXiv:2510.02837). Reference-free quality axes (efficiency /
  hallucination / adaptivity) instead of only task-success. We adopt the two ferret can compute
  deterministically — **efficiency** (cost/IO per unit of useful work) and **adaptivity**
  (recovered-from-churn vs stuck-in-a-loop) — and **drop hallucination** (needs semantic
  grounding ferret lacks; analyst territory). Cite TRACE in `internal/score/quality.go`.
- **pass^k consistency — tau-bench** (arXiv:2406.12045). pass^k = probability of succeeding on
  *all k* attempts at the same task — reliability, not average quality. We adapt it: k tasks
  sharing a `Shape` are k attempts at a recurring shape; consistency = how tightly their per-task
  quality clusters. Cite tau-bench in the consistency scorer's doc-comment.
- **Predictability metric — `internal/mine/surprise.go`** (`ScoreSurprise`, `FrictionCut`):
  existing n-gram surprisal + σ-above-mean cut. The bead risk-marker says **extend this, don't add
  a parallel variance** — reuse its mean/σ for the consistency spread (Q3).
- **Shipped leaf-scorer template — `internal/score/outcome.go`** (kuv.8): the in-place,
  Shape-driven, byte-stable pattern this bead copies.

## Approach

### One subcommand, two scopes by `--session` (Q5, resolved: consistency is corpus-wide)

The two halves want **different scopes**, and that is forced by arithmetic, not preference:

- **Per-task axes** are meaningful per session — `ferret quality --session PREFIX` segments one
  session and emits the per-task efficiency/adaptivity block.
- **pass^k consistency** needs k *attempts at the same shape*, and a single session almost never
  repeats an exact `Shape` — single-session clusters are k==1 everywhere and the metric is
  structurally empty. So consistency is computed **corpus-wide**: bare `ferret quality` (no
  `--session`) walks all transcripts via `SegmentSource`, clusters same-`Shape` tasks across
  sessions, and emits the consistency block. This is exactly the scope-by-`--session` pattern
  `candidates` already uses.

So: `--session PREFIX` → per-task axes for that session; no `--session` → corpus consistency
report (which also carries each task's axes, since clustering needs them). The corpus walk is in
**v1**, not deferred — without it the consistency half of the bead is decorative.

### Per-task axes (deterministic, in place — mirror `LabelOutcomes`)

For each `Segment` (skip the synthetic preamble `Index==0` and segments that own no calls,
`FirstCall<0`):

- **Efficiency** — **calls per distinct `Shape` token** (Q1, resolved). `efficiency = f(call_count /
  distinct_shape_tokens)`, squashed to [0,1] (lower ratio = tighter = higher efficiency). This is the
  **thrash** signal: repeated calls that add no new *kind* of work (the read⇄search loop) — the
  dominant friction in this corpus. **Bytes are NOT folded into the axis**: `InBytes`/`OutBytes` are
  heavy-tailed (one 2 MB read would crater an otherwise-clean task) so they stay a **reported cost
  column** beside efficiency, where bloat is visible without corrupting the [0,1] score. Call count =
  `LastCall-FirstCall+1`.
- **Adaptivity** — recovered-from-churn vs stuck-in-an-unabandoned-loop (Q2, resolved:
  reference-free). Detect a repeated `Shape` sub-sequence (a loop) and score by **whether it
  resolves**: a loop the task moves *past* (into new tokens or a terminal action) scores high;
  one that runs to the segment boundary scores low. **Resolution-detection is the core, not the
  loop itself** — a healthy TDD loop (`Edit→go_test` ×3 → `git_commit`) and flailing look identical
  *as loops*; only resolution separates them, so `Segment.Outcome` (the task shipped) is a
  **load-bearing confirming signal**, not a cosmetic nudge — without it, every TDD loop scores as
  friction. ∈ [0,1]. Reference-free, so `ferret quality` runs on every session with no analyst input.

This is reference-free: `ferret quality` runs with **zero analyst input**, like `segments`.
conform's recoverable-vs-loop localization needs an analyst reference, so it is *not* required
here (optional enrichment when a conformance spec is also supplied — deferred to ferret-f57; Q2).

### pass^k consistency per intent-cluster (deterministic)

- **Cluster formation (corpus-wide)** — group `Segment`s **across all sessions** by a canonical
  join of their ordered `Shape` slice (exact ordered-token equality, Q4 resolved; near-shape is a
  later enrichment). Empty-`Shape` tasks excluded. Same recurrence key kuv.12 candidates cluster on —
  purely deterministic, no analyst sentence (the semantic alternative is out of scope).
- **Consistency per cluster** — for k same-shape tasks, score how tightly their per-task **cost**
  (`InBytes+OutBytes`) clusters, as `1 − coefficient-of-variation`. **Reuse `mine`'s mean/σ** (Q3).
  Low spread = high consistency = predictable. Report per cluster: shape key, k, consistency, spread.
  A k==1 cluster has no consistency signal — report as a sentinel, **not** a spurious 1.0.
  - ‡ **Plan modification (ratified by dk 2026-06-19): consistency is computed over COST, not
    efficiency.** Under exact-`Shape` clustering the per-task axes are *shape-determined* — same
    ordered token sequence ⇒ identical efficiency/adaptivity — so their within-cluster spread is ~0
    and consistency-over-axes collapses to a decorative 1.0. Cost genuinely varies across same-shape
    attempts (file sizes differ per run), so cost-CV is the real cross-attempt reliability signal.
    Supersedes the earlier "consistency over efficiency" framing wherever this doc still says it.

### Determinism contract

Same `score.Result` → same report. No time, no map-order output, no randomness (the package
contract in `doc.go`). Cluster iteration sorted by the stable shape-join key before render.

## File-by-file changes

### New: `internal/score/quality.go`
- **Doc-comment** cites **TRACE (arXiv:2510.02837)**; discard rationale (no hallucination axis)
  stays out of code (it lives here + in the bead). The `package score` comment already exists in
  `doc.go` — do **not** redeclare it.
- Types:
  - `type Axes struct { Efficiency, Adaptivity float64 }` — both ∈ [0,1].
  - Add `Axes *Axes \`json:"axes,omitempty"\`` to `Segment` (mirrors the `*Outcome` field — pointer
    + omitempty so untouched segments and the JSON stay clean). *(Alternative: a separate
    `[]TaskQuality` return instead of mutating `Segment`; recommend the in-place field for
    consistency with kuv.8 — Q6.)*
  - `func ScoreAxes(res *Result)` — annotate every scored segment in place (the `LabelOutcomes`
    shape). Internally `func axesFor(seg Segment) Axes` is the single pure scorer so Q1/Q2 formulas
    tune in one place.
- **Consistency doc-comment** cites **tau-bench (arXiv:2406.12045)**:
  - `type Cluster struct { Key string; Shape []string; K int; Consistency, Spread float64 }`.
  - `func ClusterByShape(res Result) []Cluster` — deterministic grouping, sorted by `Key`.
  - `func consistency(effs []float64) (score, spread float64)` — **reuses the `mine` σ helper**
    (Q3): factor `FrictionCut`'s mean/σ into a shared helper both packages call.

### Edit: `internal/mine/surprise.go` (Q3-gated, minimal)
Extract the mean/σ math into a small exported/shared helper so `score` reuses it (no parallel
variance). No behaviour change to `mine` outputs.

### New: `cmd/ferret/quality.go` (input mirrors `segments`, render mirrors `conformance`/`retrieval`)
- `cmdQuality()` — validate `Format` (`text|json`); `--session` is **optional** (scope-by-presence,
  like `candidates`): **with** `--session`, segment that one session and emit per-task axes; **without**,
  walk all transcripts (`SegmentSource` over the corpus), `ScoreAxes` each, `ClusterByShape` across
  sessions, and emit the corpus consistency block. Resolve root like `cmdSegments`.
- `writeQualityText` — `≡` about-lines (efficiency/adaptivity meaning + pass^k=predictable-per-shape),
  per-task rows (`task N  eff=… adapt=…  cost=… shape=…`, reusing `humanBytes`), per-cluster rows
  (`shape=… k=N consistency=… spread=…`), a roll-up, and a warning helper parallel to `flowerModel`
  (flag a high-spread cluster → "inconsistent handling of a recurring task").
- `writeQualityJSON` — `out.JSON` with `session`, `project`, a `tasks` array (`index`, `efficiency`,
  `adaptivity`, `shape`, `cost`) and a `clusters` array (`shape`, `k`, `consistency`, `spread`).
  Field names parallel the conformance/segments JSON style. **This JSON is the new contract.**

### Edit: `cmd/ferret/main.go`
- CLI struct: a `Quality` block after `Retrieval` (~227) — `Session` (optional), `Root`, `Format`.
- Usage line + dispatch `case "quality": err = cmdQuality()` (~340, after `retrieval`).

### New tests
- `internal/score/quality_test.go` — table-driven (mirror `outcome_test.go` / `conform_test.go`):
  `axesFor` (tight→high eff; high cost/few tokens→low eff; resolved repeat→higher adapt than a
  boundary-running loop; bounds [0,1]; empty-Shape/zero-cost edges); `ClusterByShape` (identical
  shapes group, different split, input order doesn't change sorted output, empty-Shape excluded);
  `consistency` (k identical→max/zero spread; one outlier widens; k==1→sentinel not 1.0).
- `cmd/ferret/quality_test.go` — mirror `conformance_test.go`/`retrieval_test.go`: small synthetic
  transcript → golden text (rows + warning) + JSON shape (keys present, stable cluster order).

## Test strategy

- **Unit:** pure-function tables prove the determinism contract + the bead axes. Determinism
  asserted by shuffling task input order → identical sorted output.
- **CLI golden:** text + JSON fixtures; root resolution; missing-`--session` error; stable order.
- **Gates:** `make check` green (test + race + nilcheck). Return non-nil empty slices (nilaway).
  Match the package-coverage bar conform set in kuv.4.
- **Citation check:** TRACE in the quality doc-comment, tau-bench in the consistency doc-comment;
  discard rationale out of code.

## Open Questions (for dk — `has_design_questions=true`)

Resolved by the shipped substrate (was Q6): **package home + adapter + relocation** — `internal/score/`
exists, the segmenter is relocated, the doc-comment is written. No adapter; native `score.Segment`.

**Q1 — efficiency formula.** *(RESOLVED 2026-06-19 — dk: thrash is the axis, bloat is reported.)*
Efficiency = `call_count / distinct_shape_tokens`, squashed to [0,1] — the thrash signal (repeated
calls adding no new kind of work). Bytes are **not** folded in: heavy-tailed, so `InBytes`/`OutBytes`
ride a separate reported cost column, not the axis. No corpus normalisation in v1.

**Q2 — adaptivity source.** *(RESOLVED 2026-06-19 — dk: reference-free.)* `Shape` self-repetition
with **resolution-detection as the core** signal and `Segment.Outcome` as a **load-bearing**
confirmer (TDD loops are indistinguishable from flailing except by resolution). Runs on every
session, no analyst input. conform-enriched adaptivity is a deferred follow-up → **ferret-f57**.

**Q3 — how literally to reuse `mine/surprise` σ.** *(Recommend: factor the mean/σ helper out of
`FrictionCut` into a shared func both packages call — satisfies "don't reinvent the variance"
without coupling `score` to the whole corpus model. If dk wants consistency to literally be
surprisal spread, run `ScoreSurprise` per task and aggregate.)*

**Q4 — cluster key: `Shape` exact vs near-shape.** *(RESOLVED 2026-06-19 — dk: exact.)* Exact
ordered-`Shape` equality for v1; near-shape (edit-distance/prefix) a later enrichment. Semantic
intent-clustering stays analyst-side, out of scope.

**Q5 — single-session vs corpus pass^k.** *(RESOLVED 2026-06-19 — dk: consistency is corpus-wide,
in v1.)* Per-task axes run per `--session`; consistency is computed across all transcripts (bare
`ferret quality`, the `SegmentSource` corpus walk, scope-by-`--session` like `candidates`). Single
session is structurally k==1, so the corpus walk is required, not deferred.

**Q6 — axes in place on `Segment` vs a separate return.** *(Recommend: in-place `*Axes` field,
matching kuv.8's `*Outcome` — one consistent annotation pattern. Flag if dk prefers Segment stay
minimal and axes ride a parallel slice.)*

**Q7 — borrowed-framing verification (kuv.11 precedent).** The TRACE and tau-bench mappings are
adaptations by analogy. *(Recommend: cite as inspiration in doc-comments — the arithmetic is ours
and deterministic — and note in the bead that the framing is an adaptation. A separate verification
bead only if dk wants the mapping defended.)*

## Acceptance

- [ ] `make check` green (test + race + nilcheck).
- [ ] Per-task `Efficiency`/`Adaptivity` ∈ [0,1] computed from `score.Segment` (cost/IO + loop/recovery).
- [ ] pass^k consistency per `Shape` cluster; k==1 reported as "no signal", not 1.0.
- [ ] One `ferret quality` subcommand, two scopes: `--session PREFIX` → per-task axes;
      no `--session` → corpus-wide consistency (the `SegmentSource` walk). Golden text + JSON pass.
- [ ] pass^k consistency clusters same-`Shape` tasks **across sessions** (corpus walk in v1, not deferred).
- [ ] Code in `internal/score/` (mirrors `outcome.go`); `cmd/ferret/quality.go` a thin shell.
- [ ] `conform.Align`/`Result` unchanged; `internal/mine` unchanged except the (Q3-approved) shared σ helper.
- [ ] Per-task scores ride the `Segment` unit, not `Finding`; no new `Finding` kind (Decision 3).
- [ ] TRACE (2510.02837) + tau-bench (2406.12045) cited; discard rationale out of code.
- [ ] No persistence, no concurrency, no embed-pipeline touch.
