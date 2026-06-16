# Boot
updated: 2026-06-16

→ Build the read-before-edit/write hookify guard — top fix from baseline scan (~670k measured burn: Edit!⇝Read + Write!⇝Read⇝Write) — then record it in the ferret fix ledger (skill §Close the loop). `/ferret:scan` re-runs.

✓ done
- skill loop shipped: ferret 0.2.0+0.2.1 (routing map, lens table, fix ledger), xf 0.2.3 nug-fmt syntax
- baseline scan: 26 findings, ledger ∅ — D1 gated as ferret-kt5 (deferred 7/12)
- 6/15: PR#16 merged (a652d51) — 6 durability/correctness fixes (2kq,x9u,ged,i2p,4k7,bpl) closed. make check green. x9u OrphanBytes design confirmed (no phantom burn rows).
- 6/16: PR#17 (ferret-045 surprise-partition) + PR#18 (ferret-qsf temp-cleanup log, +stderr seam from gemini review) squash-merged → main (4dfefa1). make check green on merged main. Beads closed.
- 6/16: this PR lands the orphaned intent-grounded reframe (DESIGN, PRIOR-ART) — was wave-base 62a0ca8, never sliced.

‡ traps
- stale local branch `team/impl-20260611-085800` = 10+ UNMERGED commits, not mine — left intact, ✗ delete
- wave branch `team/impl-20260616-1237` is a PEER's live integration tree (fleet ferret-9d9/17q). Don't switch its HEAD or sweep its locked files (event/lens/build). Use isolated worktrees.
