# north star — ferret

★ ferrets out tool-use friction and pathological interaction patterns in Claude Code logs so we can intentionally learn and improve

*Seeded 2026-07-19. ⊕ drafted by Claude, ratified by dk 2026-07-19. ★ line rewritten by dk 2026-08-24.*

AX-first: the primary consumer is Claude itself. ferret's traces are ground truth for the recall-eval loop (trixi-bot's blocking gate harvests fixtures from them) and for friction-hunting in dk's sessions.

Anthropic's own `/insights` covers similar ground — friction and tool-waste findings from the same session corpus — and could look like overlap. It isn't: `/insights` narrates a cause in prose over a sampled window and labels its outputs "model-estimated"; ferret is deterministic over the whole corpus and ranks every finding by measured wasted bytes. Narrated cause vs. priced cost is the split, made explicit against a real alternative. `/insights` also reads sessions *for dk*; ferret reads them *for Claude*, feeding the recall-eval loop. (Ratified 2026-08-23, bead ferret-1j5, decision nug `1033d832a44a`.)

This file is rewritten, not appended.

## roadmap

*Route order, decision-owned — edited when the route changes, never as status. Progress derives live from `bd epic status` (parsed by the SessionStart hook: numbered lines, epic id first token).*

1. ferret-bbp — user-turn repair/acceptance tagger reads intent from the human's words
2. ferret-kuv — intent-grounded tool-improvement harness
3. ferret-wf9 — in-session feedback tap: solicit t=0 human labels inline to calibrate the scorers
4. ferret-097 — ferret closes its own loop: rank burners/misfires, tune, verify the delta
