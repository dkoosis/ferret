# ferret-kuv.8 — Terminal-action-as-success outcome signal — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Status:** DRAFT — dk approves before any code. The bead is `has_design_questions=true`; the **Open Questions** section below MUST be resolved (esp. Q1 deploy-detection scope and Q2 attribution-window semantics) before implementing Task 2+.

**Goal:** Mine a deterministic positive-outcome label already present in the log — a task whose owned tool-call stream ends in (or is immediately followed by) a commit/push/PR action gets a `terminalAction` success flag — and attach it to the per-task segment unit as ONE WEAK FEATURE, never promoted to ground truth.

**Architecture:** Deterministic, reference-free, LLM-free Go scorer in the new `internal/score/` package (shared-design Decision 2). It reuses the existing VCS classifier (`internal/lens/lenses.go:57`) to recognize terminal actions; it attaches a boolean+evidence label to the `segResult`/`segment` unit (shared-design Decision 3) — it does NOT invent a `Finding` kind and does NOT touch `internal/conform`. Borrowed prior-art: tau-bench's terminal-action-as-outcome / pass^k framing (arXiv:2406.12045), cited inline.

**Tech Stack:** Go (stdlib + existing ferret packages: `internal/lens` for VCS classification, the `cmd/ferret/segment.go` segmenter for the per-task unit), kong CLI, table-driven tests (ADR-008).

## Global Constraints

- **Engine/analyst split (epic invariant):** ferret-side code is deterministic and LLM-free; semantic judgement is the analyst's. Terminal-action detection is pure string classification over the tool stream — deterministic by construction.
- **WEAK-FEATURE guard (bead REVISED note 2026-06-17):** commit/push/deploy ≠ task success (WIP commits, push-then-revert, broken deploys). The label is ONE WEAK FEATURE among inputs; **dk's labels remain the only ground truth** (kuv.4/kuv.6). The label MUST surface as a flag, never promoted to ground truth or a calibration oracle. Its own bias must be measurable against dk's labels before use — so the emission carries the EVIDENCE (which call, which action) so a human can audit each label, not just a bare boolean.
- **Cite borrowed algorithms in code** (epic convention, nug `cb1c87f041dc` / memory `ferret-cite-algos-in-code`): cite tau-bench terminal-action / pass^k (arXiv:2406.12045) inline in the scorer's package/func doc. Keep discard/rationale OUT of code → bead/nug.
- **Byte-stable output** (segment.go contract): the label is a pure function of the transcript bytes — no time, no map iteration order, no randomness. Repeated runs on the same input are byte-identical.
- **Additive schema only** (api-contract risk marker): the label adds an `omitempty` field to the segments/spine JSON consumed by the analyst bundle (kuv.3/4/5, 567). Additive — existing consumers keep parsing; tool/shell rows without the field stay byte-identical.
- **Do NOT touch:** `internal/conform/**` (kuv.4, frozen for this epic — feed the label as input, do not reweight its scoring), `internal/mine/**` (corpus-discovery engine, out of scope), and do not invent a new `FindingKind`.

---

## Context / prior art

**Shared design doc (AUTHORITATIVE):** `docs/design/ferret-kuv-deterministic-scorers.md` resolves three cross-bead decisions this plan obeys:

- **D1 — User-turn text source:** prompt text is captured ONCE in the `Event` artifact (`Event.Text`). **kuv.8 does not need user text** — terminal actions are tool/shell calls, not prompt language — so D1 is informational here, not a dependency. No `Event.Text` read required.
- **D2 — Where scorers live:** a NEW package `internal/score/` for reference-free / self-reference per-task scorers. NOT `internal/mine/` (corpus-frequency-shaped) and NOT `internal/conform/` (requires an analyst reference plan). The terminal-action scorer is reference-free per-task → it belongs in `internal/score/`.
- **D3 — Finding model:** per-task scores & outcome labels ride the `segResult`/segment unit, NOT `Finding`. The terminal-action label is a per-task outcome — it attaches to the segment, is rendered by the segment/score output path, and must NOT be forced into `Finding` or given a new Kind.

