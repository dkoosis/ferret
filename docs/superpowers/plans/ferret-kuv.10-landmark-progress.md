# Plan — ferret-kuv.10: Landmark progress scoring (milestones from text, no plans)

status: **proposed** (awaiting dk approval — `requires_plan=true`, `has_design_questions=true`)
date: 2026-06-19 (supersedes the 2026-06-17 draft; re-anchored on the now-built `internal/score/` substrate)
bead: ferret-kuv.10 · parent: ferret-kuv · depends on: kuv.2 (✓), kuv.4 (✓), score-boot (✓), kuv.8 (✓ outcome)
shared design: `docs/design/ferret-kuv-deterministic-scorers.md` (consistent with all three decisions).

---

## What changed since the 2026-06-17 draft (read this first)

The substrate shipped, which **resolves two open questions and reverses one type decision**:

- **Q1 (package home) resolved → `internal/score/`.** `doc.go` is written and states this package
  is the home for reference-free / self-reference per-task scorers. No standalone `internal/landmark/`.
- **Q4 (reuse `conform.ObsCall`?) resolved and REVERSED.** `doc.go` records an explicit decision:
  scorers here do **NOT** reuse `conform.ObsCall` (it is bound to conformance replay — carries an
  analyst plan `Step` + `Noise` flag, and conform is frozen). The per-task input is the **`Segment`**,
  which already carries the ordered tool-shape tokens (`Shape`). → **Landmarks match against
  `Segment.Shape` tokens**, exactly like `outcome.go`'s `terminalAction(shape)` does. This is simpler
  than the old "share a call type" plan and removes the `score`→`conform` import question entirely.
- **`outcome.go` (kuv.8) is the template.** A pure function scanning `Shape` tokens, order-free,
  byte-stable — landmark is the same shape with a weighted set-coverage instead of last-match.

## Goal

Score a task's goal *progress* by how many of its **necessary milestones** the observed call
sequence hit, each weighted by **uniqueness**. Cheap, model-free, **backtrack-tolerant**:
out-of-order and repeated calls still credit a hit. This is the "progress" axis that complements
kuv.4 conformance ("deviation") and kuv.5 TRACE quality.

Unlike conformance there is **no ordered reference plan and no alignment cost** — a milestone is
satisfied if the trace touches it *anywhere*, any order, any number of times. That backtrack
tolerance is the point: the progress measure PDDL/plan-recognition needs, without enumerating a
candidate-goal set or a plan.

**The Go deliverable is the deterministic MATCHER + scorer only.** The LLM that extracts the loose
milestone set from the task's stated intent is analyst-side (engine/analyst split). Go consumes a
*supplied* milestone set; it never extracts one — mirroring conform consuming analyst-supplied
`Reference` step labels.

## Context / prior art (citation is CONVENTION — doc-comment only, rationale out of code)

- **Algorithm:** landmark-based goal/plan recognition — Pereira, Oren & Meneguzzi, "Landmark-based
  approaches for goal recognition as planning", *Artificial Intelligence* vol. 279 (AIJ 2020). A
  *landmark* is a fact that MUST hold on any path to the goal; counting achieved landmarks (rarer
  ones weighted more) ranks progress cheaply, no full plans. We adapt "fact landmark" → "milestone
  the call sequence must touch", "uniqueness" → a weight rewarding milestones few goals share. Cite
  this in the `internal/score/landmark.go` package... (doc-comment already exists in `doc.go` — put
  the citation in the **function/section doc-comment**, do not redeclare `package score`).
- **Shipped template — `internal/score/outcome.go`** (kuv.8): pure `Shape`-token scan, in-place,
  byte-stable, reuses a classifier. Landmark is the same shape, weighted-coverage instead of last-match.
- **CLI template — `cmd/ferret/conformance.go`**: `conformSpec` (analyst JSON in), `readConformSpec`
  (file/stdin), `writeConformanceText`/`writeConformanceJSON`, `flowerModel` warning. Landmark gets
  the parallel `landmarkSpec` + writers.

## Approach (and how it differs from conform)

conform runs a Levenshtein-style DP alignment because order matters and it pays per skipped/extra
call. Landmark needs **none of that** — it is **set-coverage**, not alignment:

1. Input: a set of `Milestone{ID, Weight, Tools}` + the observed `Shape` tokens for the task. The
   **analyst supplies the semantics** — `ID` and `Tools` (the `Shape` tokens that satisfy it, e.g.
   `["Read"]`, `["sh:git_commit"]`, `["mcp:trixi.get_nug"]`). The **engine supplies the uniqueness
   `Weight`**, computed from corpus frequency (Q2, resolved — see below), not asserted by the analyst.
