# Plan for review — make the ferret skill close the loop

_Drafted 2026-06-12. Executed same day. Companion to `docs/DESIGN.md`._

**Status: A, B, C, D2 shipped** (cc-plugins ferret skill v0.2.0). Schema decision: the fix
ledger uses the existing `metric.skill.ferret.outcome` nug pattern (trixi skill-metrics
taxonomy) rather than a new `reference.ferret-fix` flavor — structured body with the
verbatim motif as join key, so a future D1 CLI can parse it. D1 stays gated: bead filed,
blocked until a scan shows fixes actually get recorded and the eyeballed delta isn't enough.

## The problem

The ferret plugin (in `cc-plugins`) scans transcripts and proposes fixes, then stops. It
has no memory of what got fixed. Three gaps follow from that:

1. **No loop.** You fix `Edit!⇝Read` with a hook today; next month's scan re-surfaces it as
   if nothing changed. You can't tell whether a fix landed or whether the burn moved.
2. **No routing.** The skill says *what kind* of fix each finding wants (script, hook, trim)
   but not *where the fix lives* — cc-plugins, hookify, CLAUDE.md, or a rules file. A finding
   you can't route is a finding you won't act on.
3. **Sort confusion.** Findings rank by burn, and the reader treats burn rank as a to-do
   order. But the biggest burn is loops, which are the murkiest to fix; the clean wins sit
   lower. The skill should say: read top-down, act where the fix is clear.

Gap 1 is the real one. The other two are cheap fixes to the skill text.

## Proposed changes

### A. Routing map — skill text only (cheap)

Add a column to the bucket→action table in `SKILL.md`: where the fix lives.

| kind | action | lives in |
|---|---|---|
| routine | script | a cc-plugins slash command or skill |
| friction | hook | hookify rule, or `settings.json` PreToolUse |
| loop | trim | a CLAUDE.md / `~/.claude/rules` behavioral line — no artifact |
| noise | watch | nowhere yet |

**Risk:** none. Text only. **Effort:** minutes.

### B. Sort guidance — skill text only (cheap)

One line in the sort section: *burn rank is where to look, not what to fix first. The clean
wins (routine→script, friction→hook) often sit below the high-burn loops. Read top-down, act
where the fix is unambiguous.*

Already partly done in the last edit. Finish it.

**Risk:** none. **Effort:** minutes.

### C. Lens-as-question — skill + command text (cheap)

The skill and `/ferret:scan` default to the tool lens and never tell you to re-run with
another. Add: tool lens finds behavioral loops; target lens finds re-read waste; exact lens
finds scriptable runs. Run the lens that matches the fix you're hunting.

**Risk:** none. **Effort:** minutes.

### D. Close the loop — the real work. Two options, pick one.

The skill needs a durable record: *motif X was fixed by artifact Y on date Z*, checked
against burn-delta on the next scan.

**Option D1 — ledger in the CLI.** A new `ferret fixes` subcommand backed by
`~/.ferret/fixes.jsonl`:

```
ferret fixes add  --motif "Edit!,Read" --fix "hookify read-before-edit" --note "..."
ferret fixes list
ferret report --since-fixes   # annotate each finding: "fixed 2026-06-12, burn 253k→11k ↓"
```

- _Pro:_ burn-delta is computed where the burn is; survives across sessions; the report can
  flag a fix that didn't work (burn unchanged) or regressed (burn back up).
- _Con:_ CLI work — a bead, a subcommand, a schema. The report join needs the motif key to
  be stable across ingests (it is — motif is the sort key already).

**Option D2 — ledger as a nug.** The skill writes a `reference.ferret-fix` nug per fix and
reads them back at scan time, comparing by eye.

- _Pro:_ no CLI change; ships today; lives in the knowledge graph where dk already looks.
- _Con:_ burn-delta is eyeballed, not computed; no automatic "this fix didn't work" flag;
  the skill has to remember to query nugs each scan.

**Recommendation: D2 now, D1 if the loop proves it earns its keep.** D2 closes the loop
with zero CLI risk and tells us whether dk actually records fixes. If the habit sticks, D1
makes the burn-delta automatic and is worth a bead. If it doesn't stick, we saved the CLI
work. This matches the descriptive-validation stance in DESIGN.md — prove the behavior
before building the machine.

## Sequencing

1. A, B, C together — one skill/command edit pass. Ship with the next `/ship-plugins`.
2. D2 — add the nug read/write step to the skill. Same pass or the next.
3. D1 — file a bead (`ferret-`) only after D2 shows fixes get recorded. Not now.

## What I'd want dk to decide

- **D1 vs D2** — am I right that the cheap nug ledger comes first, or do you want the CLI
  ledger built straight away because you know you'll use it?
- **Routing to hookify** — friction fixes point at the hookify plugin. Confirm that's the
  intended home for PreToolUse guards, vs. raw `settings.json`.
- **Scope** — A/B/C are pure text and I can ship them now. Say the word and they go in the
  next plugin release; D waits on your call above.
