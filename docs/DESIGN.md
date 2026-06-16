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

## Grounding: prior art on intent-from-traces (2026-06-16)

A literature pass (full review + sources: `PRIOR-ART-intent-from-traces.md`) across IR query
intent / task segmentation, e-commerce clickstream models, symbolic goal recognition,
process mining, and 2023–25 LLM agent-trajectory evaluation. One through-line, and it
sharpens the direction above.

**Every one of those fields exists to guess a latent intent that was never written down.**
Clicks, queries, actions under a formal model — all impoverished signal. Our traces break
that assumption in our favour: the user prompt and the agent's stated reasoning sit next to
every tool call. We don't infer the "why," we read it. So we take each field's machinery and
discard its guess-the-latent stance. This is the formal backing for "dk's judgment is the
validator, not statistical lift" — the intent is observed, not modeled.

Three consequences for ferret:

1. **The unit is the task, not the call or the motif.** IR settled by 2008 (Jones &
   Klinkner) that time/turn contiguity is a bad proxy for "same goal" — a ~70% ceiling, and
   75–90% of activity is interleaved multitasking. Counting tool-vs-tool substitution (rg vs
   snipe) is the exact behavioral signal that field abandoned. Segment a trace into tasks by
   *shared goal*, read from the stated reasoning; then assess a tool against the task it was
   serving.

2. **ferret is a process-discovery engine missing its conformance half.** Process mining
   (van der Aalst) splits into discovery (descriptive: what happens) and conformance
   (normative: does reality match a reference). ferret today is discovery only — it mines
   motifs. The missing half: replay a task against a reference (best practice, or the agent's
   own stated plan) and score **fitness/precision**, with alignments that localize the failed
   call. "A tool was invoked ≠ it served its purpose" is their flower-model warning verbatim.
   The reference can be derived from the agent's own words — which classic process mining,
   lacking semantics, never could.

3. **The quality axes already exist, reference-free.** TRACE (2025) scores trajectories on
   efficiency / hallucination / adaptivity = our friction-free / effective / recoverable.
   τ-bench's **pass^k** (does the same intent succeed across k reruns) is the measurable core
   of "delightful" — a good tool is *predictable*, not just occasionally lucky. Score goal
   progress with **landmarks** (necessary milestones defined informally from text), not formal
   plans. Caveat from AgentRewardBench: LLM judges over traces over-credit success — keep the
   human as validator.

Burn stays as a cost magnitude, demoted from headline. The headline is: did the tool serve
the task's intent, cleanly and consistently.

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

Falsified-for-SWE is settled and demoted to a footnote (see prior-art doc). Two live
questions now:

1. Does the **descriptive** claim hold on dk's own corpus once the normalizer is honest?
2. Can we segment a real session into tasks by stated intent, and judge whether one tool
   served each task — the conformance half? First try below / next.

### Try #1 — task segmentation on a real session (2026-06-16)

Session: `snipe/6aab91d5…` (Jun 15, 916 lines, 104 tool calls, 57 thinking blocks). Method:
extract a compact "spine" (user prompts + thinking + tool calls + result status, ~21KB from
a 1.2MB transcript), then segment by stated intent rather than by turn.

**Seven tasks, read straight from the prompts + reasoning:** (A) orient + assess the
snipe-ffj bead; (B) reframe to a confusion-matrix + write the Inquiry Brief; (C) read pass to
understand snipe's resolution/marker code; (D) implement the `! noembed` marker end-to-end;
(E) push; (F) wrap; (G) lint-clean + deploy.

**Segmentation by intent beat turn-segmentation, as the literature predicted.** Two
sub-goals interleave across non-adjacent turns: the Brief is edited in B, C, and D ("keep the
Brief current"); snipe's own code is read in A, C, and D. A turn/time segmenter splits these;
intent grouping keeps them. Per-task intent was legible from the thinking text verbatim
("I want one choke point", "check whether the Writer is built somewhere I can give it DB
access") — no inference needed, just reading.

**The finding conformance surfaces that a raw ratio buries.** Task C's stated intent: locate
where `decisionPath`/`Degraded`/the marker are built and rendered, find `FindSimilarSymbols`
call sites, find the Writer choke point — textbook navigational symbol queries, snipe's home
turf (`def`/`refs`/`callers`/`pack`). Tool actually used: ~20 `rg` + ~15 `Read`, **zero
snipe calls** — `rg -n "type SymbolRow struct"`, `rg "func pickSelectedSymbol"`,
`rg "writeClaudeMeta"` are all `snipe def`/`refs` queries verbatim. In a session whose
explicit goal was *improving snipe*, snipe was not chosen to navigate snipe. That is a
precise, localized "tool did not serve the intent it was built for" — invisible in a global
rg-vs-snipe ratio, sharp once scoped to the task.

Friction in task D was real but **not snipe's**: a `!`-in-grep zsh history-expansion bug
(Bash), an `Edit`-ERR→`Read`→`Edit` (the read-before-edit smell ferret already tracks), and
test-FK retries. Conformance attributes friction to the right call instead of smearing it.

**What the instrument couldn't do yet (the build list):** (1) spine extraction was a one-off
script, not a ferret subcommand; (2) task boundaries + intent labels were read by Claude by
hand, not emitted — this is the `ferret candidates`-style bundle that should carry per-task
intent; (3) no automatic "applicable tool for this intent" check (the snipe-shaped-query
detector the grep-nudge hook approximates) to flag the non-adoption; (4) the snipe repo's
own index/availability at the time wasn't confirmed — adoption only counts where the tool was
actually available.

**Takeaway:** the new loop (segment → read intent → assess tool vs task) produces a finding
the old burn/ratio framing could not. Next: make spine-extraction and per-task intent a
ferret emission, and add the applicability check so the non-adoption flags itself.
