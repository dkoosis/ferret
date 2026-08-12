package mine

import (
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// Substitution detection (ferret-cax item 3) — a fourth internal/mine
// detector, mirroring polling.go's shape: a deterministic table over
// shellnorm output, no judge call anywhere. Verb = Event.Action, already the
// shellnorm-normalized token (rg, grep, ls, find, cat, head, tail, sed);
// per-call flag/arg checks parse Event.Detail via shellnorm.Argv so
// sh-parsing knowledge stays in shellnorm.
//
// Two escape hatches are structural facts that die at ingest and must be read
// off Event rather than re-derived from Detail: a compound chain is already
// Event.Compound, and a pipeline is Event.Pipe (shellnorm's pipeline-collapse
// erases the `|` from Detail — same one-way loss Event.Swallow exists to
// capture). Every other escape hatch — an unsupported flag, a redirect, an
// expansion, wrong arity, or a truncated/unparseable Detail — is read from
// the parsed argv.
//
// The table is original and mechanical, not a published algorithm: see
// ferret's cite-algos-in-code convention.

// SubstRow is one shell verb's corpus-wide substitution ranking: how many of
// its calls a native tool (Grep/Glob/Read) could have served instead.
type SubstRow struct {
	Key      string  `json:"key"`      // Event.Action, "sh:"-prefixed — same key space as MisfireRow/PollingRow
	Tool     string  `json:"tool"`     // Grep | Glob | Read — the native tool this verb maps to
	Calls    int     `json:"calls"`    // substitutable calls of this verb
	Sessions int     `json:"sessions"` // distinct sessions with ≥1 substitutable call of this verb
	Score    float64 `json:"score"`    // Calls × Sessions — mirrors PollingRow/MisfireRow
	Exemplar string  `json:"exemplar,omitempty"`
}

// SubstReport is the ranked substitution table plus the corpus session
// denominator and a tally of why calls were excluded — the escape hatches
// that keep this a floor, not a false-positive machine.
type SubstReport struct {
	Rows     []SubstRow     `json:"rows"`
	Sessions int            `json:"sessions"` // distinct sessions containing ≥1 shell event
	Excluded map[string]int `json:"excluded"` // reason → count, e.g. "pipe", "unsupported_flag"
}

// Exclusion reasons — the Excluded tally's key vocabulary. Named as constants
// (not inlined per call site) both to stop goconst from flagging the repeats
// across the eight substRule* functions and so the reader has one place to
// see the full reason set.
const (
	reasonPipe            = "pipe"
	reasonCompound        = "compound"
	reasonTruncated       = "truncated"
	reasonRedirect        = "redirect"
	reasonExpansion       = "expansion"
	reasonUnparseable     = "unparseable"
	reasonArity           = "arity"
	reasonUnsupportedFlag = "unsupported_flag"
)

// substDetailMax mirrors event's ingest-time truncation length
// (internal/event/build.go's detailMax, unexported). A Detail at or past this
// length may be missing a flag past the cut — the conservative floor from the
// bead's Risks: exclude rather than scan a possibly-incomplete command.
const substDetailMax = 160

// substTools is the verb→native-tool table: the only Action values this
// detector considers candidates. Every other shell verb is skipped, not
// excluded — it was never a substitution question in the first place.
var substTools = map[string]string{
	"rg": "Grep", "grep": "Grep",
	"ls": "Glob", "find": "Glob",
	"cat": "Read", "head": "Read", "tail": "Read", "sed": "Read",
}

// substRules holds each candidate verb's hit/escape decision, keyed the same
// as substTools. Each rule receives argv with the verb itself stripped.
var substRules = map[string]func([]string) (bool, string){
	"rg": substRuleRg, "grep": substRuleGrep,
	"ls": substRuleLs, "find": substRuleFind,
	"cat": substRuleCat, "head": substRuleHead, "tail": substRuleTail, "sed": substRuleSed,
}

// substAgg accumulates one verb's corpus-wide totals as the walk streams by.
type substAgg struct {
	calls    int
	sessions map[string]struct{}
	exemplar string
}

// MineSubstitutions walks events and ranks shell calls a native tool could
// have replaced, verb by verb, corpus-wide. Only KindShell events with raw
// command text (Detail) participate — mirrors MinePolling's gate.
func MineSubstitutions(events []event.Event) SubstReport {
	aggs := map[string]*substAgg{}
	excluded := map[string]int{}
	sessions := map[string]struct{}{}

	for i := range events {
		ev := &events[i]
		if ev.Kind != event.KindShell || ev.Detail == "" {
			continue
		}
		sessions[ev.Session] = struct{}{}
		if _, candidate := substTools[ev.Action]; !candidate {
			continue
		}
		ok, reason := substHit(ev)
		if !ok {
			excluded[reason]++
			continue
		}
		observeSubstHit(aggs, ev)
	}

	return SubstReport{
		Rows:     rankSubst(aggs),
		Sessions: len(sessions),
		Excluded: excluded,
	}
}

// observeSubstHit folds one confirmed-substitutable event into its verb's
// aggregate, first-seen command text as the exemplar.
func observeSubstHit(aggs map[string]*substAgg, ev *event.Event) {
	key := "sh:" + ev.Action
	a := aggs[key]
	if a == nil {
		a = &substAgg{sessions: map[string]struct{}{}}
		aggs[key] = a
	}
	a.calls++
	a.sessions[ev.Session] = struct{}{}
	if a.exemplar == "" {
		a.exemplar = ev.Detail
	}
}

// substHit decides whether one event's command is substitutable, checking the
// structural (ingest-captured) escape hatches first — Pipe and Compound carry
// information Detail alone cannot recover — then the parsed-argv rule for its
// verb.
func substHit(ev *event.Event) (bool, string) {
	if ev.Pipe {
		return false, reasonPipe
	}
	if ev.Compound {
		return false, reasonCompound
	}
	if len(ev.Detail) >= substDetailMax {
		return false, reasonTruncated
	}
	argv, plain := shellnorm.Argv(ev.Detail)
	if !plain {
		return false, argvFailReason(ev.Detail)
	}
	if len(argv) == 0 {
		return false, reasonUnparseable
	}
	rule := substRules[ev.Action] // caller already gated ev.Action via substTools
	return rule(argv[1:])
}

// argvFailReason sub-classifies a shellnorm.Argv parse failure for the
// Excluded tally: a redirect or expansion changes what a plain argv scan
// means, so each gets its own bucket rather than collapsing into a single
// "unparseable" catch-all. shellnorm.Argv remains the sole authority on
// *whether* to exclude — this only labels why, for the reader.
func argvFailReason(detail string) string {
	if strings.ContainsAny(detail, "<>") {
		return reasonRedirect
	}
	if strings.Contains(detail, "$") {
		return reasonExpansion
	}
	return reasonUnparseable
}

// flagSpec is one verb's flag allowlist: allow gates which flag tokens are
// accepted at all (default-deny — a flag absent from allow always excludes,
// covering "ls: any flag escapes" with an empty map), value marks which
// allowed flags consume the following token as their argument rather than
// counting it as a positional.
type flagSpec struct {
	allow map[string]bool
	value map[string]bool
}

// splitArgs partitions a verb's post-name argv into positionals and
// flag-values per spec. ok is false the instant an unrecognized flag or a
// value flag missing its argument appears — the shared default-deny gate
// every substRule* function builds on.
func splitArgs(args []string, spec flagSpec) (positionals []string, values map[string]string, ok bool) {
	values = map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			positionals = append(positionals, a)
			continue
		}
		if !spec.allow[a] {
			return nil, nil, false
		}
		if spec.value[a] {
			if i+1 >= len(args) {
				return nil, nil, false
			}
			values[a] = args[i+1]
			i++
			continue
		}
	}
	return positionals, values, true
}

