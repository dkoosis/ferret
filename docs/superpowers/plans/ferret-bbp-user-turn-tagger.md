# ferret-bbp — User-turn repair/acceptance tagger (implementation plan)

status: **draft** (awaiting dk approval — this bead is `has_design_questions=true`)
date: 2026-06-17
bead: ferret-bbp (P2, feature, `requires_plan=true`, `requires_review=false`)
builds on shared design: `docs/design/ferret-kuv-deterministic-scorers.md` (D1, D2, D3)

---

## Goal

Read intent from the **human's words**, not just the tool-call stream. Add a
transparent, regex/lexical-first tagger over user turns that emits per-turn
**dialogue-act / signal labels** and rolls them up to a per-episode **OUTCOME**
label `{success | repair-heavy | abandoned}`.

Two payoffs, both sharper than today's agent-side burn proxy:

1. **Repair boundary** — when the human types `no, try again` / `not what I meant`,
   that's a higher-precision, cheaper friction marker than the burn/loop-count
   proxy `mine` infers today.
2. **Outcome signal** — acceptance (`great`, `save this`) vs abandonment is the
   only *direct* task-success signal ferret has — the missing PARADISE leg
   (`task_success`).

v1 is **regex/lexical only — no model**. Transparent feature pipeline with a small
auditable taxonomy, each matcher citing its borrowed method. dk = validator.

## Non-goals (fence the scope — `north-star-drift` risk marker)

- **No emotion/affect detector.** Report §4A jargon trap: `kill` / `dead` /
  `crash` / `braindead` are dev style, not affect. Build friction/outcome labels
  only.
- **No ML / LLM in this layer.** Semantic judgement is the analyst's
  (`internal/analyst/**`, ferret-o7v) — its deterministic cheaper sibling lives
  here. Do not entangle the two.
- **No new `Finding` kind**, no churn to `bucketKind`/`mdSections` (D3).
- The **stretch dialogue-act / "move" tag** (correct/reject/clarify/constrain/
  accept) is **deferred to a follow-up bead** unless v1 lands with budget to spare;
  v1 ships the two load-bearing label families (repair, acceptance/outcome). See
  Open Question 4.

---

## Context / prior art

### The three forks the bead surfaced are already resolved by the shared design

The bead NOTES flagged three "should we / either-or" architecture forks. The
`ferret-kuv` shared design doc resolves all three for the whole kuv family. This
plan **adopts those decisions** rather than re-deciding. One of them landed
**opposite** to the bead's own recommendation — flagged as Open Question 1.

| Fork (bead NOTES) | Bead's instinct | Shared-design ruling (authoritative) |
|---|---|---|
| **D1 Text source** | reuse spine `[user]` extraction | **Capture once in `Event.Text`** (new `omitempty` field on `KindPrompt` events) — *opposite of the bead's recommend* |
| **D2 Placement** | new `internal/dialogue` *or* fold into `mine` | **New `internal/score/` package** (shared home for all 5 reference-free per-task scorers) — neither `dialogue` nor `mine` |
| **D3 Finding integration** | extend `mine.Finding` *or* separate surface | **Per-task scores ride the `segResult`/task unit, NOT `Finding`** |

### Where the load-bearing pieces live (verified)

- `internal/event/event.go:9-30` — `Event` is text-free today. D1 adds
  `Text string json:"text,omitempty"`.
- `internal/event/build.go:165-173` — `userLine()` already has `blk.Text` in hand
  (it sets `sawText` from `blk.Text` at :160) but stores none of it. **This is the
  single populate site for D1.**
- `cmd/ferret/spine.go:148-159` — `emitText()` re-reads raw transcript, prints
  `[user] …` capped at `spineTextCap = 4000`. The cap constant to reuse as
  `promptTextCap`.
- `cmd/ferret/segment.go:321,334` — the **segmentation** path *already* carries
  verbatim per-turn user text in `segment.Prompt` (read straight from
  `blocks[i].Text`). This is the natural per-episode unit and overlaps the bbp
  tagger's needs — see Open Question 2.
- `cmd/ferret/segment.go:387-460` — `classifyBoundary` / `affirmations` /
  `controlCommands` are **existing lexical user-turn classifiers**. The `affirmations`
  set (`yes`, `lgtm`, `looks good`, `ship it`, …) is a ready-made acceptance lexicon
  seed — reuse, don't re-invent (see File-by-file).
- `internal/conform/conform.go` + `cmd/ferret/conformance.go` — the **precedent
  shape** for "new `internal/<pkg>` deterministic scorer + thin `cmd/ferret`
  dispatcher + `out` rendering": follow it exactly for `internal/score` + the new
  subcommand.
- `internal/mine/finding.go:14-25` — `Finding` is motif-centric; D3 keeps it
  untouched.

### Cited methods (cite-algos-in-code convention — each matcher cites its source)

- **Repair** (trouble source → initiation → solution): Schegloff, Jefferson &
  Sacks 1977; Hoey 2018 CA review.
