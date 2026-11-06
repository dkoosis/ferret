# Workflow

*Dev conventions for shipping changes. Terse.*

## Batch small fixes → one PR

‡ Several small/independent fixes in flight → ONE branch, ONE PR, ONE commit — the commit count is ruled in `standard-git.md` (sd-g2pf, 2026-09-07); ✗ restate it here. Full `make check`/CI fires once at the PR (+ once on merge to main), NOT once per fix — a PR-per-one-liner is what serializes the queue behind build time.

- Each fix stays its own bead; the `Closes:` trailer names every one, and that trailer is what carries traceability once per-fix commits are gone. Review reads the diff.
- Bundle by session/theme; ✗ mix a risky change in with trivial ones (it drags the whole PR's review bar up).
- ✗ confuse with drive-by edits *folded into* an unrelated change — batching keeps fixes as separate traced BEADS, shipped together in one commit.
- **Default: auto-batch** — ≥2 small fixes queued → roll them onto one PR without asking.

## PR ↔ beads

‡ Every PR body carries a `Closes:` trailer naming the beads it lands, one `Closes: <id>` line each (no bead → `Closes: none`); `sdlc ship --closes=<ids>` writes them. ✗ `Closes: ferret-abc, ferret-def` on one line — `sdlc merge` cannot read it. Squash-merge keeps the trailer in main's commit. `sdlc merge` closes the beads the trailer names, so ✗ `bd close` after a merge.

## Code traps

*Moved from the retired boot.md (2026-11-05).*

- ‡ `agent_id`/`agent_type` are the only parent-versus-subagent discriminator; `session_id` and `transcript_path` are shared. Anything that keys retrieval or attribution per agent carries `agent_id`.
- Scorers live in `internal/score/` (landmark, quality, conform, qpp). New scorers go there.
- A new session-render func takes the `sessionRun{w, root, session, format}` struct (`cmd/ferret/segment.go`), not its fields.
- Bare-`ferret` scoreboard: `--max-bytes` budgets the data rows (they go through `sink.Row`, ✗ `sink.Head`); Δ is row-capped at `scoreboardCap` over an ordered `buildDelta` (newest fix first, key ascending on ties). Read ferret-yid's trail before changing the Δ order.
- A nug's `tool:` clause for `ferret signatures seed` must be the command as run (a `$(...)` masks to `<sub>`); prose never matches.
- `/team` shares one tree under loto: ✗ reach outside your write-set (a wave agent once destroyed a peer's untracked work, ccp-l1nf); no concurrent `make check`.
- A branch that looks like a huge diff against main is usually a stale merge-base fooling the three-dot diff; the tell is `git diff main <branch>`.
- Adding fields to a hot struct (`mine.Finding`) trips `rangeValCopy` on existing value-range loops; expect to fix it with index-range plus a helper at wave verify.
- RTK stays unused: decision nug 5d4f08673823. ✗ reopen it without the hand replay that nug names.