**⚠ Conflict with the bead's own sweeper-enrichment notes (flag, do not silently follow):** the bead's "Sweeper enrichment" section (written before the shared-design doc, which is newer — commit `8399b2d`) says to model a NEW sibling file `cmd/ferret/outcome.go` on `segment.go` and "wire its consumption into `internal/conform`." The shared design WINS:
- The scorer LOGIC lives in **`internal/score/`**, not `cmd/ferret/outcome.go`. Per D2, reusable scoring logic moves OUT of `cmd/` into `internal/score/`; `cmd/` stays a thin dispatch shell. The sweeper's "new sibling file under cmd/" instinct is superseded.
- Do **NOT** wire consumption into `internal/conform` in this bead. `conform/` is frozen (D2); kuv.4 is CLOSED. The label is *available for* kuv.4/kuv.5 to consume later, but this bead only PRODUCES and EMITS it on the segment/spine unit. (See Q3.)
- The sweeper's REUSE instinct — use `internal/lens/lenses.go:57` VCS classification, do NOT re-invent commit/push/PR matching — is CORRECT and retained.

**Template to mirror — `internal/conform/conform.go`:** the canonical thin, LLM-free, citation-in-code deterministic scorer. It cites its algorithm sources inline (Adriansyah; van der Aalst/Rozinat), keeps the package pure-arithmetic, leaves semantic work analyst-side, exposes self-contained result types. Mirror this shape: pure functions, deterministic, a self-contained result struct, a package doc comment explaining the WHY and the WEAK-FEATURE guard.

**The per-task unit — `cmd/ferret/segment.go`:** `segment` (line 64) is the per-task scaffold (`FirstCall`/`LastCall` own tool-call indices in encounter order; `Shape []string` is the ordered tool-shape token sequence of the calls the task owns). `segResult` (line 77) is the whole-session emission. The terminal-action label hangs off `segment` as a new field; the segmenter populates it as it streams. NOTE: the segmenter today reads the **raw transcript** via `segmentSource` (line 485), not the `Event` artifact — so terminal-action detection must run over what the segmenter already has in hand (the per-call shape tokens / `blk.Name`), OR over the segment's `Shape` token slice after the fact. See Approach.

**The VCS classifier to REUSE — `internal/lens/lenses.go:41-63` (`coarse.Token`):** the existing deterministic classifier maps a shell event to `"vcs"` when `Action` is `git`, `git_*`, `gh`, or `gh_*` (line 57). Shell actions are normalized by `internal/shellnorm` to `git_<subcommand>` form (e.g. `git commit` → `git_commit`, `git push` → `git_push`; bare `gh pr create` → `gh` or `gh_pr`). **"deploy" is NOT in the vcs set today** — see Q1. Reusing this means the terminal-action predicate is grounded in the same vocabulary as the rest of ferret, not a parallel hand-rolled matcher.

**CLI registration pattern — `cmd/ferret/conformance.go` + `cmd/ferret/main.go`:** a subcommand is (1) a struct in the `var CLI` grammar (`main.go:209` `Conformance` / `main.go:195` `Segments` are the closest siblings — `Segments` reads a `--session` + `--root` + `--format text|json`), (2) a `case "<name>": err = cmdX()` arm in the dispatch switch (`main.go:312-316`), and (3) a `cmdX()` + text/JSON writers. **For kuv.8 the cleanest surface is NOT a new subcommand** — the label rides the EXISTING `ferret segments` output (and the spine), because it is a per-task field on a unit that already has a command. See Approach + Q3.

**Prior-art source (cite in code):** tau-bench (Yao et al., "τ-bench: A Benchmark for Tool-Agent-User Interaction in Real-World Domains", arXiv:2406.12045) frames task outcome by checking the final database/environment state a tool action leaves, and aggregates reliability as pass^k across trials. kuv.8 borrows the *terminal-action-as-outcome* idea (a state-mutating end action is evidence of task completion) deterministically. The pass^k consistency aggregation itself is kuv.5's job, not this bead — cite the framing, implement only the per-task terminal-action label here.

---

## Approach

Two layers, built bottom-up so each is independently testable:

1. **The scorer (`internal/score/terminal.go`)** — a pure, LLM-free predicate + labeler over a task's owned tool-call stream:
   - `IsTerminalAction(action string) bool` — reuses the VCS-class logic (git_*/gh_* commit/push/PR family). To avoid a circular or layer-crossing import, the predicate is grounded on the SAME rule `lens.coarse` uses for `"vcs"`; the cleanest reuse is to expose that rule (see Q4) or to mirror the small, well-tested prefix set with a code comment pointing at `lens/lenses.go:57` as the source of truth. The predicate narrows `"vcs"` to the *success-bearing* subset (commit / push / PR-create), NOT every git call (`git status`, `git diff` are reads, not terminal successes) — Q1/Q2 fix the exact set.
   - `Label(shape []string) Outcome` — scans a task's ordered shape tokens and returns an `Outcome{TerminalAction bool; Action string; CallOffset int}` recording WHETHER a terminal action closed the task, WHICH action, and WHERE in the task's call sequence (the evidence the WEAK-FEATURE guard requires for human audit). Deterministic: same shape → same Outcome.
   - All pure functions, table-tested. No I/O, no time, no randomness.

