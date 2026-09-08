package mine

import (
	"regexp"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// Reason breakdown (ferret-54q) — the missing detail behind MisfireRow.Fails:
// a hand pass over 674 Edit failures split them into 301 PreToolUse guard
// denials, 57 native "not read yet", 20 "modified since read" and 5
// replace_all multi-match, four very different problems the plain fail count
// conflates. This is a grouping over event.Event.Err, captured once at
// ingest (internal/event/build.go's resolve()) — not a new detector, and not
// a judge: the same text was always in the transcripts, just discarded.

// ReasonMax bounds a non-hook reason's printed length: the first ~70 chars
// of the whitespace-collapsed error text, per the bead's Rules. Shorter than
// event.DetailMax (160, the ingest-time cap) on purpose — a reason is a
// grouping key for a report row, not the full captured detail.
const ReasonMax = 70

// ReasonRow is one error-text bucket under a ranked key, Count-descending.
type ReasonRow struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// KeyReasons is the reason breakdown for one misfire key (MisfireRow.Key).
type KeyReasons struct {
	Key     string      `json:"key"`
	Reasons []ReasonRow `json:"reasons"`
}

// ReasonsReport bundles the per-key reason breakdown with the corpus's own
// coverage: how many of the failed events considered carry no captured
// error text at all. Stated so a corpus ingested before event.Err existed
// reads as "not captured" rather than silently as "no failures" — the
// failure mode the bead's Rules single out.
type ReasonsReport struct {
	Keys       []KeyReasons `json:"keys"`
	Failed     int          `json:"failed"`     // fail|cfail events in range (post keyFilter)
	Uncaptured int          `json:"uncaptured"` // of Failed, how many have Err == ""
}

// MineReasons walks events once and groups each fail|cfail event's captured
// Err text into a reason bucket under its misfire key (the same Event.Action
// keying MineMisfires uses), scoped to keyFilter when non-empty. It never
// re-derives anything from raw transcript text — event.Err is the only
// input, exactly as captured at ingest.
func MineReasons(events []event.Event, keyFilter string) ReasonsReport {
	counts := map[string]map[string]int{} // key -> reason -> count
	failed, uncaptured := 0, 0

	for i := range events {
		key, ok := reasonCandidate(&events[i], keyFilter)
		if !ok {
			continue
		}
		failed++
		if err := events[i].Err; err == "" {
			uncaptured++
		} else {
			addReason(counts, key, reasonFor(err))
		}
	}

	return ReasonsReport{Keys: sortedKeyReasons(counts), Failed: failed, Uncaptured: uncaptured}
}

// reasonCandidate reports whether ev is a fail|cfail tool/shell event that
// MineReasons should count, and its misfire key — the same gating
// MineMisfires applies (Kind, then Status), plus the optional --key scope.
func reasonCandidate(ev *event.Event, keyFilter string) (string, bool) {
	if ev.Kind != event.KindTool && ev.Kind != event.KindShell {
		return "", false
	}
	if ev.Status != event.StatusFail && ev.Status != event.StatusCFail {
		return "", false
	}
	key := misfireKey(ev)
	if keyFilter != "" && key != keyFilter {
		return "", false
	}
	return key, true
}

// addReason folds one occurrence of reason into key's bucket, creating it on
// first use.
func addReason(counts map[string]map[string]int, key, reason string) {
	m := counts[key]
	if m == nil {
		m = map[string]int{}
		counts[key] = m
	}
	m[reason]++
}

// sortedKeyReasons projects the accumulated counts into key-ascending
// KeyReasons rows, each internally ranked by rankReasons.
func sortedKeyReasons(counts map[string]map[string]int) []KeyReasons {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]KeyReasons, 0, len(keys))
	for _, k := range keys {
		out = append(out, KeyReasons{Key: k, Reasons: rankReasons(counts[k])})
	}
	return out
}

// rankReasons sorts one key's reason buckets Count-descending, reason-stable
// on ties, mirroring rankMisfires' deterministic tie-break posture.
func rankReasons(m map[string]int) []ReasonRow {
	rows := make([]ReasonRow, 0, len(m))
	for r, c := range m {
		rows = append(rows, ReasonRow{Reason: r, Count: c})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		return rows[i].Reason < rows[j].Reason
	})
	return rows
}

// hookErrRe matches a harness PreToolUse/PostToolUse hook denial: CC wraps a
// failing hook's own stderr as "... hook error: [bash <path> <args...>]<rest>".
// Group 1 is the invoked script path; group 2 is whatever follows the closing
// bracket on the same line (the hook's own stderr, when a dispatcher ran a
// named gate under it) — see hookScript.
var hookErrRe = regexp.MustCompile(`hook error:\s*\[bash\s+(\S+)[^\]]*\]([^\n]*)`)

// identRe recognizes a bare identifier token: letters, digits, - and _,
// starting with a letter — the shape a gate/hook script's self-reported name
// takes (gate-bd-sync, read-before-edit).
var identRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// hookScript reports the hook script name behind a captured hook-denial error
// text, per the bead's Rule: keyed by script name, not message text. Two
// shapes occur in the wild:
//
//   - a gate invoked directly: "...hook error: [bash /path/read-before-edit.sh …]"
//     → the bracketed script's basename, extension stripped.
//   - a gate invoked through a shared dispatcher (dispatch-pretool.sh): the
//     dispatcher immediately reports the specific gate's own name right after
//     the bracket, "...[bash .../dispatch-pretool.sh]: gate-bd-sync: refusing…"
//     → that self-reported name, which is the more specific (and more useful)
//     of the two — still "the hook script name" in spirit, just the one that
//     actually ran the check rather than the generic router in front of it.
func hookScript(errText string) (string, bool) {
	m := hookErrRe.FindStringSubmatch(errText)
	if m == nil {
		return "", false
	}
	if name := hookNameAfterBracket(m[2]); name != "" {
		return name, true
	}
	return scriptBase(m[1]), true
}

// hookNameAfterBracket extracts the leading "<name>:" identifier from the
// text immediately following a hook error's closing bracket, or "" when the
// text doesn't take that shape (the direct-invocation case falls through to
// scriptBase instead).
func hookNameAfterBracket(rest string) string {
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	end := strings.IndexAny(rest, ": \t")
	if end == -1 {
		end = len(rest)
	}
	name := rest[:end]
	if identRe.MatchString(name) {
		return name
	}
	return ""
}

// scriptBase strips a path down to its final component, extension removed.
func scriptBase(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".sh")
}

// reasonFor derives one event's reason bucket from its captured Err text: a
// hook denial keys by script name (hook:<name>); everything else keys by its
// own whitespace-collapsed, ~70-char text prefix — so two real failures with
// different messages land in different buckets, and only truly identical
// (or identically-prefixed) error text collapses together.
func reasonFor(errText string) string {
	if name, ok := hookScript(errText); ok {
		return "hook:" + name
	}
	return reasonText(errText)
}

// reasonText whitespace-collapses s and truncates to ReasonMax runes.
func reasonText(s string) string {
	collapsed := strings.Join(strings.Fields(s), " ")
	r := []rune(collapsed)
	if len(r) <= ReasonMax {
		return collapsed
	}
	return string(r[:ReasonMax])
}
