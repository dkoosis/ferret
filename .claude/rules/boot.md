# Boot
updated: 2026-06-15

→ Build the read-before-edit/write hookify guard — top fix from baseline scan (~670k measured burn: Edit!⇝Read + Write!⇝Read⇝Write) — then record it in the ferret fix ledger (skill §Close the loop). `/ferret:scan` re-runs.

✓ done
- skill loop shipped: ferret 0.2.0+0.2.1 (routing map, lens table, fix ledger), xf 0.2.3 nug-fmt syntax
- baseline scan: 26 findings, ledger ∅ — D1 gated as ferret-kt5 (deferred 7/12)
- 6/15: PR#16 merged (a652d51) — 6 durability/correctness fixes (2kq,x9u,ged,i2p,4k7,bpl) closed. make check green. x9u OrphanBytes design confirmed (no phantom burn rows).

‡ traps
- stale local branch `team/impl-20260611-085800` = 10+ UNMERGED commits, not mine — left intact, ✗ delete
