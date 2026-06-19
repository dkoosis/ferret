# triage — ferret-kuv.5

Dispatch pivoted: implementation already existed on branch (commit 9335567, authored prior session,
pushed to origin/ferret-kuv.5, bead still open, no PR). Dispatch became verify + review + ship.

## Findings

| # | Source | Finding | Sev | Decision |
|---|--------|---------|-----|----------|
| 1 | code-reviewer | `efficiency()` dead guard `calls<=0` — never fires for no-call sentinel (FirstCall=-1→calls=1); latent 1.0 if a future test pairs FirstCall=-1 with non-empty shape | low | APPLY — one-line guard swap to `seg.FirstCall < 0`, in-scope (bead's own new file), zero-risk (upstream ScoreAxes already skips these; check is strictly safer). make check green after. |
| 2 | code-reviewer | Deviation: cluster consistency uses cost coefficient-of-variation, not efficiency-spread (plan said efficiency) | n/a | ACCEPT — reviewer + author agree: exact-Shape clustering makes axes shape-determined (~0 spread, decorative 1.0); cost genuinely varies across same-shape attempts, so cost-CV is the real reliability signal. Surfaced in PR body for dk. |

## Plan-adherence (Explore reviewer)
All 5 load-bearing plan claims confirmed against code (score.Segment fields, LabelOutcomes template,
mine.MeanStdDev extraction, CLI scope-by-presence, pre-existing nilchecks outside touch set).
Acceptance criteria met; one DEVIATION (cost-CV) documented and accepted.

## Determinism / edges (code-reviewer) — all clean
map→sort.Slice byte-stable; div-by-zero guarded (mean==0, k<2 sentinel); k==1 → clusterNoSignal(-1), not 1.0;
nil-shape safe; corpus walk returns non-nil empty slices; format+root validation present.
