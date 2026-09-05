# north star — ferret

★ measures Claude Code sessions in bytes — the multi-step sequences worth one deterministic helper, the calls that bought nothing, who burned it — and proves each fix by the delta.

AX-first: the primary consumer is Claude itself. ferret's traces are ground truth for the recall-eval loop and for friction-hunting in dk's sessions.

## Four surfaces, and what was ceded

Ratified 2026-08-30 (decision nug `feb7162e18b9`). ferret measures; it does not narrate.

1. **Sequences → helpers.** Multi-step routines ranked by burn under the `cmd` lens — the ones worth collapsing into one deterministic call. Nothing else finds them.
2. **Burn-delta.** Before/after bytes for any fix, whether the fix is a helper, a hook, or a rule line. This is what makes advice testable.
3. **Priced single-call waste.** Misfires and polling, in bytes, per command key — which call, and how much.
4. **Per-agent attribution.** Subagent share of burn; parent and fork split apart.

Ceded to Claude Code's built-in usage report (`~/.claude/usage-data/report-*.html`): intent tagging, outcome judging, prose recommendations. It does those better, for free, and over the same corpus. The dialogue/bbp/judge lanes are frozen where they stand — no new work.

The earlier `/insights` boundary (2026-08-23, bead ferret-1j5, decision nug `1033d832a44a`) holds and sharpens: narrated cause vs priced cost. The built-in report now narrates the cause well enough that ferret should only price, verify, and find structure.

This file is rewritten, not appended.

## roadmap

Route order lives in `ROADMAP.md` at the repo root — one home, parsed by the SessionStart hook. Progress derives live from `bd epic status`.
