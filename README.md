# ferret

Mines agent transcripts — Claude Code (`~/.claude/projects/**/*.jsonl`) or SWE-agent trajectories — for repeated behavior: scriptable routines, friction loops, and noisy context. AX-first — the primary consumer is Claude itself.

## Install

```
make build          # or: go build -o ferret ./cmd/ferret
make help           # all targets (check, audit, install…)
```

## Quickstart

```
ferret ingest                               # 1.4GB raw → ~36MB ~/.ferret/events.jsonl (~15s)
ferret summary  [--by project|session]      # corpus health, tool mix, failure rates
ferret ngrams   [--lens tool] [--n 2-5]               # repeated n-grams
ferret seqs     [--lens tool] [--max-gap 3]           # gapped subsequences (PrefixSpan)
ferret rank     [--lens tool] [--top 10]              # ranked review queue, bucketed
ferret surprise [--lens tool]                         # per-session predictability (low=scriptable, high=thrash)
ferret graph    [--loops] [--format mermaid|dot]      # transition graph
ferret validate [--lens exact]                        # buckets × ground-truth outcomes (needs outcomes.jsonl)
ferret tokens   --session PREFIX                      # one session's token stream (lens debugger)
```

Everything takes `--data DIR` (default `~/.ferret`), `--format json`, `--limit`, `--max-bytes`. Truncation is never silent.

## Lenses

Lenses re-slice the same canonical events at different granularity; pick with `--lens`.

| Lens | Token | Example |
|------|-------|---------|
| `coarse` | behavior class | `read`, `search`, `test`, `vcs` |
| `tool` | tool identity | `Read`, `sh:git_diff`, `mcp:trixi.set_nug` |
| `target` | tool + target class | `Edit:.go`, `Read:.md` |
| `exact` | tool + full normalized target | `Edit:internal/lens/lens.go` |

## Outcomes

`validate` joins pattern buckets against per-stream ground-truth labels in `<data>/outcomes.jsonl` (`{"stream","target","exitStatus"}`). Labeled corpora write it at ingest (SWE-agent `target`/`exit_status`); CC ingest writes none — there is no ground truth in raw transcripts.

For acquiring the SWE-agent dataset (duckdb recipe, field tolerance): `testdata/README.md`.

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

Plan: vault `Project/dk/ferret/docs/plans/2026-06-10-ferret-v0.md`.
