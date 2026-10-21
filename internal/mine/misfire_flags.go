package mine

import (
	"cmp"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// Wrong-flag chains (ferret-f3f) — the flag-level view the swallowed-error
// floor (ferret-cax) cannot give: cax counts the `2>/dev/null ||` SHAPE per
// command but names no flag and pairs no retry. This pass names the flag a
// call got wrong, from one of three tells:
//
//   - measured, error names it: a failed call whose captured error text
//     (Event.Err) names one of its own flags as unknown/undefined.
//   - measured, retry drops it: a failed call whose error text complains
//     about its invocation (a usage block, an unknown option, …), followed
//     within flagWindow shell calls by a same-command call that no longer
//     carries the flag and did not fail on its invocation too.
//   - inferred: a call whose exit code was hidden (a non-last pipe stage or
//     `;` statement, or the swallowed left arm of `2>/dev/null ||`) followed
//     within the same window by a same-command call without the flag — or,
//     for a swallow, by its own `||` fallback when that is the same tool.
//     Inferred chains need corroboration; see flagRow.
//
// A usage block alone is never proof: loto lock prints one when a peer holds
// the lock, and the next call carries the same flags. No flag change, no row.
// A help flag is never blamed, and an exit 1 with no complaint (grep's
// no-match) is not a misfire.
//
// A flag's "form" is its name, plus " <path>" when its value is path-shaped:
// `loto lock -t <path>` and its fix `loto lock <path> -t "why"` carry the
// same flag NAME, and only the value shape tells the wrong call apart.

// flagWindow is how many later shell calls a retry may come in — the bead's
// "within 3 shell calls". Later segments of the failing call itself are
// always in the window.
const flagWindow = 3

// FlagRow is one (command, flag form) wrong-flag ranking row.
type FlagRow struct {
	Key     string `json:"key"`     // "sh:"+Event.Action — the MisfireRow key space
	Command string `json:"command"` // Event.Action with its first "_" as a space: "bd list"
	Flag    string `json:"flag"`    // flag name, or "name <path>" for a path-shaped value
	// Chains = Measured + Inferred: wrong-flag calls proven by error text or
	// by a retry without the flag.
	Chains   int `json:"chains"`
	Measured int `json:"measured"` // the call's own exit code said fail
	Inferred int `json:"inferred"` // the exit code was hidden; the retry is the only tell
	Sessions int `json:"sessions"` // distinct sessions with ≥1 chain
	// Calls and CallSessions count every call of Key carrying Flag in range,
	// chain or not — the size of the habit the chains sample.
	Calls        int     `json:"calls"`
	CallSessions int     `json:"callSessions"`
	LastSeen     string  `json:"lastSeen,omitempty"` // local YYYY-MM-DD of the newest chain
	Score        float64 `json:"score"`              // Chains × Sessions
	Exemplar     string  `json:"exemplar,omitempty"` // first chained call's command text
}

// flagKey identifies one (command, flag form) aggregate.
type flagKey struct{ key, flag string }

// chainClass is how a chain was proven; the row counts the classes apart.
type chainClass int

const (
	classMeasured chainClass = iota // own exit code said fail
	classRetry                      // hidden exit, same-command retry dropped the form
	classGuess                      // swallowed `||` left arm, fallback dropped the form
	numClasses
)

// chainTally is one class's share of an aggregate.
type chainTally struct {
	n        int
	sessions map[string]struct{}
	lastSeen string
	exemplar string
}

// flagAgg accumulates one (command, flag form).
type flagAgg struct {
	calls        int
	callSessions map[string]struct{}
	tally        [numClasses]chainTally
	// cleanOK counts standalone (non-compound) calls carrying the form that
	// exited 0 — proof the form is valid at least sometimes. A compound
	// segment's ok is not proof: ferret marks the left arm of `a || b` ok
	// whenever b succeeds.
	cleanOK int
}

// streamKey is one agent's ordered run of shell calls. agent_id is the only
// parent↔subagent discriminator: they share session_id.
type streamKey struct{ session, agent string }

// flagScan holds one pass's state: each shell event's forms, computed once,
// and the aggregates.
type flagScan struct {
	events []event.Event
	forms  map[int][]string
	aggs   map[flagKey]*flagAgg
}

// MineFlagMisfires ranks wrong-flag chains by command and flag form.
func MineFlagMisfires(events []event.Event) []FlagRow {
	sc := &flagScan{events: events, forms: map[int][]string{}, aggs: map[flagKey]*flagAgg{}}
	for _, calls := range shellCallStreams(events) {
		sc.scanStream(calls)
	}
	return sc.rank()
}

// shellCallStreams groups shell events by (session, agent), in file order,
// into calls: consecutive segments sharing a Seq are one Bash call.
func shellCallStreams(events []event.Event) [][][]int {
	idx := map[streamKey]int{}
	var streams [][][]int
	lastSeq := map[streamKey]int{}
	for i := range events {
		ev := &events[i]
		if ev.Kind != event.KindShell {
			continue
		}
		k := streamKey{ev.Session, ev.Agent}
		si, ok := idx[k]
		if !ok {
			si = len(streams)
			idx[k] = si
			streams = append(streams, nil)
		}
		calls := streams[si]
		if len(calls) == 0 || lastSeq[k] != ev.Seq {
			calls = append(calls, nil)
		}
		calls[len(calls)-1] = append(calls[len(calls)-1], i)
		streams[si] = calls
		lastSeq[k] = ev.Seq
	}
	return streams
}

// scanStream walks one stream's calls, counting every flagged call and
// recording the chains among them.
func (sc *flagScan) scanStream(calls [][]int) {
	for ci, call := range calls {
		for si, i := range call {
			forms := sc.formsOf(i)
			if len(forms) == 0 {
				continue
			}
			sc.countCall(i, forms)
			later := laterInWindow(calls, ci, si)
			chained, class := sc.chainForms(i, forms, later)
			for _, form := range chained {
				sc.recordChain(i, form, class)
			}
		}
	}
}

// laterInWindow lists the event indices a retry may come from: the rest of
// the current call, then the next flagWindow calls.
func laterInWindow(calls [][]int, ci, si int) []int {
	out := slices.Clone(calls[ci][si+1:])
	for _, next := range calls[ci+1 : min(len(calls), ci+1+flagWindow)] {
		out = append(out, next...)
	}
	return out
}

// countCall adds one call to the Calls column of each of its forms, and
// marks the forms a standalone call ran cleanly with.
func (sc *flagScan) countCall(i int, forms []string) {
	ev := &sc.events[i]
	clean := ev.Status == event.StatusOK && !ev.Compound
	for _, form := range forms {
		a := sc.agg(misfireKey(ev), form)
		a.calls++
		a.callSessions[ev.Session] = struct{}{}
		if clean {
			a.cleanOK++
		}
	}
}

// chainForms returns the forms event i got wrong and how that was proven,
// or nil.
func (sc *flagScan) chainForms(i int, forms []string, later []int) ([]string, chainClass) {
	ev := &sc.events[i]
	switch {
	case sc.failed(i):
		return sc.measuredForms(i, forms, later), classMeasured
	case !hiddenExit(ev):
		return nil, classMeasured
	}
	if next := sc.swallowFallback(i, later); next >= 0 {
		return sc.guessForms(i, forms, next), classGuess
	}
	if next := sc.retry(i, later); next >= 0 {
		return sc.dropped(forms, next), classRetry
	}
	return nil, classRetry
}

// measuredForms is chainForms for a failed call: the flag its error text
// rejects by name, else — when the text complains about the invocation —
// the forms a same-command retry that did not fail dropped. A compound
// call's error text may be a peer segment's (cfail), so there only a dropped
// flag the text names is blamed.
func (sc *flagScan) measuredForms(i int, forms []string, later []int) []string {
	errText := sc.events[i].Err
	if named := namedFlag(errText, forms); named != "" {
		return []string{named}
	}
	if !invocationComplaint.MatchString(errText) {
		return nil // exit 1 with no complaint (grep's no-match) is not a misfire
	}
	next := sc.fixedRetry(i, later)
	if next < 0 {
		return nil
	}
	dropped := sc.dropped(forms, next)
	if m := mentioned(errText, dropped); len(m) > 0 {
		return m // `--slurp is not supported with --jq`: the retry dropped both, the text blames one
	}
	if sc.events[i].Status == event.StatusCFail {
		return nil
	}
	return dropped
}

// mentioned returns the forms whose flag name the error text spells out.
func mentioned(errText string, forms []string) []string {
	words := flagWord.FindAllString(errText, -1)
	var out []string
	for _, f := range forms {
		if name, _, _ := strings.Cut(f, " "); slices.Contains(words, name) {
			out = append(out, f)
		}
	}
	return out
}

// guessForms is the forms a swallowed call's `||` fallback dropped. A
// fallback to another subcommand (`bd list … || bd show X`) drops every
// flag the subcommands do not share, so it names a flag only when exactly
// one was dropped; more is ambiguous and blames none.
func (sc *flagScan) guessForms(i int, forms []string, next int) []string {
	dropped := sc.dropped(forms, next)
	if sc.events[next].Action != sc.events[i].Action && len(dropped) > 1 {
		return nil
	}
	return dropped
}

// hiddenExit reports whether ev's own exit code never reached the result:
// a non-last pipe stage or `;` statement, or a swallowed `||` left arm.
func hiddenExit(ev *event.Event) bool {
	return ev.Swallow || ev.Status == event.StatusUnmeasured
}

// retry returns the first later event with the same command and a different
// flag-form set, or -1. A same-form repeat is not the retry — two parallel
// `bd list --status X --short` calls are both fixed by the one after them.
func (sc *flagScan) retry(i int, later []int) int {
	for _, j := range later {
		if sc.isRetry(i, j) {
			return j
		}
	}
	return -1
}

// fixedRetry is retry for a failed call, past attempts that still fail on
// their invocation: a usage error, a changed attempt that still misfires,
// then the call that works is one chain, closed by the call that works.
func (sc *flagScan) fixedRetry(i int, later []int) int {
	for _, j := range later {
		if sc.isRetry(i, j) && !sc.stillMisinvoked(j) {
			return j
		}
	}
	return -1
}

// isRetry reports whether event j runs i's command with other flag forms.
func (sc *flagScan) isRetry(i, j int) bool {
	return sc.events[j].Action == sc.events[i].Action && !sameForms(sc.formsOf(i), sc.formsOf(j))
}

// sameForms reports whether two form lists hold the same set.
func sameForms(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, f := range a {
		if !slices.Contains(b, f) {
			return false
		}
	}
	return true
}

// swallowFallback returns the `||` fallback of a swallowed call when it is
// the same tool (`bd list --short 2>/dev/null || bd show X`), or -1. The
// fallback is the next segment of the same call.
func (sc *flagScan) swallowFallback(i int, later []int) int {
	ev := &sc.events[i]
	if !ev.Swallow || len(later) == 0 {
		return -1
	}
	j := later[0]
	if sc.events[j].Seq != ev.Seq || toolOf(sc.events[j].Action) != toolOf(ev.Action) {
		return -1
	}
	return j
}

// stillMisinvoked reports whether the retry failed on its invocation too.
// A retry that got past the usage error and then failed for state reasons —
// loto's "blocked" by a peer, "invalid" target — did change the flags that
// mattered, so it still closes the chain.
func (sc *flagScan) stillMisinvoked(j int) bool {
	return sc.failed(j) && invocationComplaint.MatchString(sc.events[j].Err)
}

func (sc *flagScan) failed(j int) bool {
	st := sc.events[j].Status
	return st == event.StatusFail || st == event.StatusCFail
}

// dropped returns the forms of the failing call that event j does not carry.
// A help flag is never one: `x --help` exits 2 with a usage block by design,
// and the call after it naturally drops --help.
func (sc *flagScan) dropped(forms []string, j int) []string {
	have := sc.formsOf(j)
	var out []string
	for _, f := range forms {
		if !slices.Contains(have, f) && !helpFlag(f) && !sc.pathOnlyDropped(f, j) {
			out = append(out, f)
		}
	}
	return out
}

// pathOnlyDropped reports whether j kept the flag of i's `name <path>` form
// bare: `tool --verbose a.yaml` → `tool --verbose`. The flag may be boolean
// and the path positional, so neither is blamed. When j gives the flag a
// value (`loto lock -t a.go` → `loto lock a.go -t "why"`), the binding is
// what changed and the form is blamed, as it is when the flag fell past a
// truncated Detail and its value cannot be read.
func (sc *flagScan) pathOnlyDropped(form string, j int) bool {
	name, ok := strings.CutSuffix(form, " <path>")
	if !ok || !slices.Contains(sc.formsOf(j), name) {
		return false
	}
	words := shellWords(sc.events[j].Detail)
	at := slices.Index(words, name)
	if at < 0 {
		return false
	}
	return at+1 == len(words) || bareFollower.MatchString(words[at+1])
}

// bareFollower is a word that ends a flag's value slot: another flag, a
// redirect, or a shell operator.
var bareFollower = regexp.MustCompile(`^(-|\d*>|[|&;])`)

// helpFlag reports whether a form asks for help rather than doing work.
func helpFlag(form string) bool {
	switch form {
	case "-h", "--help", "-help":
		return true
	}
	return false
}

// recordChain books one chain of class c for (event i's command, form).
func (sc *flagScan) recordChain(i int, form string, c chainClass) {
	ev := &sc.events[i]
	t := &sc.agg(misfireKey(ev), form).tally[c]
	t.n++
	t.sessions[ev.Session] = struct{}{}
	if !ev.Time.IsZero() {
		t.lastSeen = max(t.lastSeen, ev.Time.Local().Format(time.DateOnly))
	}
	if t.exemplar == "" {
		t.exemplar = ev.Detail
	}
}

func (sc *flagScan) agg(key, form string) *flagAgg {
	k := flagKey{key, form}
	a := sc.aggs[k]
	if a == nil {
		a = &flagAgg{callSessions: map[string]struct{}{}}
		for c := range a.tally {
			a.tally[c].sessions = map[string]struct{}{}
		}
		sc.aggs[k] = a
	}
	return a
}

// flagRow folds an aggregate's counted classes into a row; ok is false when
// none counts. Inferred chains need corroboration. A form is proven when
// measured chains outnumber its clean standalone successes (`loto lock -t a
// b` is valid — intent a, target b — but fails far more often than not).
// Retry chains on their own are the everyday `cmd 2>&1 | head` followed by
// the same tool with other flags, so they count only for a proven form.
// Guess chains count unless a clean success disproves the form — the agent
// itself wrote the fallback that drops the flag.
func (a *flagAgg) flagRow(k flagKey) (FlagRow, bool) {
	proven := a.tally[classMeasured].n > a.cleanOK
	counted := []chainClass{classMeasured}
	if proven || a.cleanOK == 0 {
		counted = append(counted, classGuess)
	}
	if proven {
		counted = append(counted, classRetry)
	}
	row := FlagRow{Key: k.key, Flag: k.flag, Calls: a.calls, CallSessions: len(a.callSessions)}
	sessions := map[string]struct{}{}
	for _, c := range counted {
		t := &a.tally[c]
		if c == classMeasured {
			row.Measured += t.n
		} else {
			row.Inferred += t.n
		}
		maps.Copy(sessions, t.sessions)
		row.LastSeen = max(row.LastSeen, t.lastSeen)
		row.Exemplar = cmp.Or(row.Exemplar, t.exemplar)
	}
	row.Chains = row.Measured + row.Inferred
	if row.Chains == 0 {
		return FlagRow{}, false
	}
	row.Command = strings.Replace(strings.TrimPrefix(k.key, "sh:"), "_", " ", 1)
	row.Sessions = len(sessions)
	row.Score = float64(row.Chains) * float64(row.Sessions)
	return row, true
}

// rank projects aggregates with ≥1 counted chain into rows: Chains ×
// Sessions descending, then chains, then key and flag for byte-stable output.
func (sc *flagScan) rank() []FlagRow {
	var rows []FlagRow
	for k, a := range sc.aggs {
		if row, ok := a.flagRow(k); ok {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(x, y FlagRow) int {
		return cmp.Or(
			cmp.Compare(y.Score, x.Score),
			cmp.Compare(y.Chains, x.Chains),
			cmp.Compare(x.Key, y.Key),
			cmp.Compare(x.Flag, y.Flag),
		)
	})
	return rows
}

// toolOf is a command key's tool: "bd_list" → "bd".
func toolOf(action string) string {
	tool, _, _ := strings.Cut(action, "_")
	return tool
}

// formsOf returns event i's distinct flag forms, computed once.
func (sc *flagScan) formsOf(i int) []string {
	if f, ok := sc.forms[i]; ok {
		return f
	}
	f := flagForms(&sc.events[i])
	sc.forms[i] = f
	return f
}

// flagForms is a shell event's distinct flag forms. Event.Flags is the
// source (parsed at ingest from the untruncated command); an older corpus
// without it falls back to parsing the truncated Detail.
func flagForms(ev *event.Event) []string {
	names := ev.Flags
	if len(names) == 0 && strings.Contains(ev.Detail, " -") {
		names = shellnorm.Flags(ev.Detail)
	}
	if len(names) == 0 {
		return nil
	}
	words := shellWords(ev.Detail)
	var forms []string
	for _, name := range names {
		form := name
		if pathShaped(flagValue(words, name)) {
			form = name + " <path>"
		}
		if !slices.Contains(forms, form) {
			forms = append(forms, form)
		}
	}
	return forms
}

// flagValue returns the word after the first occurrence of flag name, or
// the value of a `name=value` word; "" when there is none.
func flagValue(words []string, name string) string {
	for i, w := range words {
		if v, ok := strings.CutPrefix(w, name+"="); ok {
			return v
		}
		if w == name {
			if i+1 < len(words) {
				return words[i+1]
			}
			return ""
		}
	}
	return ""
}

// shellWords splits a command into words, honoring single and double quotes
// (quotes removed). Cheap and tolerant: Detail may be cut mid-quote.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

var (
	// fileName is a bare file name with a letter-led extension: a.go, SKILL.md.
	// Bead ids (mn-lg8.11) and versions (1.5) fail the letter-led extension.
	fileName = regexp.MustCompile(`^[\w.-]+\.[A-Za-z][A-Za-z0-9]{0,5}$`)
	// shellVar is a bare variable reference: $F, ${W}, $PWD.
	shellVar = regexp.MustCompile(`^\$\{?[A-Za-z_]\w*\}?$`)
)

// pathShaped reports whether a flag value looks like a path: it holds a
// slash, is . or .., is a bare file name, or is a bare variable (agents pass
// paths as $F, "$PWD"). Whitespace, redirects and other flags are never paths.
func pathShaped(v string) bool {
	if v == "" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t<>|;&") {
		return false
	}
	return strings.Contains(v, "/") || v == "." || v == ".." ||
		fileName.MatchString(v) || shellVar.MatchString(v)
}

var (
	// flagComplaint finds the phrase with which a CLI rejects a flag.
	flagComplaint = regexp.MustCompile(`(?i)unknown (?:shorthand )?flag|flag provided but not defined|unknown option|unrecognized option|invalid option|illegal option|unexpected argument|no such option`)
	// flagWord is a flag spelled out in error text; quotedLetter is a
	// shorthand quoted bare (`invalid option -- 'x'`).
	flagWord     = regexp.MustCompile(`-{1,2}[A-Za-z][\w-]*`)
	quotedLetter = regexp.MustCompile(`'([A-Za-z])'`)
	// invocationComplaint is error text that complains about how the command
	// was called — the precondition for blaming a flag the retry dropped.
	invocationComplaint = regexp.MustCompile(`(?im)^\s*(?:error:\s*)?usage:|unknown (?:shorthand )?flag|flag provided but not defined|unknown option|unrecognized option|invalid option|illegal option|unexpected argument|no such option|unknown command|requires? an? argument|missing (?:required )?argument|accepts? (?:at most |exactly |between )?\d+ arg|flag after the words|not supported with`)
)

// namedFlag returns the form whose flag the error text rejects by name, or
// "" when the text names none of the call's own flags.
func namedFlag(errText string, forms []string) string {
	loc := flagComplaint.FindStringIndex(errText)
	if loc == nil {
		return ""
	}
	line, _, _ := strings.Cut(errText[loc[1]:], "\n")
	cands := flagWord.FindAllString(line, -1)
	for _, m := range quotedLetter.FindAllStringSubmatch(line, -1) {
		cands = append(cands, "-"+m[1])
	}
	for _, c := range cands {
		for _, f := range forms {
			if name, _, _ := strings.Cut(f, " "); name == c {
				return f
			}
		}
	}
	return ""
}