2. For each milestone, scan the **whole** observed `Shape` for a matching token — order-free,
   repeat-tolerant (this *is* the backtrack tolerance). Hit = matched anywhere.
3. Progress = Σ(weights of hit milestones) / Σ(all weights) ∈ [0,1]. **Uniqueness weighting is
   measured**: a milestone whose tools are rare across the corpus gets a higher weight than a
   generic one (inverse-frequency, the faithful form of the landmark "uniqueness" term).
4. Report per-milestone hit/miss + weighted progress; a **missing high-weight milestone** is the
   load-bearing signal (the goal can't have been reached without it) — parallel to conform's
   "skipped gate".

No DP table, no move types, no cost — one pass + a weighted ratio. Do **not** extend
`conform.Align`/`Result` (Decision 2: conform frozen).

## File-by-file changes

### New: `internal/score/landmark.go`
- Section/func doc-comment cites **Pereira/Oren/Meneguzzi AIJ 2020** (CONVENTION); discard rationale
  (why not PDDL / candidate-goal enumeration) stays here + in the bead, out of code.
- Types:
  - `type Milestone struct { ID string \`json:"id"\`; Tools []string \`json:"tools"\`; Weight float64 \`json:"weight,omitempty"\` }`
    — analyst supplies `ID` + `Tools` (semantics); `Weight` is engine-filled from corpus frequency
    (Q2). `omitempty` so an analyst spec needn't carry it; an explicit non-zero value is honored as
    an override.
  - `type Hit struct { Milestone string \`json:"milestone"\`; Hit bool \`json:"hit"\`; Weight float64 \`json:"weight"\` }`.
  - `type Progress struct { Hits []Hit \`json:"hits"\`; Score float64 \`json:"score"\`; HitCount, Total int; HitWeight, TotalWeight float64 }`.
  - `func ScoreLandmarks(milestones []Milestone, shape []string) Progress` — the **pure** scorer:
    single pass, order-free match, weighted ratio over the `Weight`s already on the milestones. Same
    byte-stable contract as `outcome.go`. No I/O, no corpus — keeps the scorer trivially testable.
  - `func WeighByCorpus(milestones []Milestone, corpus *mine.Corpus)` — fills each `Weight` from the
    **inverse frequency** of its `Tools` across the corpus (rarer tools ⇒ higher weight), in place.
    This is the only corpus-touching part; isolating it keeps `ScoreLandmarks` pure and the
    frequency arithmetic in the engine layer (same posture as `mine/surprise.go`). A milestone with a
    caller-set `Weight` is left untouched (override).
  - `func milestoneHit(m Milestone, shape []string) bool` — order-free, repeat-tolerant token scan
    (mirror `terminalAction`'s loop). One function so Q3 (binary vs partial) tunes in one place.
- Naming: `ScoreLandmarks`, not `Score` — `score.Score` is too generic in a package that will hold
  several scorers.

### New: `cmd/ferret/landmark.go` (mirror of `conformance.go`)
- Errors `errLandmarkBadSpec`, `errLandmarkNoMilestones`, `errLandmarkReadSpec` (parallel to conform).
- `type landmarkSpec struct { Task string \`json:"task,omitempty"\`; Milestones []score.Milestone \`json:"milestones"\`; Shape []string \`json:"shape"\` }`
  — JSON envelope parallel to `conformSpec`. **Observed is supplied as `Shape` tokens** in the spec
  (Q4-new resolved: spec-supplied for v1; a `--session`-derived mode is a deliberate follow-on once a
  milestone-set library exists to map onto segmented tasks), not `conform.ObsCall` (per `doc.go`).
- `cmdLandmark()` — validate `Format` (`text|json`); read spec; require ≥1 milestone
  (`errLandmarkNoMilestones`); load the corpus and `score.WeighByCorpus(spec.Milestones, corpus)` to
  fill uniqueness weights (skipped for any milestone with a caller-set override); then
  `score.ScoreLandmarks(spec.Milestones, spec.Shape)`; dispatch writer. (Corpus load mirrors how the
  mine-backed subcommands resolve `~/.ferret/events.jsonl`.)
- `readLandmarkSpec(path)` — file-or-stdin, identical structure to `readConformSpec`.
- `writeLandmarkText` — `≡` about-lines, task line, milestone count, weighted `progress=`, per-milestone
  `✓ hit` / `✗ missed (necessary, never reached)` rows, roll-up, and a "missing necessary milestone"
  warning helper parallel to `flowerModel` (trips when a high-weight milestone is missed → "goal likely
  not reached"; threshold a named const, Q5).
- `writeLandmarkJSON` — `out.JSON`: `task`, `milestones` (count), `observed` (count), `progress`,
  `hitCount`, `total`, `hitWeight`, `totalWeight`, `hits`. Parallel to the conformance JSON. **New contract.**

### Edit: `cmd/ferret/main.go`
- CLI struct: a `Landmark struct { Spec string; Format string }` block with `cmd:""` help, after the
  `Conformance` block (~218).
- Usage line `ferret landmark [--spec FILE] [--format text|json]   (reads stdin if no --spec)` and
  dispatch `case "landmark": err = cmdLandmark()` (~340, by the conformance case).

### New tests
- `internal/score/landmark_test.go` — table-driven `ScoreLandmarks`: all-hit → 1.0; none → 0.0;
  weighted partial (uniqueness weight changes the score); **out-of-order** still credits;
  **repeated** tokens credit once (no double count); empty milestone set / empty `Shape` edges.
- `cmd/ferret/landmark_test.go` — mirror `conformance_test.go`: spec from file + stdin, bad-JSON →
  `errLandmarkBadSpec`, golden text (rows + progress + missing-milestone warning) + JSON shape.

## Test strategy

- **Unit:** pure-function tables prove determinism + the three bead properties — weighted score,
  backtrack tolerance (out-of-order), repeat tolerance (repeats = single credited hit). Mirror the
  `outcome_test.go`/`conform_test.go` table shape.
- **CLI golden:** text + JSON fixtures; file/stdin read; error paths; key presence; `progress` correct.
- **Gates:** `make check` green (test + race + nilcheck). Non-nil empty slices (nilaway). Match the
  package-coverage bar conform set in kuv.4.
- **Citation check:** Pereira/Oren/Meneguzzi AIJ 2020 present in the landmark doc-comment.

## Open Questions (for dk — `has_design_questions=true`)

Resolved by the shipped substrate: **Q1 package home** → `internal/score/`; **Q4 call type** →
match on `Segment.Shape` tokens, do **not** reuse `conform.ObsCall` (per `doc.go`).

**Q2 — uniqueness weight source.** *(RESOLVED 2026-06-19 — dk: measured.)* Inverse corpus frequency
from `mine.Corpus` (`WeighByCorpus`), the faithful form of landmark "uniqueness" — rarer tools weigh
more. This also keeps the split clean: the analyst names which tools make a milestone (semantics),
the engine computes the weight from frequency (deterministic, like `surprise.go`). A caller-set
`Weight` is honored as an override. Asserted/uniform weighting is not the v1 path.

**Q3 — match scoring: binary vs partial credit.** *(Recommend: binary hit per milestone, order-free,
repeats credit once. Partial credit has no clear semantics yet — defer.)*

**Q4-new — how is `observed` supplied?** *(RESOLVED 2026-06-19 — dk: spec-supplied for v1.)* The spec
carries `Shape` tokens directly (parallel to conformance; keeps `landmark` a pure scorer). A
`--session`-derived mode is a deliberate follow-on, not v1: it is an orchestration layer *on top of*
this scorer + a milestone-set library (mapping task→goal→milestones is itself semantic/analyst work),
so the pure scorer is the necessary substrate either way. Follow-ons: **ferret-vy7** (milestone-set
library) blocks **ferret-afm** (the `--session` scoring mode).

**Q5 — "missing necessary milestone" warning threshold.** Parallel to conform's
`flowerPrecisionCap=0.5`. *(Recommend: flag when a milestone above a configurable high-weight const is
missed → "goal likely not reached"; keep the threshold a single named const.)*

## Acceptance

- [ ] `make check` green (test + race + nilcheck).
- [ ] `internal/score` landmark tests pass: milestone set + observed `Shape` → weighted progress;
      backtrack-tolerant (out-of-order / repeated tokens still credit a single hit).
- [ ] New `ferret landmark` subcommand: golden text + JSON fixtures pass; spec from `--spec FILE` or stdin.
- [ ] Uniqueness weight is measured: `WeighByCorpus` fills `Weight` from inverse corpus frequency
      (caller override honored); `ScoreLandmarks` stays a pure, corpus-free function (corpus touch isolated).
- [ ] JSON result schema parallel to `conform.Result`'s envelope (api-contract uniformity).
- [ ] Matches against `Segment.Shape` tokens; does **not** import/reuse `conform.ObsCall` (per `doc.go`).
- [ ] Pereira/Oren/Meneguzzi AIJ 2020 cited in the doc-comment; discard rationale out of code.
- [ ] `conform.Align`/`Result` unchanged (Decision 2: conform frozen).
- [ ] No persistence, no concurrency, no embed-pipeline touch.
