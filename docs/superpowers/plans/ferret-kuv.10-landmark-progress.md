# Plan — ferret-kuv.10: Landmark progress scoring (milestones from text, no plans)

status: **proposed** (awaiting dk approval — `requires_plan=true`, `has_design_questions=true`)
date: 2026-06-17
bead: ferret-kuv.10 · parent: ferret-kuv · depends on: kuv.2 (✓), kuv.4 (✓)
shared design: `docs/design/ferret-kuv-deterministic-scorers.md` (this plan stays consistent with all three decisions there)

---

## Goal

Score a task's goal *progress* by how many of its **necessary milestones** the observed
tool-call sequence hit, each weighted by **uniqueness**. Cheap, model-free,
**backtrack-tolerant**: out-of-order and repeated calls still credit a hit. This is the
"progress" axis that complements kuv.4 conformance ("deviation") and kuv.5 TRACE quality.

Unlike conformance, landmark scoring has **no ordered reference plan** and **no alignment
cost** — a milestone is satisfied if the trace touches it *anywhere*, in any order, any
number of times. That backtrack-tolerance is the whole point: it is the progress measure
PDDL/plan-recognition needs but without enumerating a candidate-goal set or a plan.

**The Go deliverable is the deterministic MATCHER + scorer only.** The LLM that extracts
the loose milestone set from the task's stated intent is analyst-side (same engine/analyst
split as kuv.2/kuv.4). Go consumes a *supplied* milestone set; it never extracts one. This
mirrors conform consuming analyst-supplied `Reference` step labels and `Step`/`Noise` tags.

## Context / prior art

- **Algorithm:** landmark-based goal/plan recognition — Pereira, Oren & Meneguzzi,
  "Landmark-based approaches for goal recognition as planning", *Artificial Intelligence*
  vol. 279 (AIJ 2020). The borrowed idea: a *landmark* is a fact that MUST hold on any
  path to the goal; counting achieved landmarks (and weighting rarer ones more) ranks goal
  progress cheaply, without computing full plans. We adapt "fact landmark" → "milestone
  the call sequence must touch", and "uniqueness" → a weight that rewards milestones few
  goals share. Per the epic CONVENTION (nug `cb1c87f041dc`, memory `ferret-cite-algos-in-code`):
  **cite this method + source in the package doc-comment of `internal/score/landmark.go`.**
  Keep the discard rationale (why not PDDL / candidate-goal enumeration) OUT of code — it
  lives here and in the bead, not in comments.
- **Reference pattern:** `internal/conform/conform.go` — the deterministic, LLM-free
  scorer template. Pure function `Align(reference, observed) Result`; analyst sets semantic
  flags (`Step`, `Noise`), Go owns the arithmetic. Landmark mirrors this shape exactly:
  a pure `Score(...)` over a supplied milestone set + observed calls.
- **CLI pattern:** `cmd/ferret/conformance.go` — `conformSpec` (analyst-supplied JSON in),
  `readConformSpec` (file/stdin), `writeConformanceText` / `writeConformanceJSON`,
  `flowerModel` warning helper. Landmark gets the parallel `landmarkSpec` + writers.
- **Wiring:** `cmd/ferret/main.go` — CLI struct (~209 `Conformance` block), usage block
  (~279), dispatch `switch` (~316). Landmark adds one of each, next to conformance.
- **Output sink:** `internal/out/out.go:56` `JSON(w, v)` — reuse for the JSON writer.

## Approach (and how it differs from conform)

conform runs a **Levenshtein-style DP alignment** because order matters and it pays a cost
per skipped/extra call. Landmark needs **none of that machinery** — it is a *set-coverage*
score, not an alignment:

1. Input: a set of `Milestone{ID, Weight, Tools}` (analyst-supplied) + the observed
   `[]ObsCall` for the task (reuse the spine/segments call shape; a milestone is "hit" when
   the trace contains a matching call **anywhere**).
2. For each milestone, deterministically decide **hit / not-hit** by scanning the whole
   observed trace (order-free, repeat-tolerant — this is the backtrack tolerance).
3. Progress = (sum of weights of hit milestones) / (sum of all milestone weights) ∈ [0,1].
   Uniqueness weighting makes hitting a rare/distinctive milestone count for more than a
   generic one.
4. Report per-milestone hit/miss + the weighted progress score; a "missing necessary
   milestone" is the load-bearing signal (the goal cannot have been reached without it),
   parallel to conform's "skipped gate".

