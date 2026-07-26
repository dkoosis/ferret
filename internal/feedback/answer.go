package feedback

// Answer recognition — the pure decision this package owns for the answer-side
// of the tap (ferret-j33), mirroring select.go's framing: dk's next turn is NOT
// presumed to be the answer to a banked ask. It is recognized ONLY by a
// deterministic leading token, never inferred from position alone.

import (
	"regexp"
	"strings"

	"github.com/dkoosis/ferret/internal/dialogue"
	"github.com/dkoosis/ferret/internal/label"
)

// answerTokenRe matches a leading valence token — case-insensitive, longest
// form checked first (yes/no/skip before their single-letter short forms) —
// immediately followed by a HARD boundary: whitespace, a dash variant, or a
// sentence-punctuation mark, or end of string. Never a bare word-character
// boundary (`\b`): that would false-match "y" inside "y'know" or "s" inside
// "sure". The separator run (one or more boundary chars, e.g. " - ") is
// consumed together so "y - now fix the tests" and "y: now fix the tests"
// both strip cleanly; group 2 captures everything after it to the end of the
// turn (dot-matches-newline, `(?s)`, so a multi-line remainder survives
// intact) — empty when the token sits at the very end of the turn (a bare
// "y").
var answerTokenRe = regexp.MustCompile(`(?is)^(yes|no|skip|y|n|s)(?:[\s\-–—:,.!?]+(.*)|$)`)

// tokenValence maps a matched (lowercased) token to its label.Valence
// constant. Only the six grammar tokens are ever passed in; any other input
// is a caller bug, not a recognized-answer path.
func tokenValence(token string) (string, bool) {
	switch token {
	case "yes", "y":
		return label.ValenceYes, true
	case "no", "n":
		return label.ValenceNo, true
	case "skip", "s":
		return label.ValenceSkip, true
	default:
		return "", false
	}
}

// RecognizeAnswer classifies prompt against the deterministic leading-token
// grammar the answer-side tap recognizes for a turn following an armed ask:
// yes/no/skip, or their short y/n/s forms, case-insensitive, immediately
// followed by a hard boundary (see answerTokenRe). On a match it returns the
// label.Valence the token names and remainder — whatever follows the token
// and its separator, trimmed — so "y - now fix the tests" strips to
// (ValenceYes, "now fix the tests") and a bare "y" strips to (ValenceYes,
// ""). ok is false for any turn that doesn't match the leading grammar at
// all; the caller records label.ValenceIgnored in that case, never a
// failure — an unrecognized turn is itself signal, not an error.
//
// A "no"/"n" match is additionally guarded against the benign-reassurance
// continuation ("no worries", "no problem") dialogue.IsBenignNo already
// classifies for the dialogue tagger: a benign no is not a "no" vote, so it
// falls through to ok=false (recorded as ignored) rather than misreading
// ordinary reassurance as a negative verdict. Reused via import — the same
// guard-word list dialogue.TagMove's matchRepair applies internally — so the
// two packages' notion of "benign no" can never drift apart.
func RecognizeAnswer(prompt string) (valence, remainder string, ok bool) {
	trimmed := strings.TrimSpace(prompt)
	m := answerTokenRe.FindStringSubmatch(trimmed)
	if m == nil {
		return "", "", false
	}
	v, ok := tokenValence(strings.ToLower(m[1]))
	if !ok {
		return "", "", false
	}
	if v == label.ValenceNo && dialogue.IsBenignNo(trimmed) {
		return "", "", false
	}
	return v, strings.TrimSpace(m[2]), true
}
