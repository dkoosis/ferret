# north star — ferret

★ ferrets out tool-use friction and pathological interaction patterns in Claude Code logs and suggests opportunities for improvement.

AX-first: the primary consumer is Claude itself. ferret's traces are ground truth for the recall-eval loop and for friction-hunting in dk's sessions.

Anthropic's own `/insights` covers similar ground — friction and tool-waste findings from the same session corpus — and could look like overlap. It isn't: `/insights` narrates a cause in prose over a sampled window and labels its outputs "model-estimated"; ferret is deterministic over the whole corpus and ranks every finding by measured wasted bytes. Narrated cause vs. priced cost is the split, made explicit against a real alternative. `/insights` also reads sessions *for dk*; ferret reads them *for Claude*, feeding the recall-eval loop. (Ratified 2026-08-23, bead ferret-1j5, decision nug `1033d832a44a`.)

This file is rewritten, not appended.

## roadmap

Route order lives in `ROADMAP.md` at the repo root — one home, parsed by the SessionStart hook. Progress derives live from `bd epic status`.
