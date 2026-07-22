package score

import (
	"strings"

	"github.com/dkoosis/ferret/internal/lens"
)

// Terminal-action-as-success outcome label (ferret-kuv.8).
//
// The idea (prior-art item 8(b), nug cb1c87f041dc): a positive-outcome signal is
// already latent in the log — a task that ends by committing/pushing/opening a PR
// "shipped" something, independent of chat sentiment. So a task that OWNS a
// terminal VCS action gets a deterministic, reference-free, LLM-free success flag,
// attributed to the task that the action terminates (the segment scaffold already
// owns its calls, so attribution is just "which segment holds the call").
//
// This is ONE WEAK FEATURE, NOT ground truth and NOT a calibration oracle: a
// commit can be WIP, a push can be reverted, a PR can be abandoned. dk's labels
// (kuv.4/kuv.6) remain the only ground truth; this label's own bias must be
// measured against them before use. It is surfaced as a flag (nil = silence, not
// a negative label) and never folded into conformance fitness/precision.
//
// Reuse: lens.IsVCS (internal/lens) is the single source of truth for the VCS
// family — the same classifier the coarse lens uses, so the label can never drift
// from it on what counts as a VCS call. But the family gate is too broad on its
// own (it is true for read-only git_status/git_diff, which signal nothing), so we
// narrow to the mutating/terminal subset below. "deploy" is deliberately NOT in
// scope: it is absent from the VCS set today, and adding it would mean detecting
// non-VCS deploy tooling — a separate, deliberate extension this bead does not make.

// terminalVCS is the mutating/terminal subset of the VCS family that reads as a
// "shipped" signal — the normalized shellnorm forms (base_subcommand). git_push
// and gh_pr (create/merge) are the strongest; git_commit and git_merge ship work
// locally. Read-only VCS (git_status, git_diff, git_log, gh_pr_view→gh_pr is kept
// broad here intentionally — gh's subcommand depth stops at "pr") is excluded by
// not appearing here. Keep this tight: every entry must be an action that PRODUCES
// a durable artifact, never one that only inspects.
var terminalVCS = map[string]bool{
	"git_commit": true, // local checkpoint of work
	"git_push":   true, // published to the remote
	"git_merge":  true, // integrated a branch
	"gh_pr":      true, // gh pr create / gh pr merge — opened or landed a PR
}

// terminalTracker is the durable-artifact subset of bd (beads) actions —
// ferret-bbp.21, the 4th multiplicand joining terminalVCS: a task can ship a
// tracked artifact instead of a commit (mint a bead, close a bead) and that IS a
// shipped signal in dk's workflow, same shape as a commit/push. Normalized forms
// are shellnorm's base_subcommand ("bd create" -> "bd_create"), confirmed against
// internal/reach/reach_test.go's bd_update fixture.
//
// bd_update is deliberately EXCLUDED: it fires on every status flip (in_progress,
// blocked, priority, comment) — chatty, not terminal, too weak a tell even for
// this already-weak label. Only minting or closing the artifact counts.
var terminalTracker = map[string]bool{
	"bd_create": true, // minted a tracked issue — the artifact now exists
	"bd_close":  true, // closed a tracked issue — the artifact shipped
}

// Outcome is the weak positive-outcome label for one task. Positive is always
// true when the label is present (absence is represented by a nil *Outcome on the
// Segment, so there is no negative label — only "shipped" or silence). Signal is
// the firing shape token (e.g. "sh:git_commit") for traceability.
type Outcome struct {
	Positive bool   `json:"positive"`
	Signal   string `json:"signal,omitempty"`
}

// LabelOutcomes annotates every segment in res with its terminal-action outcome
// label, in place. A pure function of res.Segments[*].Shape: same input → same
// output, so it preserves the byte-stable acceptance contract. A segment with no
// terminal VCS action is left with a nil Outcome (absence, not a negative label).
func LabelOutcomes(res *Result) {
	for i := range res.Segments {
		if sig, ok := terminalAction(res.Segments[i].Shape); ok {
			res.Segments[i].Outcome = &Outcome{Positive: true, Signal: sig}
		}
	}
}

// terminalAction reports the LAST terminal action (VCS or tracker family) in a
// segment's shape tokens, if any. Last, not first, because the signal is "the
// action that terminated the task" — the closing commit/push/bead-close, even
// when earlier calls (a diff, a status, a bd_update) preceded it. Shape shell
// tokens are "sh:<cmd>"; VCS commands are gated by lens.IsVCS (the reused family
// classifier) then narrowed to the terminal subset, tracker commands are a flat
// lookup (bd is not a VCS family). Non-shell tokens (Read, Edit, mcp:…) are
// skipped.
func terminalAction(shape []string) (signal string, ok bool) {
	for _, tok := range shape {
		cmd, isShell := strings.CutPrefix(tok, "sh:")
		if !isShell {
			continue
		}
		if (lens.IsVCS(cmd) && terminalVCS[cmd]) || terminalTracker[cmd] {
			signal, ok = tok, true // keep scanning → last match wins
		}
	}
	return signal, ok
}