// grepLikeAllow is rg/grep's shared safe-flag allowlist. -c (count-only
// output) is deliberately absent — the bead names `grep -c` as a case that
// must escape, since a count is not what Grep's content/files_with_matches
// modes return.
var grepLikeAllow = map[string]bool{"-i": true, "-n": true, "-w": true, "-l": true, "-F": true}

func substRuleRg(args []string) (bool, string) {
	pos, _, ok := splitArgs(args, flagSpec{allow: grepLikeAllow})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if len(pos) < 1 {
		return false, reasonArity
	}
	return true, ""
}

func substRuleGrep(args []string) (bool, string) {
	return substRuleRg(args)
}

// substRuleLs: no allowed flags at all — the bead's named default-deny case.
// `ls path` (0 or 1 positional) maps to Glob; any flag escapes.
func substRuleLs(args []string) (bool, string) {
	pos, _, ok := splitArgs(args, flagSpec{})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if len(pos) > 1 {
		return false, reasonArity
	}
	return true, ""
}

var (
	findAllow = map[string]bool{"-name": true}
	findValue = map[string]bool{"-name": true}
)

// substRuleFind requires -name (the Glob-equivalent selector) with a value,
// plus at least one search root. `-exec` and everything else outside the
// allowlist escapes as unsupported_flag.
func substRuleFind(args []string) (bool, string) {
	pos, values, ok := splitArgs(args, flagSpec{allow: findAllow, value: findValue})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if len(pos) < 1 || values["-name"] == "" {
		return false, reasonArity
	}
	return true, ""
}

