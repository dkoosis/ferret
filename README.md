# ferret

Mines Claude Code transcripts (`~/.claude/projects/**/*.jsonl`) for repeated behavior: scriptable routines, friction loops, and noisy context. AX-first — the primary consumer is Claude itself.

```
ferret ingest                          # 1.4GB raw → ~36MB ~/.ferret/events.jsonl (~15s)
ferret summary [-by project|session]   # corpus health, tool mix, failure rates
ferret ngrams  [-lens coarse|tool|target|exact] [-n 2-5]
ferret graph   [-loops] [-format mermaid|dot]
ferret tokens  -session PREFIX         # one session's token stream (lens debugger)
```

Everything takes `-format json`, `-limit`, `-max-bytes`. Truncation is never silent.

## Design

```
raw logs → canonical events → tokens (lenses) → patterns → ranked output
```

- **Tokenization is the product.** Lenses re-slice the same events at different granularity; the artifact makes re-slicing a seconds-long loop.
- Streams keyed `(session, agent)` — subagent transcripts never interleave into the parent timeline.
- Failed actions tokenize as `tok!`; runs collapse to `tok+` (trivia suppression).
- Order comes from file position, never timestamps (some event types carry none).
- Compound bash split via `mvdan.cc/sh` AST; `git checkout -b x` → `git_checkout`.
- Noise floor: min-count, min-sessions, closed-gram suppression (a gram dies when its extension keeps ≥80% of its count).

v1 backlog: PrefixSpan, Smith-Waterman session similarity, Sequitur, PPM-lite surprise scoring, savings estimates, review queue. Plan: vault `Project/dk/ferret/docs/plans/2026-06-10-ferret-v0.md`.
