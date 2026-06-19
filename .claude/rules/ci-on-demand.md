# CI On-Demand — opt expensive CI in at PR time

*Codex review (OpenAI spend) no longer runs on every PR. Claude opts a PR in when the diff warrants it, by commenting the trigger phrase. Default OFF; unsure → don't opt in.*

## Codex review — comment `@codex review`

Advisory; does **not** gate merge (`check` is the gate).

```bash
gh pr comment <PR#> --body "@codex review"
```

| Request when diff has | Skip for |
|---|---|
| new/changed logic w/ branching or edge cases | docs / config / test-only |
| concurrency / lifecycle / goroutine code | mechanical renames/moves, `s/this/that/g` sweeps |
| persistence / write paths (store, reconciler, embeddings) | dependency bumps |
| scoring / retrieval changes | one-liners |
| security-adjacent (auth, SSRF, input handling) | generated code |
| large / sprawling change | reverts / cherry-picks of already-reviewed work |
