# ferret — search-quality metrics for `get_nug`: spec

status: **proposed** (awaiting dk approval)
date: 2026-06-18
revised: 2026-06-19 (ferret-d28 — §D0 corrected: per-hit per-query score IS in the
envelope and captured; R3b/NQC promoted to live Phase 1 behind a score-availability gate;
implementation map reconciled with merged sq.d0/sq.1 code)
scope: defines the metric set ferret computes over `get_nug` (trixi search) episodes,
so the search algorithm has a northstar to iterate against. Splits the metrics into a
deterministic Phase 1 (ships without an LLM) and an analyst-judged Phase 2 (offline
iteration engine).

## Why this doc exists

We want a quality signal for `get_nug` that Claude can optimize toward across algorithm
iterations. ferret already parses the four things a search-quality metric needs out of
the Claude Code log: the **human prompt**, **Claude's query** to `get_nug`, the
**results**, and the **following actions**. The "following actions" are implicit
relevance feedback — the signal classic IR pays human judges to produce — and we get it
free on every call. This spec turns that stream into scores.

Two framing commitments inherited from the kuv epic, unchanged here:

- **Engine/analyst split** (kuv design doc, Decision 2). ferret-side code is
  deterministic and LLM-free; semantic judgement is the analyst's, validated by dk.
  Every metric below is tagged **[det]** (ferret computes it) or **[analyst]** (LLM
  judge, dk-validated sample).
- **The consumer is Claude, not a human.** Claude reads all *k* results in-context
  before acting, so the position/click-bias machinery that dominates web-search metrics
  mostly does not apply: *which result got used* is a clean relevance label here, not a
  biased click. This is the property the whole design leans on.

The two-hop episode model in `internal/dialogue/attribute.go` is already exactly the
right decomposition and the metrics map onto it:

```
human turn →[hop 1: interpret]→ Claude's get_nug query →[hop 2: execute]→ results → human reaction
            └── QUERY QUALITY (HopInterp) ──┘          └── RESULT QUALITY (HopRetrieval) ──┘
```

---

## Unit of measurement: the retrieval episode

One **retrieval episode** = one **query-mode** `get_nug` call (or a self-requery chain of
them serving the same intent) plus the segment of following actions up to the next task
boundary or human turn. **By-id fetches are excluded** — they carry no query intent and
no ranking, so they are not retrieval episodes; in the current corpus ~55% of `get_nug`
calls are by-id (191/350), so the episode filter is load-bearing, not cosmetic. The
natural container is the existing per-task `segment` from `cmd/ferret/segment.go`; an
episode is a query-mode-`get_nug`-rooted span inside it. Outcome rolls up via
`dialogue.Episode.Classify`.

### Decision 0: what actually survives ingest (corrected against the real envelope)

This was checked against the real corpus — it is **not** a free checklist. The metrics
want a `get_nug` call's **query argument** and **returned nug ids + scores in rank
order**. What the CC log actually carries on a query-mode call:

| Need | In the CC log? | Consequence |
|---|---|---|
| query string | ✅ | Q1–Q4 live |
| result **ids** + rank | ✅ — rank = result **order** | R1, R2 (MRR), R7, C1/C2 live |
| per-hit **retrieval score** | ✅ | each hit carries a per-query `score` (a **descending per-query match score**, distinct from the static `meta.trixiQuality`); verified across corpus — R3b/NQC live |

