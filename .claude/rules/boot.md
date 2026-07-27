# Boot
updated: 2026-07-27

*Project working-memory. Maintained for future-me: current state + live frontier + traps that still bite. Resolved lanes pruned — history lives in beads/PRs.*

## lane: ferret-097 — close the loop (NEW, phase 4, ratified 2026-07-27)
Phases 1–3 (bbp/kuv/wf9) ALL CLOSED. Route fork resolved as EXTEND (decision nug `0d350b228b92`): ★ line unchanged, roadmap gains rung 4. **ferret-097** — ferret moves observation→actuation: rank what burns, tune, verify the delta.
→ Children: **nrr** (per-shellnorm-command output-byte burn ranking, `report --kind burn` or `ferret burn` — pin door at build) · **ct1** (repeated-misfire ranking + repair pairs, feeds the kuv.15 substitution-ledger loop) · **kt5** (burn-delta in report, undeferred, blocked by nrr). Ready: nrr, ct1.
→ Trigger: RTK review (rtk-ai/rtk — Rust output-filter proxy). RTK adoption NOT decided — revisit after nrr's ranking says what actually burns.
~ dk drives forks himself — hand them crisply, ✗ pre-decide ratified semantics.

## State
- main @ `4cf29d5` (#104: 917 omitempty + fk8 judge fan-out), origin synced. PR queue EMPTY.
- Scorers live in **`internal/score/`** (landmark/quality/conform/qpp all there — ratified, design-doc D2). New scorers go here.
- `/team` = one shared tree + loto (worktrees retired). No concurrent `make check`; primary verifies once at wave end.
- 7hr (param-clump refactor) stays DEFERRED — revisit only if phase 4 leaves cmd/ferret's surface intact.

## Frontier — where the work is

**ferret-bbp — CLOSED.** User-turn repair/acceptance tagger: read intent from the *human's words*, not just tool sequences. Full deterministic spine + v2 taxonomy + Hop1/Hop2 judges + `helped` adjudicator + agent-initiative scorer all shipped (#49–91). Detail in the ledger below. This was ferret's side of the retrieval-outcome contract.

**ferret-kuv — CLOSED.** Intent-grounded tool-improvement harness. All children shipped or dropped (kuv.3/.14/.15/.16/567 done; **kuv.9 dropped** — its correction-language signal was subsumed by bbp, grounding source PULSE rejected). **567** (metrics-engine for a Claude analyst) shipped its children (567.1/.2). Substitution-ledger loop CLOSED: adjudicate flags → dk validates → `ferret fixes sub` records → hook/CLAUDE.md nudge consumes.

**ferret-wf9 — CLOSED (phase 3).** In-session feedback tap shipped end-to-end (#96–99): ask-side selector + label ledger + live join orchestration + answer-side recognition/valence/probe-exclusion. Gold labels now accumulate in-band.

**ferret-097 — OPEN (phase 4, the live lane).** Close the loop: burn ranking (nrr) + misfire ranking (ct1) → tune → burn-delta verify (kt5). Detail in the top lane block.

**Retrieval-outcome contract (trixi⇄ferret)** — ferret's consumer side = the bbp epic, now SHIPPED. Spec: `~/Projects/dk/Project/trixi/specs/retrieval-outcome-contract-design.md`. Seam: `trixi/observe.2.1` emits retrieval-event JSONL (producer) → `ferret/bbp` joins to segments + adjudicates (consumer, done) → `search-loop.4` reads back via interrupted-time-series (reader, upstream). Golden fixture is the contract test (ferret vendors it). Producer conformance = `tx-dii8m`.

## Live traps
- **agent_id/agent_type are the ONLY parent↔subagent discriminator** — `session_id` + `transcript_path` are SHARED (claude-code-guide-confirmed). Anything keying retrieval/attribution per-agent MUST carry `agent_id`.
- **/team shared-tree clobber** — a wave agent relocating a *peer's* untracked files via a flat-basename scratch dir can silently destroy untracked work (lost kuv.10's `internal/score/landmark.go`, 6-19). Filed **`ccp-l1nf`** (cc-plugins, P1). loto is a no-op *within* a wave (shared identity); write-set disjointness is the only guard, leaky for untracked files. ✗ reach outside your write-set.
- **Branch-staleness diff** — a branch that LOOKS like a huge diff vs main is usually a stale merge-base fooling the *three-dot* diff. Real tell = two-dot tree diff: `git diff main <branch>`.
- **`codex-review.yml` fails at infra level** (~30s run, no review posted) — recurring; the `@codex review` comment trigger doesn't land a verdict. ✗ wait on it or treat as a gate. `make check` (local) is the gate (ci-on-demand.md).
- **Hot-struct lint tail** — adding fields to a hot struct (`mine.Finding`) trips `rangeValCopy` on *existing* value-range loops; integration beads push a func past gocognit 15. Cheap to fix at wave verify (index-range + helper-extract), expect it.

## dk read (stable, 30+ sessions)
Dry fenced grants, zero corrections → trust-the-loop. Surface deviations, ✗ ask permission mid-build. On **open design** dk drives + wants the *why* before a model-changing/destructive call; hand him the approve/merge fork crisply (he ends on "next?"). When dk states a ground-truth fact, verify-then-proceed — ✗ re-litigate.

## Loose thread
- read-before-edit/write hookify guard — top ferret-scan burn finding (`Edit!⇝Read` + `Write!⇝Read⇝Write`, ~670k). Build as a hook, log in the ferret fix ledger. Harness-side, not a ferret bead. Done-status unverified.

## Shipped ledger
**bbp epic (closed):** agent-initiative scorer bbp.11 (#83/85/87) + no-pushback over-init bbp.18 (#86) · bbp.21 shipped-artifact tell (#91) · bbp.20 query-mode recall roots (#90) · bbp.19 transcript-paste parse (#93) · bbp.17 read-adjacency 5-verdict (#81) · bbp.16 ts→segment join + helped CLI (#79) · bbp.15 judge_fingerprint (#78) · bbp.14 helped adjudicator (#74) · bbp.13/.12/.10/.9 (#56/57/58/59) · bbp.7 v2 taxonomy (#54) · bbp.5 staged Hop1 judge (#53) · spine bbp.1/.2/.3/.4/.6 (#49–52).
**kuv + misc:** kuv.16 confessed-waste proposals (#93) · kuv.15 substitution ledger (#80) · 567.2 conformance leak-multiplicand (#88) · kuv.3 analyst-assumes-snipe (#77) · izo recall-trace mode (#84) · qus odds-ratio signal + 5c0 placeholder-mapping (#94) · 8bb 'next:' hints (#92) · dic trixi-ask store-reach · backlog wave 1 bug fixes 9iu/kzg/ffc/mls/cc3 (#95, #89) · landmark wave kuv.10/vy7/afm/t5d (#45–47) · kuv.5 quality axes (#44) · kuv.4 conformance (#27) · kuv.2 segmentation · kuv.1 spine (#23) · nilaway clear (#48).
