# Boot
updated: 2026-06-11

→ ◯ for dk: 1k validate numbers are in (below). Pull the full 80k? Exact-lens anti-correlation is the live thread — confounds (model size, trajectory length) unchecked.

## 1k validate results (uncommitted: cmd/ferret/main.go, internal/sweagent/*)
- base-fail 83.1% (831/1000, mostly llama-70b)
- tool lens: lifts ≈1.0 (FRICTION 1.05 best) — buckets cover 65–86% of streams, diluted to base
- exact lens: LOOP lift **0.48** (n=72), WATCH 0.67 (n=231) — loops predict *success*, sign flipped vs hypothesis
- repro: `/tmp/swe-sample.jsonl` (1k rows), `ferret validate -data /tmp/swe [-lens exact]`

Two real-data fixes en route (need commit):
1. row.go: nebius shape = `text` field, action in **last** ``` fence (verified vs observations)
2. main.go: dataset has many rollouts per instance_id → `#n` suffix, else 1000 rows collapse to 48 streams

✓ done
- 7 overnight fix PRs reviewed, merged, beads closed; main green (build/test/lint)
- filed P3 bead: partial ingest seals mismatched outcomes.jsonl
- 1k sample ingested + validated; decoder widened; stream-collision fixed; make check green

‡ traps
- stale lint cache keys removed worktree paths → `golangci-lint cache clean` on phantom hits