> **Reframing (we were wrong).** An earlier draft asserted the result envelope carried
> no per-query score — only `id`/`name`/`body`/`meta.trixiQuality` (a static per-nug
> quality). That was wrong. ferret-sq.d0 (PR#36) checked the actual `get_nug` result
> array across the corpus and found a per-hit `score` field — a real, descending
> per-query match score, *distinct* from `meta.trixiQuality`. So the score column flips
> ❌→✅ and the R3b/NQC corner is no longer blocked on any trixi-side change. The one
> remaining caveat is **coverage, not capture**: see the score-availability gate below.

Capture the query + ranked hits the d01 way (capture once, `omitempty`, no migration).
This is **shipped**: `parseNugHits` in `internal/event/build.go` reads the per-hit
`score` straight out of the result array; `Event.Query`/`Event.Results` are populated on
query-mode `get_nug` calls (`build.go` call site, gated on `evs[0].Query != ""`).

```go
// Event additions, populated only on KindTool query-mode get_nug calls (shipped, PR#36):
Query   string    `json:"qq,omitempty"` // the search query Claude sent to get_nug
Results []NugHit  `json:"rs,omitempty"` // returned nug hits, in rank (result) order
```

```go
// NugHit as shipped in internal/event/event.go. Score is the per-query match
// score read straight from the result envelope (descending per-query, distinct
// from the static meta.trixiQuality). Rank comes from slice order, so all
// rank-keyed metrics work regardless; Score additionally unblocks R3b/NQC.
type NugHit struct {
	ID    string  `json:"id"`
	Score float64 `json:"sc,omitempty"` // 0/omitted only when a hit lacks a score
}
```

**Score-availability gate (the real caveat).** Score is captured as-is, and a hit that
omits one lands at zero (`omitempty`). In a query-mode sample, the per-hit `score` was
present on only **~a third** of results. NQC (R3b, below) reads the *distribution* over a
result set, so it needs a **complete** set of scores to be meaningful — a set where only
some hits scored would skew the variance. Therefore **gate R3b per episode on score
availability**: compute NQC only when every hit in the result set carries a non-zero
score; skip the episode (do not impute zeros) otherwise, and report NQC coverage (the
fraction of episodes that qualified) alongside the metric so a low denominator is visible.

Ranked ids unlock everything rank- or set-keyed (RU, Q1/Q2, R1, R2, R7, C1/C2). The
**score-distribution** corner (R3b/NQC) is now also unblocked, subject to the
per-episode score-availability gate above.

### The load-bearing primitive: `consumed(episode)` **[det]**

A returned nug is **consumed** when a following action depends on it before any
re-query. Deterministic tells, in precedence (strongest first):

1. **explicit** — a later assistant turn or tool call references the returned nug id / a
   distinctive span of its content;
2. **circumstantial** — a task-advancing action (Edit / Write / a direct answer) follows
   the call with **no** self-requery and **no** `set_nug` writing similar content;
3. **negative-only** — the human turn that closes the episode is not `MoveRepair`.

**Tell 2 is not safe to fold into the headline.** "Edit follows, no requery" attributes
the action to the nug, but Claude edits from prior knowledge too — that is precisely the
retrieved-but-ignored failure R7 names. Counting it as a hit inflates RU. So `consumed`
ships in **two variants**, reported side by side:

- `consumed_strict` = **tell 1 only** (explicit id/content reference).
- `consumed_loose`  = **tell 1 ∨ 2 ∨ 3**.

Report both until the dk-labeled sample says which tracks ground truth; do not silently
promote the loose number to the headline. RU (below) inherits both variants —
`RU_strict` / `RU_loose`. `consumed` is the hinge of the whole spec: define it once in
`internal/score`, unit-test both variants against the dk sample.

---

## The northstar: Retrieval Utility (RU)

One number to optimize. Per episode, after excluding coverage-gap episodes (see §C —
those measure the store, not the algorithm):

```
served(e) = consumed(e)              # a returned nug was actually used
          ∧ ¬selfRequeryBefore(e)    # query was right first time (HopInterp clean)
          ∧ outcome(e) ≠ abandoned   # human didn't walk away / repair this hop

RU = mean( served(e) )  over answerable episodes
```

RU ties query + ranking + coverage + human outcome into a single rate. It connects to
the bbp/AHI work: the human-side `MoveRepair`/`OutcomeAbandoned` label is the missing
PARADISE success leg that consumption alone can't supply. Reported as `RU_strict` and
`RU_loose` (per the `consumed` variants above).

**Keep the strict AND-gate for the headline — do not blend.** A weighted blend of the
three conditions reintroduces arbitrary weights, the exact sin this metric design
exists to avoid. The gate is honest precisely because all-three-hold is an unambiguous
win. But the gate alone hides *which* condition failed, so **emit the three component
rates alongside** the gated number, for gradient:

```
consumed_rate  = mean( consumed(e) )            # result quality leg
firsttry_rate  = mean( ¬selfRequeryBefore(e) )  # query quality leg
nonabandon_rate= mean( outcome(e) ≠ abandoned ) # human outcome leg
RU             = mean( all three )              # the gated headline
```

**Gate for the number, decompose for the why.** Optimize RU; read the three component
rates to see which leg moved it.

---

## A. Query quality — the HopInterp leg

Did Claude's query faithfully encode the human's intent? Failures here are interp-hop:
the right nug may exist and rank fine, but the query never asks for it.

| ID | Metric | Tag | Definition | ferret source |
|---|---|---|---|---|
| Q1 | **Self-requery rate** | [det] | fraction of episodes with ≥2 `get_nug` calls where a reformulation precedes any `consumed` result | `Event.Retry` motif within the episode (`attribute.SelfRequery`) — lower is better |
| Q2 | **Reformulation depth** | [det] | count of `get_nug` calls before the first consumed result | episode call count |
| Q3 | **Intent coverage** | [analyst] | judge scores query vs `Event.Prompt`: did it capture the salient *retrievable* concepts? 0–2 | analyst prompt; **needs no results** — pure prompt→query check, isolates the translation step |
| Q4 | **Query specificity (pre-retrieval QPP)** | [det] | query length + mean IDF of query terms against the nug corpus | cheap judgement-free predictor (Cronen-Townsend lineage already cited in `attribute.go`) |
| Q5 | **Rewrite pairs harvested** | [det+analyst] | count of bad→good query pairs mined from self-requery chains that then succeed | `Q1` chains where the later query yields `consumed` — direct training data for query rewriting |

Q1 is the strongest free signal: a query immediately followed by a *rewritten* query
that then succeeds is a self-labeled interp-hop failure **and** a rewrite training pair
(Q5) at once. This is the Jones & Klinkner reformulation signal already cited in
`analyst.go`.

## B. Result quality — the HopRetrieval leg

Given the query, were the returned nugs good?

| ID | Metric | Tag | Definition | ferret source |
|---|---|---|---|---|
| R1 | **Hit rate / answerability** | [det] | fraction of episodes returning ≥1 `consumed` nug (det) or ≥1 *relevant* nug (analyst variant) | `consumed` / judge |
| R2 | **Use-rank → MRR** | [det] | reciprocal rank of the consumed nug in `Event.Results`; mean over episodes | `Results` order + `consumed`. Rank-1 use = ideal. Clean because Claude reads all *k* (no click bias) |
| R3a | **Set-size QPP** | [det] | flags empty vs oversized result sets | `attribute.EmptyResult` / `OversizedResult` — ships in Phase 1, reference-free |
| R3b | **Score-distribution QPP (NQC)** | [det] | result-set score variance (normalized query commitment) | per-hit `Results[].Score` is in the envelope and captured (see §D0) — **live in Phase 1**, gated per episode on score availability (compute only when every hit scored; report NQC coverage) |
| R4 | **Context precision@k** | [analyst] | of *k* returned, fraction judged relevant | golden set |
| R5 | **Context recall** | [analyst] | of relevant nugs that exist in the store, fraction returned | golden set + candidate pool (see §Golden set) |
| R6 | **nDCG@k** | [analyst] | graded-relevance ranking quality over the returned set | golden set |
| R7 | **Grounding** | [det] | did the next action depend on a returned nug, or proceed as if empty? | `consumed` vs retrieved-but-ignored — a quiet failure R1 misses |

R2 and R6 are the two ranking metrics: R2 is the always-on behavioral one (free, online),
R6 is the offline graded one (golden set, for tight iteration). Optimizing R2 alone
risks position-gaming; R6 keeps it honest.

## C. Coverage — the confounder to hold separate

A low RU may mean the nug **isn't in the store**, not that the algorithm failed. Do not
let coverage gaps score the ranker.

| ID | Metric | Tag | Definition | ferret source |
|---|---|---|---|---|
| C1 | **Coverage-gap rate** | [det] | episodes where `get_nug` returns empty/weak → immediately followed by `set_nug` writing similar content | `get_nug`(empty) → `set_nug` motif. Near-perfect "the nug didn't exist" detector |
| C2 | **Good-abandonment guard** | [det] | episodes where confirmed *absence* was the useful answer — excluded from RU failure, not counted as a miss | empty result + no repair + no retry |

Coverage-gap episodes are **excluded from RU's denominator** and routed to a corpus-
coverage backlog. C2 prevents penalizing a search that correctly returned "nothing here."

---

## Golden set + judge rubric (Phase 2)

The one methodological move that pays for itself: a relevance-judged offline set so the
algorithm can be iterated in minutes instead of waiting for production behavior.

**Build.** Sample episodes; the analyst grades each returned nug against `Event.Prompt`
+ the stated task. Validate the judge against a dk-labeled sample to calibrate
(conservatism per the `analyst.go` precedent — a miscalibrated judge wastes dk's
validation budget). Seed new cases actively from the production [det] signals (Q1
self-requery, C1 coverage-gap are high-yield).

**Recall needs a candidate pool.** R5 requires judging nugs the query *didn't* return.
Per query, judge over `returned ∪ sampled-unreturned` (or the full store when small
enough), so "relevant nugs that exist" is well-defined.

Relevance grade scale (graded, enables nDCG):

| Grade | Meaning |
|---|---|
| 3 | exact answer to the intent |
| 2 | relevant, materially helps |
| 1 | marginally on-topic |
| 0 | irrelevant |

---

## Implementation map

Respecting kuv Decisions 2 & 3: scorers live in `internal/score`, ride the `segResult`
unit, and do **not** become `conform` references or new `Finding` kinds.

Status reflects what merged via ferret-sq.d0 (PR#36, capture) and ferret-sq.1 (PR#39,
the Phase-1 det scorers + RU + `ferret retrieval` CLI). Only R3b/NQC remains to wire.

| Piece | Home | Status |
|---|---|---|
| Decision 0 capture (`Query`, `Results`, per-hit `Score`) | `internal/event` (`event.go`, `build.go`), `cmd/ferret` codec | **shipped** (PR#36); `parseNugHits` reads `score`; `omitempty`, no migration |
| `consumed` predicate + episode assembly | `internal/score/retrieval.go` | **shipped** (PR#39); `BuildEpisodes`, `referencedID` (tell 1), `ConsumedStrict`/`ConsumedLoose` on `Episode` |
| Q1–Q2, R1, R2, R3a, R7, C1–C2 (Phase 1) | `internal/score/retrieval.go` + `retrieval_rollup.go` | **shipped** (PR#39); `Episode` fields + `Aggregate` rollup (`R3aEmpty`/`R3aOversized`, `R7GroundingRate`, `C1CoverageGap`, `C2GoodAbandon`, …) |
| R3b/NQC (Phase 1, score-distribution) | `internal/score/retrieval.go` + `retrieval_rollup.go` | **to wire** — read `Results[].Score`, gate per episode on full score availability, add NQC + NQC-coverage to `Rollup` |
| `attribute.TurnContext` wiring | `internal/dialogue` | `Episode.TurnContext()`/`Hop()` project episode signals; `SelfRequery`/`EmptyResult`/`Oversized` filled from the episode |
| RU rollup | `internal/score` (`retrieval_rollup.go`) → `ferret retrieval` output | **shipped** (PR#39); `Rollup` carries `RUStrict`/`RULoose` + the three legs + Q/R/C rates |
| Q3, R4–R6 + golden set + judge (Phase 2) | `internal/analyst` | relevance-judge prompt; dk validates a sample |
| CLI surface | `cmd/ferret/retrieval.go` | **shipped** (PR#39); `ferret retrieval` (text + json, `--session`/`--limit`), mirrors `ferret conformance` |

### Two-phase rollout

- **Phase 1 — [det] only, no LLM, the always-on northstar.** RU (consumption + outcome),
  Q1 self-requery, Q2 depth, R1 hit-rate, R2 use-rank/MRR, R3a set-size QPP, R3b NQC
  score-distribution QPP, R7 grounding, C1/C2 coverage. All from the event stream +
  `dialogue` + the `attribute` wiring. Ships behind Decision 0. **Merged:** the capture
  (PR#36) and every leg except R3b (PR#39); R3b/NQC is the last Phase-1 scorer to wire
  (read `Results[].Score`, gate per episode on full score availability — see §D0).
- **Phase 2 — [analyst] offline iteration engine.** Q3 intent coverage, R4 precision,
  R5 recall, R6 nDCG via the golden set. dk-validated. This is what you iterate the
  algorithm against between releases.

---

## Open decisions for dk

1. **`consumed` variants** — ship `strict` (tell 1) and `loose` (tell 1∨2∨3) in
   parallel; the dk-labeled sample adjudicates which becomes the headline. *(resolved:
   report both, no silent promotion of the loose number.)*
2. **Decision 0 columns** — *(resolved against the corpus, then corrected by sq.d0:
   query arg + ranked result **ids** survive, **and so does a per-hit per-query `score`**
   (descending, distinct from `meta.trixiQuality`) — see §D0. Captured in `NugHit.Score`
   (PR#36). Episode unit filtered to query-mode; R3b/NQC is **no longer deferred** —
   in Phase 1, gated per episode on score availability (~⅓ of sampled hits scored).)*
3. **Relevance scale** — graded 0–3 (enables nDCG) vs binary (cheaper to judge)?
4. **RU gating vs weighting** — *(resolved: keep the strict AND-gate for the headline,
   emit the three component rates alongside. No blend.)*

## Approve to unblock

```
bd create "ferret search-quality metrics" --design docs/design/ferret-search-quality-metrics.md
# Phase 1 leaves: capture(D0) → consumed/RU → Q/R/C det scorers → ferret retrieval CLI
# Phase 2 leaves: golden-set harness → analyst relevance judge → nDCG/precision/recall
```
