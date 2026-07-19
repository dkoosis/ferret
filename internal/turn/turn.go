// Package turn holds the deterministic, transcript-level classification of a user
// turn: lifting the genuine prompt text out of a content-block list (PromptText)
// and deciding whether that prompt is a real task boundary or a non-boundary that
// continues the current one (ClassifyBoundary). These are low-level transcript
// concepts with no scoring semantics, so they live here — below internal/score —
// and are shared by both the segmentation engine (internal/score) and the recall
// autopsy (internal/reach) without either reaching UP into the other.
package turn

import (
	"regexp"
	"strings"

	"github.com/dkoosis/ferret/internal/transcript"
)

// blockTypeText is the transcript content-block type for plain text. Kept local
// so this package does not couple to cmd's spine renderer for one constant.
const blockTypeText = "text"

// kindCarrier is the classification for a non-dialogue system envelope folded into
// the live segment (local-command, task-notification, compaction summary, teammate
// relay, bash I/O echo, interrupt/image marker).
const kindCarrier = "carrier"

// compactionSummaryOpener is the leading phrase of Claude Code's post-compaction
// carrier turn — the summary the harness injects as a user message when a session
// is continued after running out of context. Unlike a <local-command-…> envelope it
// arrives as a bare text block, so PromptText lifts it like a real prompt and it
// would otherwise open a spurious task boundary (smoke FP, turn 15). It is a system
// envelope, not user intent, so ClassifyBoundary folds it as a "carrier". Matched as
// a lowercased prefix: distinctive enough that no genuine user prompt collides, while
// tolerating the variable summary body that follows.
const compactionSummaryOpener = "this session is being continued from a previous conversation"

// wholeImageMarkerRe matches Claude Code's pasted-image placeholder, e.g. "[Image #1]".
// A user turn is frequently just this marker (a bare paste) OR the marker riding
// in front of a genuine instruction ("[Image #1] fix this"). wholeImageMarkerRe
// tests the former — the whole (trimmed) prompt is one or more markers and nothing
// else — so only a marker-only turn folds; an embedded marker keeps its turn
// (else real dialogue is dropped, Phase A P1 trap). Detection only here; the span
// is stripped for tagging by dialogue.TagMove, not by the boundary pass.
var wholeImageMarkerRe = regexp.MustCompile(`(?i)^(\s*\[image(\s+#\d+)?\]\s*)+$`)

// affirmations are whole-prompt acknowledgements that CONTINUE the current task
// rather than open a new one ("short affirmations don't change topic" — prior art:
// claude-code-log-analyzer arc_analyzer.py Phase 2). Matched by exact equality on
// the normalized prompt, so "yes, but also do X" stays a boundary — only a bare
// "yes" folds in.
var affirmations = map[string]bool{
	"yes": true, "y": true, "yeah": true, "yep": true, "yup": true,
	"ok": true, "okay": true, "k": true, "sure": true, "yes please": true,
	"proceed": true, "continue": true, "go": true, "go ahead": true,
	"do it": true, "go for it": true, "sounds good": true, "please do": true,
	"please": true, "lgtm": true, "looks good": true, "approved": true,
	"ship it": true,
}

// controlCommands are no-op session-control built-ins (no plugin namespace) that
// carry no task work, so they CONTINUE the current segment. Namespaced work
// commands (/wrap:wrap, /lintbrush:clean) are NOT here and open a real task —
// the hand-run reads /wrap and /lintbrush:clean as distinct tasks F and G.
var controlCommands = map[string]bool{
	"clear": true, "exit": true, "quit": true, "compact": true,
	"resume": true, "help": true, "model": true, "login": true,
	"logout": true, "cost": true, "status": true, "doctor": true,
	"config": true, "vim": true, "terminal-setup": true,
}

