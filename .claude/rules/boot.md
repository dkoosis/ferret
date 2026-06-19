# Boot
updated: 2026-06-19

## lane: TersePanda
updated: 2026-06-19

→ **Watch PR#44 (kuv.5)** for bot/codex review; on merge `bd close ferret-kuv.5 --reason "merged #44"` (still claimed/in_progress, close-on-merge). Then next ready: **kuv.10** (landmark progress, plan_approved) or **d28** impl half (in_progress, dk-assigned).
✓ kuv.5 consistency scalar DECIDED (dk, 2026-06-19): per-task COST (1−CV), not efficiency. Plan+code+docs ratified (commits fd50002/18f27e9 + doc reframe). ✗ re-surface.

✓ #41 merged (sq.2 judge + golden + d28 spec). Built kuv.5 → PR#44: internal/score/quality.go (ScoreAxes per-task eff/adapt + ClusterByShape pass^k) + mine.MeanStdDev shared σ (Q3) + `ferret quality` 2-scope cmd. make check green, score cov 93.1%, race clean. `@codex review` requested.
✓ earlier: merged #35–#39; 5 beads closed.

‡ traps
- kuv.5 lives in WORKTREE /Users/vcto/Projects/ferret/ferret-kuv.5 (branch ferret-kuv.5, pushed). `.gitignore` ignores the dir. ✗ leave stray dispatch logs (docs/.../ferret-kuv.5/stage-*.log) in commits.
- PRE-EXISTING nilaway debt: internal/gates/gates.go:242 + internal/analyst/golden.go:111 (from #37/#41). NOT mine; `make check` doesn't gate on it (`audit` does). Don't chase as a kuv.5 regression.
- wave2 lanes all add to cmd/ferret/main.go CLI → worktree-isolate.

~ dk: dry fenced grants, zero corrections — trust-the-loop. Surface deviations, don't ask permission mid-build.

## lane: PlaidSparrow

→ verify agent_id/agent_type lands on ferret's capture-hook event (Python-vs-TS field split), then wire it as the parent-vs-subagent key for d01/bbp get_nug + user-turn attribution.

✓ Board-readable get_nug improvement-flow write-up → nug `a55d63b61ddb`; confirmed Q3 (prompt→query) + bbp (repair/acceptance) as the two frontier legs over the shipped det/judged scorers.

‡ traps
- CC hooks: session_id + transcript_path are SHARED parent↔subagent; only agent_id/agent_type discriminate (SubagentStop also carries agent_transcript_path).

~ dk: research/divergent session — light-touch steering questions, zero corrections.

## lane: NullMerlin

→ Watch PR#34 (ferret-d01) for bot review (like #33); on merge `bd close ferret-d01`, then bbp analysis follow-ons.

✓ PR#33 bbp tagger merged (3 gemini fixes accepted); filed+shipped ferret-d01 — full prompt text on events → PR#34

‡ traps
- d01 captures harness envelopes (teammate-msg/command/compaction-summary) AS prompt text — filtering them is bbp analysis follow-on #4, NOT ingestion
- ferret-d01 blocks ferret-bbp (dep wired)

~ dk terse fenced grant, "let's resume"→headway; zero corrections, trust-the-loop

## lane: ProudBuffalo

→ Merge PR#25 (5ic) + #26 (kuv.2 det-half): `gh pr merge 25 26 --squash`. Then kuv.2 analyst-merge half — interactive, dk=validator.

✓ /team backlog: 5ic + kuv.2 det-half → PR#25/#26; grooming deferred 567 behind kuv.2

‡ traps
- kuv.2 bead STAYS OPEN post-#26 — only deterministic Go half shipped; analyst merge half remains (by design)
- left local: fix/ferret-5ic + fix/ferret-kuv.2 (pushed); wave branch team/impl-20260616-2241 kept

~ dk: grooming + autonomous team-backlog, zero corrections — trust-the-loop when fenced

## lane: PrimeLeopard

→ Build read-before-edit/write hookify guard — top baseline fix (~670k burn: Edit!⇝Read + Write!⇝Read⇝Write), log in ferret fix ledger (skill §Close the loop). `/ferret:scan` re-runs.

✓ /team backlog drain: kuv.1 spine subcommand → draft PR#23 (await dk merge); kuv.7 closed (snipe WAS indexed in-window → DESIGN non-adoption finding VALID)
✓ deferred ferret-567 → dk call: 2026-06-11 design superseded-ish by kuv intent reframe ("Extends ferret-567") — live / fold / supersede?

‡ traps
- SHARED checkout, ≥1 LIVE peer — a peer did the docs/→vault move (a3defde, UNPUSHED on main; main 1 ahead of origin, peer pushes). I branch-switched + reset --hard in the shared tree = risky; use worktrees / loto-coordinate next time. ✗ blanket commit/push.
- left local: team/impl-20260616-1500 + fix/ferret-kuv.1 (pushed). Old 0611/1237 peer-branch traps STALE → dropped

## lane: MildEgret

→ Merge PR#27 (kuv.4 conform det-half): `gh pr merge 27 --squash`. Then kuv.4 analyst-labeling half (interactive, dk=validator) — bead STAYS OPEN.

✓ kuv.4 deterministic conformance scorer → PR#27 (`ferret conformance` + internal/conform, 100% cov, make check green). Validated loto/6ccedb07 — localizes skipped wait-green gate + 8 off-plan polls.

‡ traps
- seq reference is enough (dk call) — ✗ build branching/partial-order model
- left: fix/ferret-kuv.4 (pushed)

~ dk: terse fenced grant, zero corrections, trust-the-loop; commit→push staccato at close.

## lane: PrimeStoat

→ ferret-bbp when picked — user-turn repair/acceptance tagger, regex-first, extends kuv intent-reframe. `bd show ferret-bbp`

✓ assessed ferret vs ../dk AHI report (analyzing agent-human interaction logs.md); filed ferret-bbp w/ refs (PARADISE, ISO 24617-2, CA repair, USE)

‡ traps
- ferret reads tool-call stream only; AHI gap = USER-side language (repair "no/try again", acceptance "save this") → outcome label, the missing PARADISE leg
- v1 regex-only, ✗ emotion detector (dev jargon "kill/dead/braindead" ≠ affect)

~ dk: terse fenced grant, "yes + refs", zero corrections — trust-the-loop

## lane: UltraVole

→ After PR#31 merges, close the 9 beads: `bd close ferret-s3z ferret-0vz ferret-g2o ferret-v42 ferret-c71 ferret-001 ferret-0m7 ferret-020 ferret-xz8`.

✓ Fixed 9 go-bug-audit beads (s3z 0vz g2o v42 c71 001 0m7 020 xz8) + tests → PR#31; check/race/nilcheck green

‡ traps
- beads still CLAIMED (open) — close on merge
- lockData `//go:build unix` (Flock); dropped writeAtomic perm (unparam)

~ dk killed the conservative profile: commit/push/PR freely, review IS the PR. ✗ ask before committing.

## lane: GoldSquirrel

→ PR queue empty — no pending merges. Check `gh pr list` next session.

✓ assessed PRs: merged #32 (CLAUDE.md team-maintainer profile only — code already in #31), closed #30 superseded (stale, merging would regress 792 lines)

‡ traps
- #30/#32 LOOKED like huge diffs vs main — stale merge-base fooling three-dot diff. Real tell = two-dot tree diff (`git diff main branch`). #31 squash-folded kuv.12+candidates+bug-audit, so branch copies were dupes.

~ dk fenced "IFF improves" grant, zero corrections — trust-the-loop

## lane: RawStarling

→ Review 6 plan docs in docs/superpowers/plans/ (bbp, kuv.5/8/9/10, 2p6); ratify `internal/score/` as scorer pkg home (all 6 flagged it; design doc D2 says so) → `bd update <id> --set-metadata plan_approved=true` per plan to unblock impl.

✓ /team backlog drain: 6 plan-gated beads → 6 plan docs on main (2 waves, 0 corrections)

‡ traps
- queue is 100% plan-gate-pending now — next /team backlog defers all 6 as gate-pending until plans approved. epic ferret-kuv = container, ✗ dispatch.

~ dk: terse fenced grant, ended on "next?" — wants the approve/merge fork, ✗ more dispatch
