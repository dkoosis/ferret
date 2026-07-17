# Agents

*Project agent policy. Replaces CLAUDE.md/AGENTS.md (bd-managed boilerplate + empty scaffolding — pointer content lives here now).*

## Git profile: Team-maintainer

This repo opts in. Commit, push, and open PRs freely as work completes — the PR is the review gate, not a pre-commit approval step. ✗ ask before committing or pushing. A current "do not commit/push" instruction in a given session still wins.

## Task tracking

`bd` (beads) for ALL task tracking — ✗ TodoWrite/markdown TODOs. `bd prime` for command reference; `bd remember` for persistent knowledge. Architecture: issues in a local Dolt DB, sync via `refs/dolt/data`, `.beads/issues.jsonl` is a passive export.

## Shell

Non-interactive flags on file ops (`cp -f`, `mv -f`, `rm -f`) — aliases may force `-i` and hang on a prompt.