- **Dialogue acts / communicative functions** (stretch tag only): ISO 24617-2
  (Bunt 2012/2020 DiAML); SWBD-DAMSL.
- **Outcome frame**: PARADISE — Walker et al. 1997 (task success + dialogue cost
  + satisfaction). The OUTCOME rollup is the `task_success` leg.
- **Follow-up dissatisfaction taxonomy**: arXiv:2204.02659.
- Refs catalogued in `../dk/AHI References.md`; cite beside each matcher in code.

---

## Approach

A pure, deterministic, table-driven lexical tagger. Two stages:

1. **Per-turn labelling.** For each `KindPrompt` event's text (`Event.Text`, D1),
   run an ordered set of regex/lexical matchers and emit zero-or-more **signal
   labels** for that turn. v1 label families:
   - `repair` — `no` / `not that` / `try again` / `I mean` / `you missed` /
     `as I said` / `that's wrong` / `closer` / `not what I meant`. Each matcher
     anchored (whole-turn or leading-cue) to avoid false hits inside longer prose;
     follow the `affirmations` exact-match / `pivotCues` prefix-match discipline
     already in `segment.go`.
   - `acceptance` — `great` / `yes` / `exactly` / `perfect` / `save this` /
     `use that` / `lgtm` / `ship it` (seed from the existing `affirmations` set).

   Matching is **anchored and conservative** (precision over recall): a bare or
   leading cue, normalized (lowercase, trim trailing punctuation), exactly the
   discipline `classifyBoundary` uses. No substring soup.

2. **Per-episode OUTCOME rollup.** Group user turns into episodes (the segment /
   `segResult` task unit — see Open Question 2), then derive one OUTCOME per
   episode from the turn labels:
   - `repair-heavy` — repair signals over a threshold (≥N or ≥ratio of turns).
   - `success` — ends in an acceptance signal, no trailing unresolved repair.
   - `abandoned` — no acceptance and a topic switch / no follow-up
     (deterministic proxy: the episode ends without an acceptance label and the
     next boundary is a genuinely new goal, not a repair continuation).

   Exact thresholds are knobs surfaced in Open Question 3.

Output surfaces on the **per-task unit** (D3), rendered by a new thin subcommand
that mirrors `ferret conformance` / `ferret segments`.

---

## File-by-file changes

> Scope is deliberately small. The blast radius the bead lists (6) is mostly the D1
> `Event` field ripple; the tagger itself is greenfield in one new package.

### 1. `internal/event/event.go` — D1 schema add (shared with kuv.9)

- Add one field to `Event`:
  ```go
  Text string `json:"text,omitempty"` // populated only for KindPrompt; capped at promptTextCap
  ```
- `omitempty` ⇒ tool/shell rows stay byte-identical in the JSONL; only prompt rows
  grow. No migration — artifact is regenerated by `ferret ingest`.

### 2. `internal/event/build.go` — populate `Event.Text`

- In `userLine()` (~:165), capture `blk.Text` for the prompt block, capped at a
  shared `promptTextCap` (reuse the `4000` value; define the constant in `event`
  or share `spineTextCap`). Set it on the emitted `KindPrompt` event.
- This is the only populate site. Verify the existing prompt-detection guard
  (`sawText && !sawResult && !raw.IsMeta`) is unchanged.

### 3. `internal/score/` *(new package — D2)*

- `score.go` — package doc citing the engine/analyst split (LLM-free, per-task,
  reference-free); the `internal/conform` header is the template.
- `repair.go` / `acceptance.go` — the two matcher tables + the per-turn tag fn.
  Each table entry cites its CA / PARADISE source inline. Pure functions over a
  normalized turn string; mirror `segment.go`'s normalization helpers (don't
  duplicate — extract a shared `normTurn` if it pays).
- `outcome.go` — the per-episode rollup: turn labels → `{success|repair-heavy|
  abandoned}`, thresholds as named consts.
- Public surface: a small input type (turns + episode grouping) → an output
  type carrying per-turn labels + per-episode OUTCOME. Keep it I/O-free and
  byte-stable (no maps in output order, no time) — same purity contract as
  `segment.go`.

### 4. `cmd/ferret/<newcmd>.go` *(new thin dispatcher)*

- Mirror `cmd/ferret/conformance.go` / `segment.go`: read events (or segments —
  Open Question 2), call `internal/score`, render `text|json`.
- Likely name `ferret outcomes` or `ferret signals` (Open Question 5). Wire one
  `case` in `main.go`'s dispatch (~:312) and one usage line (~:277).

### 5. `internal/out/` — rendering (only if the new subcommand needs shared md/json)

- Follow `out`'s existing renderers. **Do NOT touch `MDFinding`** (D3) — this is
  not a Finding.

### Explicitly NOT touched

- `internal/analyst/**` (o7v LLM layer), `internal/conform/**`,
  `internal/event/codec*.go`, `mine.Finding` / `bucketKind` / `mdSections`.

