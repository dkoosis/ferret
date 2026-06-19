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

// terminalAction reports the LAST terminal VCS action in a segment's shape
// tokens, if any. Last, not first, because the signal is "the action that
// terminated the task" — the closing commit/push, even when earlier VCS calls
// (a diff, a status) preceded it. Shape shell tokens are "sh:<cmd>"; the cmd is
// gated by lens.IsVCS (the reused family classifier) and then narrowed to the
// terminal subset. Non-shell tokens (Read, Edit, mcp:…) are skipped.
func terminalAction(shape []string) (signal string, ok bool) {
	for _, tok := range shape {
		cmd, isShell := strings.CutPrefix(tok, "sh:")
		if !isShell {
			continue
		}
		if lens.IsVCS(cmd) && terminalVCS[cmd] {
			signal, ok = tok, true // keep scanning → last match wins
		}
	}
	return signal, ok
}