2. **Attach + emit on the segment unit** — wire the labeler into the existing segmenter so each `segment` carries its outcome:
   - Add an optional field to `segment` (`cmd/ferret/segment.go:64`): `Outcome *score.Outcome json:"outcome,omitempty"` (pointer + `omitempty` so tasks with no terminal action stay byte-identical in JSON).
   - Populate it in the segmenter's finalize step (`result()`, line 419) by calling `score.Label(seg.Shape)` per segment — `Shape` already holds the ordered tool-shape tokens the task owns, so no new transcript pass is needed.
   - Render it in both `writeSegmentsText` (a compact `[outcome:commit @call N]` line) and `writeSegmentsJSON` (already emits the whole `segResult`; the new field flows through automatically).

**Why no new subcommand:** the bead says "emit on the spine" and the label is a per-task field. `ferret segments` already emits the per-task unit the analyst bundle consumes (kuv.3/4/5, 567). Adding the field there is strictly additive and reaches every existing consumer for free; a standalone `ferret outcome` command would duplicate session resolution + segmentation for no gain. (If dk wants a dedicated command anyway — Q3 — it is a thin wrapper over the same `segmentSession` + a filter to terminal-action tasks.)

**Why narrow the VCS set:** the WEAK-FEATURE guard demands the label mean something. `"vcs"` includes `git status`/`git diff`/`git log` (reads). Treating any git call as "success" would be noise. The terminal-action subset is the state-MUTATING, completion-bearing actions: `git_commit`, `git_push`, `gh` PR-create (and `git_merge`?) — the exact membership is Q1/Q2.

---

## File-by-file changes

### Task 1: The terminal-action scorer (`internal/score/terminal.go`) (D2)

**Files:**
- Create: `internal/score/terminal.go` — `IsTerminalAction`, `Label`, the `Outcome` type, package doc.
- Create: `internal/score/terminal_test.go` — table-driven tests.
- Reference (do not edit): `internal/lens/lenses.go:57` — the VCS classifier this predicate mirrors / reuses.

**Interfaces:**
- Produces: `score.Outcome struct { TerminalAction bool; Action string; CallOffset int }` (JSON: `terminalAction` / `action` / `callOffset`). `score.Label(shape []string) Outcome`. `score.IsTerminalAction(action string) bool`.
- Consumed by: Task 2 (segmenter `result()`).

