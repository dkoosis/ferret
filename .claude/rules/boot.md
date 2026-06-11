# Boot
updated: 2026-06-11

→ Pull the full 80k, re-validate: 1k signal confirmed, survives model/length strata.

1. duckdb one-liner in `testdata/README.md`, drop LIMIT → `/tmp/swe-full.jsonl`
2. `ferret ingest -source swe-agent -root /tmp/swe-full.jsonl -data /tmp/swe80 && ferret validate -data /tmp/swe80 -lens exact`
3. `go run ./cmd/confound -data /tmp/swe80 -sample /tmp/swe-full.jsonl`

✓ done
- nebius decoder fixed (text shape, last fence, rollout #n streams); pushed
- 1k validate: exact-lens LOOP lift 0.48 (45.7 vs 12.9 within 70b)

‡ traps
- no train/test split yet — correlation claim only
