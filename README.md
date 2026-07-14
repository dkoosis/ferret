# ferret

Mines Claude Code transcripts (`~/.claude/projects/**/*.jsonl`) for repeated behavior: scriptable routines, friction loops, and noisy context. AX-first — the primary consumer is Claude itself.

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
ferret report   [--kind routine|friction|loop|noise] # findings → action verb, ranked by measured burn
ferret surprise [--lens tool]                         # per-session predictability (low=scriptable, high=thrash)
ferret graph    [--loops] [--format mermaid|dot]      # transition graph
ferret tokens   --session PREFIX                      # one session's token stream (lens debugger)
ferret reach    [--since Y-M-D] [--until Y-M-D]       # recall-opportunity reach-rate (memory keystone)
```

Everything takes `--data DIR` (default `~/.ferret`), `--format json`, `--limit`, `--max-bytes`. Truncation is never silent.

## Reach-rate (memory keystone metric)

`ferret reach` mechanizes the memory-recall autopsy for epic **tx-qw86** ("Memory where the action is"). It reads raw transcripts (not the events artifact), finds **recall-opportunity** moments — dk asking what's already known/decided/built (the always-loaded `recall.md` triggers: *do you remember · did we · I thought we · where do we stand · don't we already · what did we decide*) plus tx-vtea re-orientation asides (*where are we · I forget · remind me*) — and classifies what the agent reached for **first**: the trixi store (`get_nug`, `trixi search`/`get`), `bd` beads, grep/rg/Read, gh/git forensics, or nothing.

**Reach-rate = store-first reaches / opportunities**, every rate printed with its `n`. Weekly reports feed a rolling 3-week judgment window (single-week deltas are noise at tens of opportunities/week).

```
ferret reach --since 2026-07-03 --until 2026-07-05          # a fixed window (text scorecard)
ferret reach --format md --limit 20                        # trailing 7d, one ledger-appendable block
ferret reach --project trixi --format json                 # scoped, machine-readable
```

Default window is the trailing 7 days, so the weekly cadence is a bare `ferret reach --format md` appended to the Inquiry ledger — loop/cron-able, e.g. a weekly `launchd`/cron entry or `/loop 7d ferret reach --format md`.

Phase 1 is transcript-only (no telemetry). The Phase-2 **RU** column (was the reached result actually *used*?) joins trixi's `retrieval_events` telemetry (tx-kji6) onto these opportunities — the seam is `reach.JoinTelemetry`, a no-op until Phase 2.

## Lenses

Lenses re-slice the same canonical events at different granularity; pick with `--lens`.

| Lens | Token | Example |
|------|-------|---------|
| `coarse` | behavior class | `read`, `search`, `test`, `vcs` |
| `tool` | tool identity | `Read`, `sh:git_diff`, `mcp:trixi.set_nug` |
| `target` | tool + target class | `Edit:.go`, `Read:.md` |
| `exact` | tool + full normalized target | `Edit:internal/lens/lens.go` |

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