- [ ] **Step 1: Write the failing test.** Create `internal/score/terminal_test.go` with a table covering: a shape ending in `sh:git_commit` → `{TerminalAction:true, Action:"git_commit", CallOffset:<idx>}`; `sh:git_push` → true; `sh:gh` (PR create) → true per Q1; a shape with only `Read`/`Edit`/`sh:git_status` → `{TerminalAction:false}`; an empty shape → false; a shape with a terminal action mid-stream followed by more calls → still labeled (records the LAST terminal action's offset — Q2 decides last-vs-any). Mirror the `conform` and `shellnorm` test table style (ADR-008, testify-free if the repo is — check existing tests).
  ```go
  func TestLabel(t *testing.T) {
      tests := []struct {
          name  string
          shape []string
          want  Outcome
      }{
          {"commit closes task", []string{"Read", "Edit", "sh:git_commit"}, Outcome{TerminalAction: true, Action: "git_commit", CallOffset: 2}},
          {"push closes task", []string{"sh:git_push"}, Outcome{TerminalAction: true, Action: "git_push", CallOffset: 0}},
          {"read-only git is not terminal", []string{"sh:git_status", "sh:git_diff"}, Outcome{}},
          {"no vcs at all", []string{"Read", "Edit"}, Outcome{}},
          {"empty", nil, Outcome{}},
      }
      for _, tt := range tests {
          t.Run(tt.name, func(t *testing.T) {
              if got := Label(tt.shape); got != tt.want {
                  t.Errorf("Label(%v) = %+v, want %+v", tt.shape, got, tt.want)
              }
          })
      }
  }
  ```
- [ ] **Step 2: Run it, verify it fails** — `go test ./internal/score/ -run TestLabel`. Expected: FAIL (package/symbol not defined).
- [ ] **Step 3: Write the predicate + labeler.** Create `internal/score/terminal.go`. The shape tokens come from `callShapeTokens` (segment.go:275): built-in tools are bare names (`Read`), shell calls are `sh:<verb>` (`sh:git_commit`), MCP is `mcp:server.tool`. So `IsTerminalAction` strips a `sh:` prefix and tests the success-bearing VCS subset.
  ```go
  // Package score holds ferret's reference-free, per-task deterministic scorers
  // (shared-design Decision 2): outcome labels and quality axes that score a task
  // against itself or nothing, distinct from internal/conform (analyst-reference
  // replay) and internal/mine (cross-corpus frequency).
  //
  // Terminal-action labeling borrows τ-bench's outcome framing (Yao et al.,
  // "τ-bench", arXiv:2406.12045): a state-mutating end action (commit/push/PR) is
  // deterministic evidence a task completed. It is a WEAK FEATURE, not ground
  // truth — WIP commits, push-then-revert, and broken deploys all trip it; dk's
  // labels (kuv.4/kuv.6) remain the only ground truth. The label therefore carries
  // its evidence (which action, which call) so each one is human-auditable.
  package score

  import "strings"

  // Outcome is one task's terminal-action verdict. TerminalAction is the weak
  // success flag; Action and CallOffset are the evidence (which VCS action, at
  // which 0-based offset in the task's owned call sequence) so the label can be
  // audited against dk's ground-truth labels, never trusted blind.
  type Outcome struct {
      TerminalAction bool   `json:"terminalAction"`
      Action         string `json:"action,omitempty"`
      CallOffset     int    `json:"callOffset,omitempty"`
  }

  // terminalActions is the success-bearing subset of the lens "vcs" class
  // (internal/lens/lenses.go:57). It is NARROWER than "vcs": read-only git calls
  // (status/diff/log) are not completions. State-mutating end actions only.
  var terminalActions = map[string]bool{
      "git_commit": true, "git_push": true,
      "git_merge": true, // Q2: keep or drop
      "gh": true, "gh_pr": true, // gh pr create / gh pr merge — Q1
  }

  // IsTerminalAction reports whether a shape token (a built-in tool name or a
  // "sh:<verb>" shell token, per callShapeTokens) is a success-bearing terminal
  // VCS action. It mirrors the vcs classification at lens/lenses.go:57, narrowed
  // to completion-bearing subcommands.
  func IsTerminalAction(token string) bool {
      verb := strings.TrimPrefix(token, "sh:")
      return terminalActions[verb]
  }

  // Label scans a task's ordered shape tokens and returns the terminal-action
  // outcome: the LAST terminal action in the task (Q2) and its call offset, or a
  // zero Outcome when the task ended in no completion action.
  func Label(shape []string) Outcome {
      out := Outcome{}
      for i, tok := range shape {
          if IsTerminalAction(tok) {
              out = Outcome{TerminalAction: true, Action: strings.TrimPrefix(tok, "sh:"), CallOffset: i}
          }
      }
      return out
  }
  ```
- [ ] **Step 4: Run tests, verify they pass** — `go test ./internal/score/`. Expected: PASS. (Adjust the `gh`/`git_merge` table rows to match the Q1/Q2 resolution before this is final.)
- [ ] **Step 5: Commit** — `git add internal/score/terminal.go internal/score/terminal_test.go && git commit -m "feat(score): terminal-action outcome labeler (ferret-kuv.8 D2)"`.

### Task 2: Attach the outcome label to the segment unit + emit (D3)

**Files:**
- Modify: `cmd/ferret/segment.go:64` — add `Outcome *score.Outcome` field to `segment`.
- Modify: `cmd/ferret/segment.go:419` (`result()`) — populate per-segment via `score.Label`.
- Modify: `cmd/ferret/segment.go:519` (`writeSegmentsText`) — render the label.
- Modify: import block at `cmd/ferret/segment.go:11` — add `github.com/dkoosis/ferret/internal/score`.
- Test: `cmd/ferret/segment_test.go` (extend existing) — assert a segment whose calls end in `git commit` carries `Outcome.TerminalAction == true`; a read-only segment carries no outcome (nil).

**Interfaces:**
- Consumes: `score.Label(shape []string) score.Outcome`, `score.Outcome` (from Task 1).
- Produces: `segment.Outcome *score.Outcome` — non-nil only when the task ended in a terminal action; flows into `writeSegmentsJSON` automatically and the spine emission.

- [ ] **Step 1: Write the failing test.** In `cmd/ferret/segment_test.go`, add a case feeding a small in-memory transcript (mirror the existing segment_test fixtures — a user prompt then assistant `tool_use` blocks, the last a `Bash` with `git commit -m ...`) through `segmentSource` and assert the owning segment's `Outcome.TerminalAction` is true with `Action == "git_commit"`, and a control segment with only `Read` calls has `Outcome == nil`.
  ```go
  // in the existing table or a new TestSegmentOutcome:
  // build a segResult via the same path the other segment_test cases use, then:
  if got := seg.Outcome; got == nil || !got.TerminalAction || got.Action != "git_commit" {
      t.Errorf("Outcome = %+v, want terminalAction git_commit", got)
  }
  ```
- [ ] **Step 2: Run it, verify it fails** — `go test ./cmd/ferret/ -run TestSegmentOutcome`. Expected: FAIL (field `Outcome` undefined).
- [ ] **Step 3: Add the field.** In `cmd/ferret/segment.go`, in the `segment` struct (after `Conts`, line 73), add:
  ```go
  Outcome *score.Outcome `json:"outcome,omitempty"` // weak terminal-action success label (kuv.8); nil = task ended in no completion action
  ```
  and add `"github.com/dkoosis/ferret/internal/score"` to the import block (line 11).
- [ ] **Step 4: Populate it.** In `result()` (line 419), inside the existing `for i := range s.segs` loop that sums bytes, label each segment:
  ```go
  for i := range s.segs {
      totalIn += s.segs[i].InBytes
      totalOut += s.segs[i].OutBytes
      if o := score.Label(s.segs[i].Shape); o.TerminalAction {
          oc := o
          s.segs[i].Outcome = &oc
      }
  }
  ```
  (Only set the pointer when `TerminalAction` is true, so no-outcome segments stay `omitempty`-absent — byte-stable.)
- [ ] **Step 5: Render it in text.** In `writeSegmentsText` (line 532, inside the per-segment loop after the `Conts` loop), add:
  ```go
  if seg.Outcome != nil {
      fmt.Fprintf(bw, "  [outcome:%s @call+%d] terminal-action (weak success signal — NOT ground truth)\n",
          seg.Outcome.Action, seg.Outcome.CallOffset)
  }
  ```
- [ ] **Step 6: Run tests, verify they pass** — `go test ./cmd/ferret/`. Expected: PASS.
- [ ] **Step 7: Commit** — `git add cmd/ferret/segment.go cmd/ferret/segment_test.go && git commit -m "feat(segments): attach weak terminal-action outcome label per task (ferret-kuv.8 D3)"`.

### Task 3: Full quality gate + done-signal verification

**Files:** none new — verification only.

- [ ] **Step 1: Run the full gate** — `make check` (build + vet + nilcheck + race per CLAUDE conventions). Expected: green.
- [ ] **Step 2: Byte-stability spot check.** Run `ferret segments --session <fixture> --format json` twice and `diff` the output — must be identical (the byte-stable contract). Confirm a session containing a commit shows `"outcome"` on the owning task and that tool/shell-only tasks omit the field.
- [ ] **Step 3: Confirm the WEAK-FEATURE guard in output.** Verify the text rendering labels the signal "weak success signal — NOT ground truth" and the JSON field is named `terminalAction` (not `success`/`passed`), so no downstream consumer mistakes it for an oracle.
- [ ] **Step 4: Commit any gate fixes**, then update the bead: `bd update ferret-kuv.8` with a note that the deterministic label ships; consumption into kuv.5/quality-axes is downstream (Q3).

---

## Test strategy

- **Unit (`internal/score/terminal_test.go`):** table-driven over `IsTerminalAction` and `Label` — terminal vs read-only git, gh PR, MCP/built-in non-VCS, empty shape, mid-stream vs trailing terminal action (last-wins per Q2). Pure functions → exhaustive cheap coverage, target 100% of the new package (matches the kuv.4 conformance precedent).
- **Integration (`cmd/ferret/segment_test.go`):** feed an in-memory transcript through `segmentSource` and assert the label lands on the correct owning segment (commit attributed to the task that issued it, via the existing `Shape` attribution), and absence → nil Outcome. Reuse the existing segment_test fixtures/builders.
- **Byte-stability:** the existing segment determinism guarantee covers the new field automatically (it is a pure function of `Shape`); add no time/map-order dependence. Spot-checked in Task 3.
- **No** conform/mine test touched — those packages are unmodified.

---

## Open Questions (for dk)

**Q1 — Is "deploy" in scope, and how is a PR/deploy action recognized?** The bead title and description say "commit/push/deploy"; the sweeper note flags that "deploy" is NOT in the lens vcs set and must be extended *deliberately and cited* if in scope. Deploy is also not a single normalized verb — it's `gh workflow run`, `kubectl apply`, `vercel`, `fly deploy`, `make deploy`, etc., with no deterministic signature. **Proposed default (pick one):**
  - (a) **v1 = git/gh only** (`git_commit`, `git_push`, `gh` PR-create/merge); defer deploy to a follow-up bead — keeps the predicate deterministic and grounded in the existing vcs classifier. *(my recommendation — deploy detection is a separate, fuzzy problem and would dilute the weak-but-clean signal.)*
  - (b) include an explicit, cited deploy allow-list now (`make_deploy`, `gh_workflow`, …) accepting that it is incomplete.

**Q2 — Attribution window + "terminal" definition.** The bead says "an agent turn immediately followed by commit/push" and "attribute success to the task/turn that PRECEDES them." Two sub-questions:
  - (a) **Which calls count as terminal — last-in-task, or any-in-task?** This plan labels by the LAST terminal action in the task's owned calls (a commit mid-task that's followed by more edits is weaker evidence than one that closes the task). Confirm last-wins, or should it be "any terminal action present"? Also: should `git_merge` count (it's a completion but often mid-flow)?
  - (b) **Cross-boundary attribution.** The bead's "immediately followed by" implies a commit in the NEXT segment might still credit the PREVIOUS task. The `Shape`-based approach credits the task that OWNS the commit call (the segmenter's existing attribution). Is owning-task attribution sufficient, or is a look-behind across the boundary required? (Owning-task is simpler and matches the segment unit; recommend it for v1.)

