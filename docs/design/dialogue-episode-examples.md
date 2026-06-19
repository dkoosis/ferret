# Dialogue episode examples (real corpus)

Five real **episodes** pulled from the transcript corpus — not invented. An episode
is the unit ferret-bbp scores: the ordered **user turns** of one task (= one segment
from the deterministic segmenter), rolled up into one PARADISE **outcome**.

Term of art: search/IR calls this a *query session* / *reformulation chain*
(Jones & Klinkner, CIKM'08); dialogue/CA calls it a *conversation* segmented into
*episodes*, with *repair sequences* inside. ferret's unit is the episode.

## How to read the tags

Per-turn **move** (`internal/dialogue.TagMove`), regex-first, repair-wins-over-accept:

- `repair` — the human signals trouble + redirects ("no", "not that", "what I meant", "wait", "NOT useful")
- `accept` — the human signals satisfaction ("got it", "thank you", "that is MUCH more useful", "yes")
- `neutral` — a new ask, a question, or a directive carrying no repair/accept cue

Episode **outcome** (`internal/dialogue.Classify`), v1 rules:

- ends on `accept`, repairs ≤ 2 → **success**
- ends on `accept`, repairs > 2 → **repair-heavy** (landed, at friction cost)
- repairs present, no closing `accept` → **abandoned**
- no `accept` and no `repair` → **unknown** (no outcome signal)

Quotes are verbatim. `[…]` trims length only.

---

## Episode 1 — success (repair, then it lands)

Source: trixi `6846a2e8`, turns 12–17. Task: "where should CC plugins be documented?"

| # | turn (verbatim) | move | cue |
|---|---|---|---|
| 12 | where do we document plugins? | neutral | — |
| 13 | no. what i mean is, where is the correct place to document claude code plugins | **repair** | "no." / "what i mean" |
| 14 | see also there is also a writing plugins (or skill?) skill | neutral | — |
| 15 | see if they provide any useful guidance about documenting | neutral | — |
| 16 | Be much more succinct. When I ask a one line question, and you provide a wall of text with five headings, that is NOT useful. | **repair** | "NOT useful" |
| 17 | that is MUCH more useful. thank you | **accept** | "thank you" |

repairs=2, closes on accept, 2 ≤ cut → **success**. Note turn 13's repair targets a
*wrong-scope answer*; turn 16's targets the *agent's verbosity* — two different trouble
sources, both genuine. This is the canonical "friction but resolved" shape.

---

## Episode 2 — repair-heavy (frustration, still lands)

Source: trixi `6a0af9dc`, turns 1–10. Task: a daily status question that went sideways.

| # | turn (verbatim) | move | cue |
|---|---|---|---|
| 1 | We're Where are we with regard to getting Go workflow set up as a task runner? | neutral | — |
| 2 | Does the road map make this clear, or are you having to research these answers | neutral | — |
| 3 | Wait a minute. Are you looking at the roadmap that's regarding agent orchestration | **repair** | "Wait a minute" |
| 4 | Listen, this is not working well at all | **repair** | "not working" |
| 5 | I'm asking a question I ask every day. I want […] a simple, authoritative answer. | **repair** | "I'm asking" (meta-correction) |
| 6 | whatever works. do it | neutral | (cede / directive) |
| 7–9 | (orientation questions, "eli5 how the spike relates…") | neutral | — |
| 10 | got it. keep going | **accept** | "got it" |

repairs=3, closes on accept, 3 > cut → **repair-heavy**. The task landed but the human
had to correct three times first — exactly the signal burn/loop-count alone can't see.
Teaching point: "Listen, this is not working" is *task-level dissatisfaction*, real
repair — not an emotion detector firing. (Contrast with the dev-jargon trap: "kill the
process", "the test is dead" are NOT affect — see the bbp jargon-trap test.)

---

## Episode 3 — the v1 GAP: resolved-by-directive, mislabeled "abandoned"

Source: trixi `ed2205bd`, turns 21–32. Task: pick a multi-agent working model.

| # | turn (verbatim) | move | cue |
|---|---|---|---|
| 21 | is teams relevant to us since they share a tree? | neutral | — |
| 22 | don't try and placate me, concentrate on getting a working environment […] | **repair** | "don't" |
| 23 | what I meant was, concentrate on getting to a plan in which you have confidence. | **repair** | "what I meant" |
| 30 | It may make sense to move to a unified server. our projects end up cross-referencing | neutral | — |
| 31 | you are being melodramatic about the concurrency issue. It's one person. | **repair** | (correction) |
| 32 | I'd try multi-agent w redirect to start | neutral | (decision / constrain) |

repairs=3, no closing `accept` → Classify returns **abandoned**. But the task did NOT
abandon — it *resolved* on turn 32's directive ("I'd try … to start"). v1 has no move for
a decision/constraint that settles a task without praise. **This is the case ferret-bbp's
cross-episode follow-on must fix**: absence of an explicit accept ≠ abandonment; a topic
shift to the *next* task is the real abandonment signal, and that's only visible across
episode boundaries (the segmenter sees it; a single move slice does not).

---

## Episode 4 — abandoned (approach killed, topic shifts)

Source: trixi `6846a2e8`, turns 18–25. Task: build a "coffee break" idle trigger like /recap.

| # | turn (verbatim) | move | cue |
|---|---|---|---|
| 18 | Please read […] tell me how it relates to our "idle" plugin trigger: <url> | neutral | — |
| 19 | do I understand correctly, that our approach […] is incorrect, and the better approach would be […] an inbuilt Claude Code capability […] | neutral | (probe — trouble surfacing) |
| 20 | yes, the goal was explicitly "act like recap". | neutral | — |
| 23 | To do anything that references the session data, would we need the session ID […]? | neutral | — |
| 24 | no. TBH what I'm seeing is that we went on a wild goose chase because we didn't RTFM | **repair** | "no." |
| 25 | do a post mortem with the goal of preventing this class of error […] | neutral | (new task) |

repairs=1, no closing `accept` → **abandoned**. This one is *correctly* abandoned: turn 24
names the trouble (whole approach wrong — "wild goose chase"), and turn 25 switches to a new
task (post-mortem) without the original ever landing. The genuine abandonment signal here is
the **topic switch on turn 25**, which v1's single-episode rule approximates via "repair, no
accept."

---

## Episode 5 — unknown (no outcome signal)

Source: ferret `e61f53d7`, turns 1–5. Terse execution lane, no repair or praise.

| # | turn (verbatim) | move | cue |
|---|---|---|---|
| 1 | status? | neutral | — |
| 2 | geting close.... | neutral | — |
| 3 | continue | neutral | — |
| 4 | continue | neutral | — |
| 5 | next? | neutral | — |

repairs=0, accepts=0 → **unknown**. The floor case: when the human runs a tight,
trusting execution loop, there's no linguistic outcome signal at all. ferret should *not*
manufacture one — outcome is `unknown`, and the finding falls back to the agent-side
burn/loop proxy. This is most of dk's own ferret sessions, which is exactly why bbp's
value shows up on the *messier* sessions above, not the clean ones.

---

## What this tells the design

1. **Capture is the prerequisite** — every quote above had to be re-parsed from raw CC
   transcripts because the prompt text isn't on the event yet (ferret-d01). A downstream
   consumer can't build this table from ferret output today.
2. **Single-episode rules undercount resolution** — Episode 3 shows `abandoned` is
   over-eager; directive-resolution and cross-episode topic-switch are the missing signals.
3. **Repair ≠ affect** — Episodes 2 and 4 carry strong language ("not working", "wild goose
   chase") that is task-level repair, not emotion. v1 stays lexical/structural by design.