---

## Test strategy (done-signal: `make check` green + new table-driven unit tests)

- **`internal/score` unit tests** — table-driven, **one table per matcher family**
  (repair, acceptance) per the bead's done-signal, plus the OUTCOME rollup:
  - Positive cases per cited matcher; **negative cases for the jargon trap**
    (`kill`/`dead`/`braindead`/`crash` MUST NOT tag as anything — explicit
    non-trigger rows, the `non_triggers` discipline).
  - Anchoring cases: `yes, but also do X` is NOT a bare acceptance; `no` leading a
    correction IS a repair; a `no` buried mid-sentence is not.
  - OUTCOME rollup: a hand-built turn sequence → expected
    `{success|repair-heavy|abandoned}` for each class, incl. boundary thresholds.
  - **Determinism**: same input twice → identical output (byte-stable), the
    `segment.go` purity contract.
- **`internal/event` build test** — a `KindPrompt` event now carries `Text`
  (capped); tool/shell events still emit no `text` key (omitempty).
- **`cmd/ferret` golden** — mirror `conformance_test.go` / `segment_test.go`:
  a fixture transcript → expected `text` and `json` outcome rendering.
- Confirm no existing event/JSONL golden breaks from the `omitempty` field add
  (it shouldn't, by omitempty — but the kuv.9 sibling shares this add, coordinate).

---

## Open Questions (for dk — REQUIRED, this bead is `has_design_questions`)

**1. D1 landed opposite to the bead's own recommendation — confirm.**
The bead NOTES recommended *reusing the spine `[user]` extraction* and explicitly
warned "Event is deliberately text-free." The shared design (Decision 1) overrode
that: **capture prompt text in `Event.Text`.** Rationale in the doc: bbp + kuv.9
read uniformly from the tokenized artifact, "tokenization is the product," and the
spine path can later source from `Event.Text` to collapse the two ingest paths.
I'm planning to **Event.Text** (follow the shared design). **Confirm you're good
overriding the bead's instinct, or tell me to keep the tagger on the spine/segment
path instead.**

**2. Episode unit for the OUTCOME rollup — `segResult` segment vs raw event stream?**
`cmd/ferret/segment.go` *already* carries verbatim per-turn text in
`segment.Prompt` and *already* defines the per-task boundary unit (`segResult`).
The shared design (D3) says per-task scores "ride the `segResult`/task model." So
the natural move is: **the tagger consumes segments, not raw events** — reusing the
boundary logic instead of re-deriving episodes. But that couples bbp to the
segmentation subcommand's output shape. Options: (a) score over `segResult`
directly (max reuse, tighter coupling); (b) score over the event stream and do my
own lightweight episode grouping (more independent, some duplication). **Which
coupling do you want?** (Leaning (a) — D3's "rides segResult" points there, and
segment.Prompt already has the text, which would even make the D1 Event.Text add
*unnecessary for bbp specifically* — though kuv.9 still needs it.)

**3. OUTCOME thresholds.** What makes an episode `repair-heavy` — an absolute
repair count (≥2?) or a ratio of repair turns to total user turns? And the
`abandoned` proxy: is "ends without acceptance + next boundary is a new goal"
acceptable as the deterministic v1 definition, or do you want a stricter signal
(e.g. an explicit time-gap / session-end test)? I'll pick conservative defaults if
you don't specify, but these are tuning knobs you'll want to validate.

**4. Stretch dialogue-act / "move" tag — in v1 or defer?** The bead marks the
per-turn move tag (correct/reject/clarify/constrain/accept, ISO 24617-2) as
**stretch**. I'm planning to **defer it to a follow-up bead** and ship only repair
+ acceptance/outcome in v1 (matches "high precision before heavy models" and the
`north-star-drift` fence). **OK to defer, or do you want the move tag in v1?**

**5. Subcommand name + does this even need its own report surface?** New
subcommand `ferret outcomes` vs `ferret signals` vs folding the OUTCOME label into
the existing `ferret segments` output (since it's already the per-task surface).
Folding into `segments` avoids a new subcommand but widens that command's
contract. **Preference?**

---

## Acceptance (from the bead's done-signal)

- [ ] `make check` green (build + lint + nilcheck + race).
- [ ] New `internal/score` (or chosen home) unit tests — table-driven, one per
      CA/PARADISE matcher family + OUTCOME rollup, incl. jargon-trap non-triggers —
      pass.
- [ ] A finding/report can state a per-episode OUTCOME `{success | repair-heavy |
      abandoned}` derived from **user language**, not burn alone.
- [ ] No emotion/affect detector added; no new `Finding` kind; `mine.Finding` /
      `MDFinding` / `bucketKind` / `mdSections` untouched (D3).
- [ ] `Event.Text` add is `omitempty` and breaks no existing JSONL golden.
- [ ] Each matcher cites its borrowed method inline (Schegloff repair; PARADISE
      outcome; ISO 24617-2 if the move tag lands).