// substRuleCat: no flags, exactly one file — multiple files concatenate, which
// Read (one file at a time) cannot reproduce.
func substRuleCat(args []string) (bool, string) {
	pos, _, ok := splitArgs(args, flagSpec{})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if len(pos) != 1 {
		return false, reasonArity
	}
	return true, ""
}

var nValueAllow = map[string]bool{"-n": true}

// substRuleHead/-Tail: `-n COUNT FILE` is the only recognized shape — Read
// with an offset/limit covers the same "give me N lines of one file" case.
func substRuleHead(args []string) (bool, string) {
	pos, _, ok := splitArgs(args, flagSpec{allow: nValueAllow, value: nValueAllow})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if len(pos) != 1 {
		return false, reasonArity
	}
	return true, ""
}

func substRuleTail(args []string) (bool, string) {
	return substRuleHead(args)
}

var sedAllow = map[string]bool{"-n": true} // boolean: suppresses auto-print

// sedPrintRange matches a quiet-mode print-only script: `Np` or `N,Mp` — the
// only sed usage this table treats as Read-equivalent. Anything else (edits,
// substitutions, deletes) is not a read.
var sedPrintRange = regexp.MustCompile(`^[0-9]+(,[0-9]+)?p$`)

// substRuleSed requires -n plus a print-range script and one file — the
// "sed-print" row in the bead's verb table. Missing -n means sed auto-prints
// every line (not a substitution for a targeted Read); any other script shape
// may mutate, which no tool_result-only observation can rule out.
func substRuleSed(args []string) (bool, string) {
	pos, _, ok := splitArgs(args, flagSpec{allow: sedAllow})
	if !ok {
		return false, reasonUnsupportedFlag
	}
	if !hasFlag(args, "-n") {
		return false, reasonUnsupportedFlag
	}
	if len(pos) != 2 || !sedPrintRange.MatchString(pos[0]) {
		return false, reasonArity
	}
	return true, ""
}

func hasFlag(args []string, flag string) bool {
	return slices.Contains(args, flag)
}

// rankSubst projects the per-verb aggregates into the sorted SubstRow table:
// Score = Calls × Sessions descending, mirroring rankPolling/rankMisfires'
// shape, with deterministic tie-breaks so repeated runs are byte-stable.
func rankSubst(aggs map[string]*substAgg) []SubstRow {
	rows := make([]SubstRow, 0, len(aggs))
	for key, a := range aggs {
		verb := strings.TrimPrefix(key, "sh:")
		row := SubstRow{
			Key:      key,
			Tool:     substTools[verb],
			Calls:    a.calls,
			Sessions: len(a.sessions),
			Exemplar: a.exemplar,
		}
		row.Score = float64(row.Calls) * float64(row.Sessions)
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		x, y := rows[i], rows[j]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Calls != y.Calls {
			return x.Calls > y.Calls
		}
		if x.Sessions != y.Sessions {
			return x.Sessions > y.Sessions
		}
		return x.Key < y.Key
	})
	return rows
}
