package main

// buildProbeAdjustments is the read-side counterpart of feedback_answer.go's
// ask-rendered check (ferret-j33 §6, de-contamination): given the label
// ledger's OWN durable record of what dk answered, re-locate WHICH transcript
// line each answer landed on, so cmdHelped/feedback_judge can fold the raw
// probe-turn text out of segmentation (score.SegmentSourceExcluding) and out
// of the repair-adjacency dialogue tagger (sessionUserTurns' exclude param).
//
// No new join code: this walks the transcript exactly like `answer`'s own
// render check does — tracking the last-seen ASSISTANT text and testing
// label.Question as a substring of it — so a label's answer turn is the
// first not-yet-matched user line immediately following the assistant turn
// that rendered its question. The label ledger's own already-stripped Text
// field is reused verbatim as the adjustment's replacement (single source of
// truth, no re-derivation of the leading-token split).

import (
	"strings"

	"github.com/dkoosis/ferret/internal/label"
	"github.com/dkoosis/ferret/internal/transcript"
	"github.com/dkoosis/ferret/internal/turn"
)

// buildProbeAdjustments walks src once and returns the two maps ferret-j33's
// de-contamination hop needs, both keyed by the answer turn's transcript
// timestamp:
//
//   - adjust: replacement prompt text for score.SegmentSourceExcluding — the
//     label's own recorded remainder (label.Text), so the turn segments on
//     its clean work content instead of the raw "y - now fix the tests"
//     prefix. An empty remainder (a bare-token answer) still gets an entry
//     ("") — feed's user branch folds that to the same no-boundary carrier
//     path a tool_result envelope takes.
//   - exclude: for sessionUserTurns — the turn must be skipped OUTRIGHT
//     (never even tagged), since a probe answer's raw text ("no — it clearly
//     wasn't relevant") would otherwise masquerade as a genuine repair move.
//
// An Ignored label gets NO entry in either map: that turn was genuine dk
// work (an unrecognized answer, or none at all), and excluding it would be
// the exact class of bug the render-check guards against, just on the
// scoring side, not the labeling side.
// sessionProbeAdjustments is the shared cmdHelped/runFeedbackJudge wiring
// step: load the ledger, filter to this session's labels, and hand them to
// buildProbeAdjustments. Split out so both call sites build the
// (adjust, exclude) pair identically rather than re-deriving the load+filter
// step twice.
func sessionProbeAdjustments(src transcript.Source, data string) (adjust map[string]string, exclude map[string]bool, err error) {
	all, err := label.Load(label.Path(data))
	if err != nil {
		return nil, nil, err
	}
	labels := make([]label.Label, 0, len(all))
	for _, l := range all {
		if l.Session == src.Session {
			labels = append(labels, l)
		}
	}
	return buildProbeAdjustments(src, labels)
}

func buildProbeAdjustments(src transcript.Source, labels []label.Label) (adjust map[string]string, exclude map[string]bool, err error) {
	adjust = map[string]string{}
	exclude = map[string]bool{}
	matched := make([]bool, len(labels))
	var lastAssistantText string

	readErr := transcript.ReadLines(src.Path, func(line []byte) error {
		raw, ok := decodeRaw(line)
		if !ok || raw.IsMeta || raw.Message == nil {
			return nil // tolerant: a bad line is skipped, not fatal (mirrors segmenter/sessionUserTurns)
		}
		switch raw.Type {
		case roleAssistant:
			if text := turn.PromptText(raw.Message.Content); text != "" {
				lastAssistantText = text
			}
		case roleUser:
			if turn.PromptText(raw.Message.Content) == "" {
				return nil // a tool_result carrier can never be an answer turn
			}
			matchProbeLabels(raw.Timestamp, lastAssistantText, labels, matched, adjust, exclude)
		}
		return nil
	})
	if readErr != nil {
		return nil, nil, readErr
	}
	return adjust, exclude, nil
}

// matchProbeLabels checks every not-yet-matched label against lastAssistant —
// the assistant text immediately preceding this user turn — and records an
// adjustment/exclusion entry at ts for each label whose Question landed
// there. Split out of buildProbeAdjustments' closure so the per-turn
// matching logic reads as its own unit (gocognit).
func matchProbeLabels(ts, lastAssistant string, labels []label.Label, matched []bool, adjust map[string]string, exclude map[string]bool) {
	for i := range labels {
		if matched[i] || labels[i].Question == "" {
			continue
		}
		if !strings.Contains(lastAssistant, labels[i].Question) {
			continue
		}
		matched[i] = true
		if labels[i].Valence == label.ValenceIgnored {
			continue
		}
		adjust[ts] = labels[i].Text
		exclude[ts] = true
	}
}