No DP table, no move types, no cost — keep it a single pass plus a weighted ratio. Do **not**
extend `conform.Align`/`Result` (design doc Decision 2: conform is frozen for this epic).

## File-by-file changes

### New: `internal/score/landmark.go`
Per design-doc **Decision 2**, reference-free / self-reference per-task scorers live in the
new `internal/score/` package — NOT `internal/conform` (frozen) and NOT a standalone
`internal/landmark/` (the bead NOTES predate the shared doc; see Open Questions Q1).

- Package doc-comment: what landmark scoring is, the engine/analyst split, and the
  **Pereira/Oren/Meneguzzi AIJ 2020 citation** (per CONVENTION).
- `type Milestone struct { ID string; Weight float64; Tools []string /* and/or other
  match predicate — see Q3 */ }` — analyst-supplied; `Weight` carries the uniqueness term.
- Reuse the observed-call shape. **Decision (Q4):** reuse `conform.ObsCall` rather than
  define a parallel type, so analyst tooling emits one call shape across scorers; the
  `Step`/`Noise` fields are simply unused by landmark (or we define a minimal local
  `Call{Index, Tool}` if importing conform is unwanted — flagged Q4).
- `type Hit struct { Milestone string; Hit bool; Weight float64 }`.
- `type Progress struct { Hits []Hit; Score float64; HitCount, Total int; HitWeight,
  TotalWeight float64 }`.
- `func Score(milestones []Milestone, observed []ObsCall) Progress` — the pure scorer:
  single pass, order-free matching, weighted ratio. Same determinism contract as `Align`
  (same inputs → same `Progress`, always). No I/O, no LLM.
- Small matcher helper: `func matches(m Milestone, observed []ObsCall) bool` — order-free,
  repeat-tolerant scan. Keep the match predicate behind one function so Q3 (partial credit
  vs binary) can be tuned in one place.

### New: `cmd/ferret/landmark.go` (mirror of `conformance.go`)
- `errLandmarkBadSpec`, `errLandmarkNoMilestones`, `errLandmarkReadSpec` (parallel to the
  conform errors).
- `type landmarkSpec struct { Task string `json:"task,omitempty"`; Milestones
  []score.Milestone `json:"milestones"`; Observed []conform.ObsCall `json:"observed"` }`
  — keep the JSON envelope **parallel to `conformSpec`** so analyst tooling stays uniform
  (bead risk-marker: api-contract). See Q4 for whether milestones/observed share types.
- `cmdLandmark()` — validate `Format` (`text|json`), read spec, require ≥1 milestone
  (`errLandmarkNoMilestones`), call `score.Score`, dispatch to the text/json writer.
- `readLandmarkSpec(path)` — file-or-stdin, identical structure to `readConformSpec`.
- `writeLandmarkText(w, spec, res)` — `≡` about-lines, task line, milestone count, weighted
  `progress=` score, then per-milestone `✓ hit` / `✗ missed (necessary, never reached)`
  rows, then a roll-up. A "missing necessary milestone" warning helper parallel to
  `flowerModel` (e.g. flag when a high-weight milestone is missed → goal likely not reached).
- `writeLandmarkJSON(w, spec, res)` — `out.JSON` map: `task`, `milestones` (count),
  `observed` (count), `progress`, `hitCount`, `total`, `hitWeight`, `totalWeight`, `hits`.
  **The JSON schema is the NEW contract** — keep it parallel to the conformance JSON shape.

### Edit: `cmd/ferret/main.go`
- CLI struct: add a `Landmark struct { Spec string; Format string }` block with `cmd:""`
  help, right after the `Conformance` block (~212).
- Usage description: add one line after the conformance line (~279):
  `ferret landmark [--spec FILE] [--format text|json]   (reads stdin if no --spec)`.
- Dispatch `switch`: add `case "landmark": err = cmdLandmark()` after the conformance case (~317).

### New tests
- `internal/score/landmark_test.go` — table-driven `Score` cases:
  all-hit → 1.0; none-hit → 0.0; weighted partial (uniqueness weight changes the score);
  **out-of-order** hits still credit (backtrack tolerance); **repeated** calls credit once
  (no double count); empty milestone set / empty observed edge cases.
- `cmd/ferret/landmark_test.go` — mirror `conformance_test.go`: spec read from file+stdin,
  bad-JSON → `errLandmarkBadSpec`, golden **text** output (per-milestone rows + progress +
  missing-milestone warning) and **JSON** shape (required keys present, `progress` correct).

## Test strategy