// ClassifyBoundary reports whether a user prompt is a NON-boundary that continues
// the current segment, with its class and a compact label. Three deterministic
// classes: "carrier" (a system/local-command envelope, not user intent), "control"
// (a no-op built-in slash-command), "affirmation" (a short closed-set ack). Any
// other prompt — including namespaced work commands — opens a boundary.
func ClassifyBoundary(prompt string) (skip bool, kind, label string) {
	trimmed := strings.TrimSpace(prompt)
	low := strings.ToLower(trimmed)

	if strings.HasPrefix(low, "<local-command-") || strings.HasPrefix(low, "<task-notification") {
		return true, kindCarrier, carrierLabel(low)
	}
	// Phase A (bbp.7) — three non-dialogue carrier buckets riding the prompt field:
	//   teammate-relay + bash I/O echo fit the <…> envelope → carrierLabel, whole-turn
	//   skip. Interrupt/image markers have NO envelope → fixed labels (carrierLabel
	//   would degrade to the useless "carrier"). ~14-16% of raw follow-up turns are
	//   these artifacts, not dialogue — pre-filter, don't tag.
	if strings.HasPrefix(low, "<teammate-message") ||
		strings.HasPrefix(low, "<bash-input>") ||
		strings.HasPrefix(low, "<bash-stdout>") ||
		strings.HasPrefix(low, "<bash-stderr>") {
		return true, kindCarrier, carrierLabel(low)
	}
	if strings.HasPrefix(low, "[request interrupted") {
		return true, kindCarrier, "interrupt-artifact"
	}
	// Whole-turn image marker only: an embedded marker ("[Image #1] fix this") keeps
	// its turn and opens a boundary — the marker span is stripped downstream, not here.
	if wholeImageMarkerRe.MatchString(trimmed) {
		return true, kindCarrier, "image-marker"
	}
	if strings.HasPrefix(low, compactionSummaryOpener) {
		return true, kindCarrier, "compaction-summary"
	}
	if name, ok := commandName(trimmed); ok {
		if !strings.Contains(name, ":") && controlCommands[name] {
			return true, "control", "/" + name
		}
		return false, "", ""
	}
	if affirmations[strings.TrimRight(low, " .!,?…")] {
		return true, "affirmation", trimmed
	}
	return false, "", ""
}

// IsAffirmation reports whether a whole turn is a closed-set acknowledgement (a
// bare "yes" / "ok" / "go ahead"), using the same affirmations set and
// normalization ClassifyBoundary folds on — so any consumer that needs the bare-ack
// test (e.g. score's confirmation-waste friction metric) can never drift from what
// counts as a fold-worthy affirmation.
func IsAffirmation(prompt string) bool {
	low := strings.ToLower(strings.TrimSpace(prompt))
	return affirmations[strings.TrimRight(low, " .!,?…")]
}

// commandName extracts a slash-command's name from a <command-name>…</command-name>
// envelope, lowercased with the leading slash stripped (e.g. "/wrap:wrap" -> the
// namespaced "wrap:wrap"; "/clear" -> "clear"). ok is false when no such tag.
func commandName(prompt string) (name string, ok bool) {
	const open, closeTag = "<command-name>", "</command-name>"
	_, after, found := strings.Cut(prompt, open)
	if !found {
		return "", false
	}
	inner, _, found := strings.Cut(after, closeTag)
	if !found {
		return "", false
	}
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(inner), "/"))
	return name, name != ""
}

// carrierLabel returns the leading tag name of a system envelope (the lowercased
// prompt), e.g. "<local-command-stdout>…" -> "local-command-stdout".
func carrierLabel(low string) string {
	if i := strings.IndexByte(low, '>'); i > 1 {
		return strings.Trim(low[:i+1], "<>")
	}
	return kindCarrier
}

// PromptText extracts the genuine user-prompt text from a content block list:
// the concatenation of its text blocks, whitespace-collapsed. A list with no text
// block (e.g. a pure tool_result carrier) returns "" — not a boundary.
func PromptText(blocks transcript.Blocks) string {
	var parts []string
	for i := range blocks {
		if blocks[i].Type != blockTypeText {
			continue
		}
		if body := strings.TrimSpace(blocks[i].Text); body != "" {
			parts = append(parts, body)
		}
	}
	return collapseWS(strings.Join(parts, " "))
}

// collapseWS folds any run of whitespace (including newlines) into single spaces
// so a value stays on one line. Mirrors score's spine helper of the same name —
// kept local so this package carries no dependency on the score layer.
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
