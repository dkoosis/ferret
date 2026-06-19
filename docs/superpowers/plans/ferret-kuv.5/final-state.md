# final-state — ferret-kuv.5

bead: ferret-kuv.5 · shape: standard · profile: craft
classification: defaulted-no-metadata (difficulty/est_cost null; ~2 pkgs, no new pkg, no labels)
p-write: snapshot-from-plan-doc (refreshed 2026-06-19, plan_approved=true)
authority_mode: ship
preflight_audit: 2 findings PRE-EXISTING outside touch set (gates.go:242, golden.go:111 nilcheck)

## decisions locked at P-self (author-recommend slots, plan already dk-approved)
- Q3: factor FrictionCut mean/σ into a shared helper both mine+score call (no parallel variance, no whole-corpus coupling).
- Q6: in-place `*Axes` field on Segment, mirroring kuv.8 `*Outcome`.
- Q7: cite TRACE/tau-bench as inspiration in doc-comments; note framing is an adaptation in this file. No separate verification bead.

## convergence
- make check green (test+race+nilcheck); internal/score 93.1% cov.
- 1 review pass, 1 finding applied (low, in-scope), deviation accepted. No P0/P1.
- Dispatch was verify+review+ship: impl pre-existed (commit 9335567).

## plan-vs-actual file delta
matches plan File-by-file exactly: internal/score/{quality.go,quality_test.go,segment.go+Axes},
internal/mine/surprise.go (MeanStdDev extract), cmd/ferret/{quality.go,quality_test.go,main.go}.
DEVIATION: cluster consistency = cost coefficient-of-variation (not efficiency-spread) — sound under exact-Shape clustering.

## north_star_answer:
Surfaced to dk via PR body (the cost-CV deviation is the north-star-relevant design call).
kuv intent-grounded harness: per-task axes + cross-attempt reliability both serve "is the agent
predictably effective on a recurring task shape" — on-direction.
