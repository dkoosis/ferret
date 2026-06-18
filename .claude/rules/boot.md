# Boot
updated: 2026-06-17

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
