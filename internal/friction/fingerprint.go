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
	s = reHex.ReplaceAllString(s, "<hash>")
	s = reNumber.ReplaceAllString(s, "<n>")
	s = strings.ToLower(s)
	s = strings.Join(strings.Fields(s), " ")
	return s
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

	// reHex masks long hex runs: git SHAs, content hashes, object ids. The ≥7
	// floor keeps short hex-looking words (e.g. "added", "beef") from masking,
	// while catching abbreviated 7-char git SHAs and up.
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

	// reNumber masks any remaining standalone number (line numbers, ports,
	// byte counts, exit codes, durations). Run last so it does not eat digits
	// the timestamp/hash/path masks should have claimed.
	reNumber = regexp.MustCompile(`\b\d+\b`)
)
