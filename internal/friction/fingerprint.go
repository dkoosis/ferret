// Package friction is ferret's friction-recurrence detector: given a set of
// known friction signatures and a fresh event stream, it flags the moment a new
// friction event repeats a signature ferret has already seen.
//
// Why it exists: traps recur DESPITE being recorded. A friction nug read once at
// session open is the wrong plane for behavior-gating knowledge — recall does not
// stop the second occurrence. Recurrence should trigger ENFORCEMENT (a hookify
// rule), and the missing piece is the matcher. This package is that matcher; the
// downstream consumers (nug reading, the /wrap trap-graduation prompt) are other
// beads. Our job ends at emitting a match record precise enough for one of those
// to act on.
//
// The core design choice is the fingerprint: a normalized signature of a friction
// event with its volatile parts (paths, hashes, timestamps, numbers, quoted
// literals) masked out, so the SAME underlying friction fingerprints identically
// across sessions even though its surface text differs every time.
package friction

import (
	"regexp"
	"strings"
)

// Fingerprint reduces a raw friction string (a failed command line, an error
// message) to a stable signature by masking the tokens that vary run-to-run.
// Two friction events with the same underlying cause map to the same
// fingerprint even when their paths, SHAs, line numbers, and timestamps differ.
//
// The method is log-template extraction by variable masking — the normalization
// step every log-clustering parser performs before grouping messages into
// templates (cf. He et al., "Drain: An Online Log Parsing Approach with a Fixed
// Depth Tree", ICWS 2017; the placeholder-substitution idea also underlies
// logreduce and Elastic's log categorization). We mask rather than tree-cluster:
// the corpus is small and the masks below cover the volatile token classes seen
// in Claude Code tool failures, so a deterministic pass beats an online tree.
//
// Masking order is load-bearing — earlier masks consume characters later masks
// would otherwise misread (a quoted literal may contain a path; a timestamp
// contains numbers; a path segment may itself be a hex SHA). The fixed order is:
// quoted literals, timestamps, UUIDs, paths, hex hashes, then bare numbers.
// Paths run before hashes so a path with a hex-looking segment masks whole to
// <path> rather than fragmenting. Finally whitespace is collapsed and the result
// lower-cased so casing and spacing never fork a signature.
func Fingerprint(raw string) string {
	s := raw
	s = reQuoted.ReplaceAllString(s, "<str>")
	s = reTimestamp.ReplaceAllString(s, "<ts>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = rePath.ReplaceAllString(s, "${1}<path>")
	s = maskRefs(s)
	s = maskHex(s)
	s = reNumber.ReplaceAllString(s, "<n>")
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// maskRefs masks a VOLATILE single-slash reference — a git branch or temp name
// like "fix/ferret-5jb" or "tmp/build.4" — to <path>, so the same friction on two
// branches fingerprints identically (a recurrence detector favors recall). It
// runs AFTER rePath, so anchored and ≥2-slash paths are already <path> and every
// remaining slash is a lone one.
//
// The precision knob: a single-slash a/b is masked UNLESS both segments are pure
// ASCII letters. "openai/ferret" and "openai/gym" (pure-alpha repo slugs) stay
// literal and so fingerprint DIFFERENTLY; "fix/ferret-5jb" (a digit/dash-bearing
// ref) masks. This is the one rule that satisfies both required cases at once.
// Its limit: a pure-alpha branch like "feature/main" is indistinguishable from a
// repo slug by text alone and stays literal — an accepted precision tradeoff,
// since the volatility markers (digits, -, _, .) catch the branch names that
// actually vary run-to-run.
func maskRefs(s string) string {
	return reRef.ReplaceAllStringFunc(s, func(tok string) string {
		left, right, ok := strings.Cut(tok, "/")
		if ok && isPlainAlpha(left) && isPlainAlpha(right) {
			return tok // repo slug / English word-pair: keep, so they stay distinct
		}
		return "<path>"
	})
}

// maskHex masks a long hex run (git SHA, content hash, object id) to <hash>, but
// ONLY when the run carries at least one hex LETTER (a-f). A run of ≥7 pure
// decimal digits is not a hash — it is a big number (byte offset, port, line no.)
// and is left for the later reNumber pass, so a 6-digit and a 7-digit instance of
// the same friction both normalize to <n> and fingerprint identically. Without
// this guard they would fork across the reHex/reNumber boundary (a missed
// recurrence).
func maskHex(s string) string {
	return reHex.ReplaceAllStringFunc(s, func(tok string) string {
		if hasHexLetter(tok) {
			return "<hash>"
		}
		return tok // pure-decimal run: leave it for reNumber
	})
}

// hasHexLetter reports whether s contains an ASCII hex letter (a-f/A-F).
func hasHexLetter(s string) bool {
	for i := range len(s) {
		c := s[i]
		if (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			return true
		}
	}
	return false
}

// isPlainAlpha reports whether s is one or more ASCII letters only — no digit,
// dot, dash, or underscore. Such a segment carries no run-to-run volatility.
func isPlainAlpha(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

var (
	// reQuoted masks single- or double-quoted literals whole — their interior
	// (filenames, messages, values) is the most volatile part of a command.
	reQuoted = regexp.MustCompile(`"[^"]*"|'[^']*'`)

	// reTimestamp masks ISO-8601-ish timestamps and bare clock times so a log
	// line that differs only by when it was emitted fingerprints identically.
	reTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?|\b\d{2}:\d{2}:\d{2}\b`)

	// reUUID masks canonical UUIDs (session/agent/request ids).
	reUUID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

	// reHex matches long hex-or-digit runs: git SHAs, content hashes, object ids.
	// The ≥7 floor keeps short hex-looking words (e.g. "added", "beef") from
	// matching, while catching abbreviated 7-char git SHAs and up. maskHex then
	// gates each match on a hex letter, so pure-decimal runs fall through to
	// reNumber rather than masking as <hash>.
	reHex = regexp.MustCompile(`\b[0-9a-fA-F]{7,}\b`)

	// rePath masks filesystem paths without over- or under-masking. A path masks
	// only when it is unambiguously a path — either ANCHORED (leading /, ./, ../,
	// ~/, or a Windows drive C:/ or C:\) or DEEP (an unanchored token with ≥2
	// interior slashes, e.g. internal/score/gates.go). A bare single-slash
	// identifier pair like "openai/ferret" is NOT a path and is kept, so
	// "openai/ferret" and "openai/gym" fingerprint DIFFERENTLY instead of both
	// collapsing to <path> (a false recurrence).
	//
	// Group 1 captures the boundary char (start, whitespace, or a small punct set)
	// preceding the path and is restored on replace ("${1}<path>"): RE2 has no
	// lookbehind, so consuming+restoring the boundary is how we require the path's
	// leading slash to START a token rather than sit mid-word — without it the
	// absolute-path branch would match the interior slash of "openai/ferret".
	// Windows drive paths (C:/Users/x) match whole, drive letter included, so they
	// mask fully rather than leaving a "c:" residue.
	rePath = regexp.MustCompile(`(^|[\s"'=,(])((?:[A-Za-z]:[\\/]|~/|\.{1,2}/|/)[\w.\-]+(?:[\\/][\w.\-]+)*|[\w.\-]+(?:/[\w.\-]+){2,})`)

	// reRef matches a lone single-slash word/word token (branch, temp name, or
	// repo slug). maskRefs decides mask-vs-keep per token; the word boundaries
	// keep it from firing inside a larger token. Runs after rePath, so multi-slash
	// paths are already masked and only single-slash refs remain.
	reRef = regexp.MustCompile(`\b[\w.\-]+/[\w.\-]+\b`)

	// reNumber masks any remaining standalone number (line numbers, ports,
	// byte counts, exit codes, durations). Run last so it does not eat digits
	// the timestamp/hash/path masks should have claimed.
	reNumber = regexp.MustCompile(`\b\d+\b`)
)