**Q3 — Surface: ride `ferret segments`, or a dedicated `ferret outcome` command + conform/kuv.5 wiring?** This plan emits the label on the existing `segments`/spine unit (additive, reaches kuv.3/4/5/567 for free) and does NOT wire it into `internal/conform` (frozen) or quality-axes (kuv.5, separate bead). The bead's sweeper note wanted a `cmd/ferret/outcome.go` + conform consumption — superseded by D2/D3 (see Context). Confirm: is emitting on the segment unit enough for this bead, with consumption deferred to the consuming beads (kuv.5)? Or does dk want a dedicated subcommand now?

**Q4 — Reuse mechanism for the vcs rule.** `IsTerminalAction` mirrors the prefix logic at `lens/lenses.go:57`. Three options: (a) duplicate the narrow success-subset set in `score/` with a comment pointing at the lens as source-of-truth *(this plan's default — the success-subset is narrower than vcs anyway, so it isn't a literal duplication)*; (b) export a helper from `internal/lens` (e.g. `lens.IsVCS(action) bool`) and call it; (c) move the vcs vocabulary to a shared low-level package. (a) is least invasive; (b) is cleanest if dk wants a single source. Pick one.

**Q5 — Conflict ratification.** This plan deliberately OVERRIDES the bead's sweeper-enrichment notes per the shared-design doc (scorer in `internal/score/` not `cmd/ferret/outcome.go`; no `conform` wiring). Confirm the shared-design doc is authoritative here (it post-dates the enrichment and the task brief says so) — this is the one place the bead's own notes and the design doc diverge.

---

## Acceptance

From the bead's **Done signal** (all four must hold):

- [ ] `make check` green (build + vet + nilcheck + race per CLAUDE conventions).
- [ ] New test: terminal-action (commit/push/PR) detection attributes a positive-outcome label to the preceding segment/task; absence → no label.
- [ ] The label is emitted deterministically on the spine/segments output (byte-stable across runs, per the segment.go contract).
- [ ] WEAK-FEATURE guard honored: the label is surfaced as a flag with auditable evidence (which action, which call), NOT promoted to ground truth / a calibration oracle, and NOT wired to reweight conform scoring.

Plus the design constraints:
- [ ] Scorer lives in `internal/score/` (D2), label rides the segment unit (D3), no new `FindingKind`, `internal/conform` and `internal/mine` untouched.
- [ ] tau-bench (arXiv:2406.12045) cited inline as the borrowed framing; discard-rationale kept out of code.
