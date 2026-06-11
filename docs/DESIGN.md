# Ferret — design intent & direction

_Captured 2026-06-11. Canonical narrative for the reframe below; the live work items are beads `ferret-z5c` (normalizer + descriptive validation) and `ferret-567` (metrics-engine product). Memory key: `ferret-intent-reframe-2026-06-11`._

## What ferret is actually for

Mine **dk's own Claude Code transcripts** to surface **automatable repetitive sequences** — work we'd want to:

1. **Automate** — the recurring sequence becomes a script / hook / macro.
2. **De-context the IO** — steps in the repeat that are *reads* (ls, find, search, open, cat) dump their output into the context window. A recurring sequence has *predictable* IO → cache it or keep it out of context so it stops crowding the window. (Token-economy win — possibly the bigger one.)

Grounded in informal experience: dk + Claude already notice, by gut, sequences that should be scripted. Ferret systematizes that intuition. **dk's judgment is the validator — not statistical lift.**

## The reframe (we were wrong about the yardstick)

The 80k SWE-agent run tested a **predictive** claim — _"friction/loop motif → task failure"_ — because SWE-agent ships ground-truth pass/fail outcomes. That claim is **falsified** on this corpus: every lift ≤ 1.01, faintly *inverted* (motif-bearing streams fail slightly less, an engagement/length confound — the motifs need ≥3 steps to manifest, so they select for sessions that did real work vs. an 83.3% baseline dominated by early flail-outs).

But **predicting task outcome was never the value.** A sequence can be uncorrelated with SWE pass/fail and still be exactly what you'd want to script or de-context in your own sessions. The falsification kills the predictive claim; it does not touch the **descriptive** one — _"recurring motifs = automation / de-context candidates"_ — which is the real goal and is still untested. Right test: run on `~/.claude/projects`, eyeball top candidates against sequences we already know are scriptable. No ground-truth needed.

## MVP: normalize → n-grams

Simplest valuable version, no lift / no PrefixSpan:

```
normalize commands → n-grams → same 5-gram recurring = signal
```

**The normalizer is the linchpin** and where the current code is weakest. Today's `tool` lens collapses bash to `sh:python` / `sh:ls` / `sh:find.` — too coarse *and* buggy (the `sh:find.` trailing-dot token is the normalizer leaking; it's the **canary** — fix it first to prove the parse works).

Real normalization = break bash into the actual command, canonicalize: strip volatile args (paths, line numbers, tmp names), keep command + meaningful flags. So `python run_tests.py` vs `python -m pytest` stay distinct, but `cat /tmp/a` / `cat /tmp/b` collapse. Garbage normalization → n-grams of noise; good normalization → the 5-grams are real.

## The product: ferret = metrics engine, Claude = analyst

Ferret is **not** the scorer / end-product. It's a **metrics engine** feeding Claude as the analyst. The loop:

```
ferret emits top-N candidate sequences + a metric bundle
  → Claude reads it, recognizes the worth-cleaning ones
  → Claude proposes optimizations (automate the routine / de-context the IO)
```

The LLM analysis **is** the product, not a fancy add-on.

### Existing algos are each a metric on a candidate (not separate quests)

| Algo | Metric it contributes |
|---|---|
| `ngrams` | recurrence of a fixed sequence — candidate generator |
| `seqs` (PrefixSpan) | gapped recurrence — variants n-grams miss |
| `rank` | top-N ranker (cohesion + FRICTION/LOOP buckets) — closest to product today |
| `surprise` | **thrash** metric (high bits/tok = thrashing) |
| `graph` | flow viz → add a **Sankey** format (alongside mermaid/dot); powers "oh yeah, I do that all the time" recognition |
| `tokens` | per-candidate **token cost** — context crowding |

Per-candidate bundle = **recurrence × token-cost × thrash × flow-link.**

### Missing metric to build

**Per-sequence token/byte cost — especially IO *output* weight.** `tokens` shows a stream, not "this 5-gram costs N tokens each occurrence." This is the metric the de-context payoff needs.

### Target emission

`ferret candidates` — top-N by each measure; one row = sequence + recurrence + token cost + thrash + flow link. Compact enough that Claude reads it and returns concrete proposals. A Sankey view for the human recognition pass.

## Build order

1. **Normalizer** — break bash → canonical command; fix `sh:find.` as the canary.
2. **n-grams on `~/.claude/projects`** — eyeball top 5-grams against gut-known scriptables (this is `ferret-z5c`'s validation, and the "see what they look like" exploration).
3. **Per-sequence token/IO cost metric.**
4. **`ferret candidates` bundle** — top-N by each measure (+ Sankey).
5. **Claude-analyst proposal loop** — read bundle, propose automate / de-context fixes.

## Open call

Falsified-for-SWE is settled. The live question is whether the **descriptive** claim holds on dk's own corpus once the normalizer is honest. That's the next run.
