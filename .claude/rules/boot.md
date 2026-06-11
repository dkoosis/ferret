# Boot
updated: 2026-06-10

→ Land the real SWE-agent finding: adapter merged (PR #2, on main). Now pull a ~1k sample and run validate for the *actual* numbers — fixture n=3 only proved plumbing.

1. duckdb hf→jsonl one-liner in `testdata/README.md`; binary never touches HF.
2. `ferret ingest -source swe-agent -root <sample> -data /tmp/swe && ferret validate -data /tmp/swe`.

‡ traps
- stale lint cache keys removed worktree paths → `golangci-lint cache clean` on phantom hits.