- **Unit (score):** pure-function table tests prove determinism and the three required
  properties from the bead Done-signal — weighted score, backtrack tolerance (out-of-order),
  repeat tolerance (repeated calls still a single credited hit). Mirror the
  `internal/conform/conform_test.go` table shape (`obs(...)` builder, `countX` helpers).
- **CLI golden (cmd):** text + JSON fixtures, mirroring `conformance_test.go` — file/stdin
  read, error paths, golden strings, JSON-key presence.
- **Gates:** `make check` green (test + race + nilcheck per repo gates). nilaway: return
  non-nil empty slices where the codebase convention requires (see recent `nilcheck` fix
  commit c93398e). 100% package coverage is the bar conform set (kuv.4 / PR#27).
- **Citation check:** confirm the Pereira/Oren/Meneguzzi AIJ 2020 reference is present in the
  `internal/score/landmark.go` package doc-comment (CONVENTION compliance).

## Open Questions (for dk — bead is `has_design_questions=true`)

**Q1 — Package home: `internal/score/` vs `internal/landmark/`.** The bead NOTES (written
2026-06-17, pre-shared-doc) say "new pkg, e.g. `internal/landmark/landmark.go`". The shared
design doc **Decision 2** (the cross-bead resolution) instead puts landmark in the new
`internal/score/` package alongside the other reference-free per-task scorers, and freezes
`conform`. **This plan follows the shared doc (`internal/score/`)** because it is the
authoritative cross-bead resolution and keeps the five new scorers coherent. Confirm — or
override back to a standalone `internal/landmark/`? *(Recommend: `internal/score/`.)*

**Q2 — Uniqueness weighting source.** How is each milestone's `Weight` derived? Options:
(a) **analyst-supplied** per milestone (simplest, matches the engine/analyst split — Go
never computes semantics); (b) inverse corpus frequency computed in Go from `mine.Corpus`;
(c) fixed/uniform (weight = 1, score = simple hit fraction). *(Recommend: (a) analyst-supplied
`Weight` on each `Milestone`; Go stays model-free and the field is already in the struct.
Leave the door open to (b) as a later enrichment.)*

**Q3 — Match scoring: binary hit vs partial credit.** Is a milestone a binary hit/miss, or
can a call partially satisfy it (fractional credit)? And how are backtracking/repeats
credited — repeated hits count once (recommended) or accumulate? *(Recommend: **binary**
hit per milestone, **order-free**, repeats credit once. Partial credit adds a fraction
parameter with no clear semantics yet — defer.)*

**Q4 — Output schema & call-type sharing.** Does the landmark spec/result share the
conformance JSON envelope or stand alone? And should `Observed` reuse `conform.ObsCall` (one
call shape across scorers, `Step`/`Noise` unused) or get a minimal local `Call{Index,Tool}`?
*(Recommend: **stand-alone** result schema kept *parallel* to conformance — its own
`progress`/`hits` fields, not bolted onto `conform.Result`; **reuse `conform.ObsCall`** for
the observed trace so analyst tooling emits one call shape. If importing `conform` into
`score` is undesirable layering, define a tiny local `Call` instead — dk's call.)*

**Q5 — "Necessary milestone" warning threshold.** conform has the `flowerPrecisionCap=0.5`
"invoked != served" warning. Landmark's parallel is a "missing necessary milestone" warning.
What trips it — any missed milestone above a weight threshold, or any miss at all? *(Recommend:
flag when a milestone above a configurable high-weight threshold is missed → "goal likely not
reached"; keep the threshold a single named const like `flowerPrecisionCap`.)*

## Acceptance (from the bead Done-signal)

- [ ] `make check` green (test + race + nilcheck).
- [ ] `internal/score` (or approved pkg) landmark tests pass: milestone-set + observed
      sequence → weighted progress score; backtrack-tolerant (out-of-order / repeated calls
      still credit a hit).
- [ ] New `ferret landmark` subcommand: golden **text** + **json** fixtures pass; reads spec
      from `--spec FILE` or stdin like conformance.
- [ ] JSON result schema is parallel to `conform.Result`'s envelope (api-contract uniformity).
- [ ] Pereira/Oren/Meneguzzi AIJ 2020 algorithm cited in the package doc-comment; discard
      rationale kept out of code (per epic CONVENTION).
- [ ] `conform.Align`/`Result` **unchanged** (design-doc Decision 2: conform frozen).
- [ ] No persistence, no concurrency, no embed-pipeline touch (per bead risk markers).
