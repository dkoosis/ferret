# Workflow

*Dev conventions for shipping changes. Terse.*

## Batch small fixes → one PR

‡ Several small/independent fixes in flight → ONE branch, ONE PR, one commit per bead. Full `make check`/CI fires once at the PR (+ once on merge to main), NOT once per fix — a PR-per-one-liner is what serializes the queue behind build time.

- Each fix stays its own bead + commit (traceable); PR body lists the beads. Review reads per-commit.
- Bundle by session/theme; ✗ mix a risky change in with trivial ones (it drags the whole PR's review bar up).
- ✗ confuse with drive-by edits *folded into* an unrelated change — batching keeps fixes as separate traced commits, just shipped together.
- **Default: auto-batch** — ≥2 small fixes queued → roll them onto one PR without asking.
