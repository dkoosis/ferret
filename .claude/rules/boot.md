# Boot
updated: 2026-07-01

*Project working-memory. Maintained for future-me: current state + live frontier + traps that still bite. Resolved lanes pruned — history lives in beads/PRs.*

## lane: GreatKilldeer
→ next: **dispatch `ferret-bbp.8`** (cross-episode abandonment wiring; depends on bbp.7's shipped MoveNewTask + inherits new-task recall tuning) — or `bbp.9` (clarify population, makes the shipped MoveClarify mechanism live). Both unblocked.
✓ shipped bbp.7 (#54, v2 13-move taxonomy) via dispatch — 5 review passes caught a P0 outcome regression + friction-inflation on dk's own turns; filed bbp.8/.9, ferret-ubf.
‡ a taxonomy split needs a codebase-wide `IsRepairMove`-style predicate audit, ✗ only plan-named call sites — the miss cost a round-4 retrieval.go ripple.
~ dk: fenced trust-the-loop, spot-checks via a critical-review paste; ends on "next?".

## State
- main @ `6f3a4cf`, origin synced (bbp.7 merged #54). PR queue EMPTY.
- Scorers live in **`internal/score/`** (landmark/quality/conform all there — ratified, design-doc D2). New scorers go here.
- `/team` = one shared tree + loto (worktrees retired). No concurrent `make check`; primary verifies once at wave end.

## Frontier — where the work is

**ferret-bbp** (epic, in_progress) — User-turn repair/acceptance tagger: read intent from the *human's words*, not just tool sequences. **Deterministic spine COMPLETE** (#49–52): boundary guard → per-segment Outcome → TurnContext/AttributeHop → Hop2 QPP → surfaced on `mine.Finding` (rank/report/out). The **consumer clause** of the retrieval-outcome contract below.
- ✓ shipped: bbp.1 (compaction-carrier boundary guard) · bbp.2 (per-seg Outcome rollup) · bbp.3 (TurnContext, un-stubbed AttributeHop) · bbp.4 (det. Hop2 QPP scorer, `internal/score/qpp.go`) · bbp.6 (de-island dialogue+hop onto `mine.Finding`, emit seam `cmd/ferret/finding_dialogue.go`)
- ✓ **bbp.7 SHIPPED** (#54) — v2 13-move taxonomy (7 outcome-bearing + 6 catalog), carrier pre-filters, behavior-preserving repair→reject split via `IsRepairMove`, `episode.Classify` extended. Split out: **bbp.8** (cross-episode abandonment wiring, depends bbp.7 + owns new-task recall tuning) · **bbp.9** (MoveClarify population — mechanism shipped, prod-inert until a caller sets PriorAgentQuestion).
- → remaining LLM half: **bbp.5** Hop1 interp-fidelity judge (`requires_plan`, still needs plan; dk drives).
- spun off the AHI-essay assessment: **bbp.10** (deterministic friction metrics) + **bbp.11** (agency-calibration axis — agent-side initiative, sibling to the user-move taxonomy).
- bbp regex-first; ✗ emotion detector (dev jargon "kill/dead/braindead" ≠ affect)

Under epic **ferret-kuv** (container, ✗ dispatch): **567** (metrics-engine for a Claude analyst — per-candidate bundle + proposal loop, in_progress), **kuv.3** (tool-for-intent applicability check, in_progress).

**Retrieval-outcome contract (trixi⇄ferret)** — drafted 2026-06-29 → `~/Projects/dk/Project/trixi/specs/retrieval-outcome-contract-design.md`. The validation seam:
`trixi/observe.2.1` emits retrieval-event JSONL (producer) → `ferret/bbp` joins to task segments + adjudicates outcome (consumer) → `search-loop.4` reads back via interrupted-time-series (reader).
Decided calls: `schema_version`/record · sidecar JSONL not the CC transcript · per-task-segment grain (store fine, roll up) · `config_fingerprint` as ITS segment-key · key = `(session_id, agent_id, ts)`, carry `agent_type`. Out of scope: `observe.2.2` (Q3 prompt→query, upstream). **ferret's side of this contract = the bbp epic.**

## Live traps
- **agent_id/agent_type are the ONLY parent↔subagent discriminator** — `session_id` + `transcript_path` are SHARED (claude-code-guide-confirmed). Anything keying retrieval/attribution per-agent MUST carry `agent_id` (it's Trap 2 in the contract; bbp.3 TurnContext attribution rides on it).
- **/team shared-tree clobber** — a wave agent relocating a *peer's* untracked files via a flat-basename scratch dir can silently destroy untracked work (lost kuv.10's `internal/score/landmark.go`, 6-19). Filed **`ccp-l1nf`** (cc-plugins, P1). loto is a no-op *within* a wave (shared identity, loto-fs84); write-set disjointness is the only guard and it's leaky for untracked files. ✗ reach outside your write-set.
- **Branch-staleness diff** — a branch that LOOKS like a huge diff vs main is usually a stale merge-base fooling the *three-dot* diff. Real tell = two-dot tree diff: `git diff main <branch>`.
- nilaway debt at `gates.go:242` / `golden.go:111` — CLEARED by #48 (provably nil-safe). ✗ re-chase as a regression.
- **`codex-review.yml` fails at infra level** (~30s run, no review posted) — recurring across PRs; the `@codex review` comment trigger doesn't land a verdict. ✗ wait on it or treat as a gate. `make check` (local) is the gate (ci-on-demand.md).
- **bbp impl lint tail** — adding fields to a hot struct (`mine.Finding`→152B) trips `rangeValCopy` on *existing* value-range loops; integration beads tend to push a func past gocognit 15. Cheap to fix at wave verify (index-range + helper-extract), but expect it.

## dk read (stable, 30+ sessions)
Dry fenced grants, zero corrections → trust-the-loop. Surface deviations, ✗ ask permission mid-build. On **open design** dk drives + wants the *why* before a model-changing/destructive call; hand him the approve/merge fork crisply (he ends on "next?"). When dk states a ground-truth fact, verify-then-proceed — ✗ re-litigate.

## Loose thread
- read-before-edit/write hookify guard — top ferret-scan burn finding (`Edit!⇝Read` + `Write!⇝Read⇝Write`, ~670k). Build as a hook, log in the ferret fix ledger. Harness-side, not a ferret bead. Done-status unverified.

## Shipped ledger
bbp.7 v2 taxonomy (#54) · bbp deterministic spine #49–52 (bbp.1/.2/.3/.4/.6) · landmark wave kuv.10/vy7/afm/t5d (#45–47) · sq.2 judge+golden + d28 spec (#41) · kuv.5 quality axes (#44) · kuv.4 conformance (#27) · kuv.2 segmentation · kuv.1 spine (#23) · d01 prompt-text capture (#34) · bbp tagger (#33) · 9 go-bug-audit fixes (#31) · nilaway clear (#48).
