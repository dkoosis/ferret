package mine

import (
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/event"
)

// flagEv builds one shell segment event for the wrong-flag fixtures. seq is
// the Bash call it belongs to: segments of one call share a seq, exactly as
// ingest writes them.
func flagEv(session string, seq int, day, action, detail, status string, flags ...string) event.Event {
	day0, _ := time.ParseInLocation(time.DateOnly, day, time.Local)
	ts := day0.Add(12 * time.Hour) // noon local: LastSeen is a local day, like the --since window
	return event.Event{
		Seq: seq, Time: ts, Session: session, Kind: event.KindShell,
		Action: action, Detail: detail, Status: status, Flags: flags,
	}
}

// findFlagRow returns the row for (key, flag), or nil.
func findFlagRow(rows []FlagRow, key, flag string) *FlagRow {
	for i := range rows {
		if rows[i].Key == key && rows[i].Flag == flag {
			return &rows[i]
		}
	}
	return nil
}

// AC1: bd list --parent X --short fails with "unknown flag: --short" and the
// next bd call drops the flag → one row naming bd list and --short.
func TestMineFlagMisfires_NamesCommandAndFlag_When_UnknownFlagIsRetriedWithoutIt(t *testing.T) {
	failed := flagEv("s1", 1, "2026-10-12", "bd_list", "bd list --parent ferret-x --short", event.StatusFail, "--parent", "--short")
	failed.Err = "Exit code 1\nError: unknown flag: --short"
	evs := []event.Event{
		failed,
		flagEv("s1", 2, "2026-10-12", "bd_list", "bd list --parent ferret-x", event.StatusOK, "--parent"),
	}
	rows := MineFlagMisfires(evs)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v; want exactly one", rows)
	}
	r := rows[0]
	if r.Command != "bd list" || r.Flag != "--short" {
		t.Errorf("row names %q %q; want bd list --short", r.Command, r.Flag)
	}
	if r.Chains != 1 || r.Measured != 1 || r.Inferred != 0 || r.Sessions != 1 || r.LastSeen != "2026-10-12" {
		t.Errorf("row = %+v; want chains=1 measured=1 sessions=1 last=2026-10-12", r)
	}
}

// AC2: `bd list --short 2>/dev/null || bd show X` exits 0 — the swallow hid
// the exit code, the fallback bd call dropped the flag → an inferred row.
func TestMineFlagMisfires_MarksRowInferred_When_SwallowedCallFallsBackWithoutFlag(t *testing.T) {
	left := flagEv("s1", 1, "2026-10-12", "bd_list", "bd list --short 2>/dev/null", event.StatusOK, "--short")
	left.Swallow = true
	left.Compound = true
	right := flagEv("s1", 1, "2026-10-12", "bd_show", "bd show ferret-x", event.StatusOK)
	right.Compound = true
	rows := MineFlagMisfires([]event.Event{left, right})
	r := findFlagRow(rows, "sh:bd_list", "--short")
	if r == nil {
		t.Fatalf("rows = %+v; want a bd list --short row", rows)
	}
	if r.Inferred != 1 || r.Measured != 0 || r.Chains != 1 {
		t.Errorf("row = %+v; want inferred=1 measured=0 — counted apart from measured", r)
	}
}

// AC3: loto lock prints a usage block because a peer holds the lock; the
// next call carries the same flags → no row.
func TestMineFlagMisfires_YieldsNoRow_When_UsageBlockHasNoFlagChangeAfter(t *testing.T) {
	blocked := flagEv("s1", 1, "2026-10-12", "loto_lock", `loto lock a.go -t "fix x"`, event.StatusFail, "-t")
	blocked.Err = "Exit code 2\nusage: loto lock <target> [<target>...] -t \"why\""
	evs := []event.Event{
		blocked,
		flagEv("s1", 2, "2026-10-12", "loto_lock", `loto lock a.go -t "fix x"`, event.StatusOK, "-t"),
	}
	if rows := MineFlagMisfires(evs); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — a usage block with no flag change is not a wrong flag", rows)
	}
	if rows := MineFlagMisfires(evs[:1]); len(rows) != 0 {
		t.Errorf("rows = %+v; want none for a lone usage block", rows)
	}
}

// loto lock -t <path> keeps the same flag NAME when fixed (`loto lock <path>
// -t "why"`); the path-shaped value is what changed, so it is the form named.
func TestMineFlagMisfires_NamesPathShapedForm_When_FlagValueWasAPath(t *testing.T) {
	bad := flagEv("s1", 1, "2026-10-12", "loto_lock", "loto lock -t internal/a.go 2>&1", event.StatusFail, "-t")
	bad.Err = "Exit code 2\nusage: loto lock <target> [<target>...] -t \"why\""
	evs := []event.Event{
		bad,
		flagEv("s1", 2, "2026-10-12", "loto_lock", `loto lock internal/a.go -t "fix x"`, event.StatusOK, "-t"),
	}
	rows := MineFlagMisfires(evs)
	r := findFlagRow(rows, "sh:loto_lock", "-t <path>")
	if r == nil || r.Measured != 1 {
		t.Fatalf("rows = %+v; want one measured loto lock '-t <path>' row", rows)
	}
}

// A retry outside the three-call window is not a chain.
func TestMineFlagMisfires_IgnoresRetry_When_OutsideThreeCallWindow(t *testing.T) {
	bad := flagEv("s1", 1, "2026-10-12", "loto_lock", "loto lock -t a.go", event.StatusFail, "-t")
	bad.Err = "usage: loto lock <target> -t \"why\""
	evs := []event.Event{
		bad,
		flagEv("s1", 2, "2026-10-12", "git_status", "git status", event.StatusOK),
		flagEv("s1", 3, "2026-10-12", "git_status", "git status", event.StatusOK),
		flagEv("s1", 4, "2026-10-12", "git_status", "git status", event.StatusOK),
		flagEv("s1", 5, "2026-10-12", "loto_lock", `loto lock a.go -t "x"`, event.StatusOK, "-t"),
	}
	if rows := MineFlagMisfires(evs); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — the retry came four calls later", rows)
	}
}

// A failure that does not complain about its invocation (grep's no-match
// exit 1) is not a wrong flag even when the next call changes flags.
func TestMineFlagMisfires_IgnoresPlainFailure_When_ErrorTextIsNotAComplaint(t *testing.T) {
	miss := flagEv("s1", 1, "2026-10-12", "grep", "grep -n foo a.go", event.StatusFail, "-n")
	miss.Err = "Exit code 1"
	evs := []event.Event{
		miss,
		flagEv("s1", 2, "2026-10-12", "grep", "grep -rn foo .", event.StatusOK, "-rn"),
	}
	if rows := MineFlagMisfires(evs); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — exit 1 with no complaint is not a misfire", rows)
	}
}

// Rows rank by chains × sessions; calls counts every call carrying the flag.
func TestMineFlagMisfires_RanksByChainsTimesSessions(t *testing.T) {
	var evs []event.Event
	unknown := func(s string, seq int, action, detail, flag string) event.Event {
		e := flagEv(s, seq, "2026-10-12", action, detail, event.StatusFail, flag)
		e.Err = "Error: unknown flag: " + flag
		return e
	}
	// --text: 2 chains in 2 sessions → score 4.
	evs = append(evs, unknown("a", 1, "bd_comment", "bd comment x --text y", "--text"),
		unknown("b", 1, "bd_comment", "bd comment x --text y", "--text"))
	// --add-label: 3 chains in 1 session → score 3.
	for i := 1; i <= 3; i++ {
		evs = append(evs, unknown("c", i, "bd_create", "bd create t --add-label x", "--add-label"))
	}
	// one more --text call that never chained: counted in calls only.
	evs = append(evs, flagEv("d", 1, "2026-10-12", "bd_comment", "bd comment x --text y", event.StatusOK, "--text"))
	rows := MineFlagMisfires(evs)
	if len(rows) != 2 || rows[0].Flag != "--text" || rows[1].Flag != "--add-label" {
		t.Fatalf("rows = %+v; want --text (4) above --add-label (3)", rows)
	}
	if rows[0].Score != 4 || rows[1].Score != 3 {
		t.Errorf("scores = %v, %v; want 4, 3", rows[0].Score, rows[1].Score)
	}
	if rows[0].Calls != 3 || rows[0].CallSessions != 3 {
		t.Errorf("--text calls = %d/%d; want 3 calls in 3 sessions", rows[0].Calls, rows[0].CallSessions)
	}
}

// Subagents share the parent's session id; a parent call and a subagent call
// are not one stream, so a subagent's retry cannot close a parent's chain.
func TestMineFlagMisfires_KeepsAgentStreamsApart(t *testing.T) {
	bad := flagEv("s1", 1, "2026-10-12", "loto_lock", "loto lock -t a.go", event.StatusFail, "-t")
	bad.Err = "usage: loto lock <target> -t \"why\""
	fix := flagEv("s1", 2, "2026-10-12", "loto_lock", `loto lock a.go -t "x"`, event.StatusOK, "-t")
	fix.Agent = "sub1"
	if rows := MineFlagMisfires([]event.Event{bad, fix}); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — the fix ran in another agent's stream", rows)
	}
}

func TestPathShaped(t *testing.T) {
	for v, want := range map[string]bool{
		"internal/a.go": true, "a.go": true, ".": true, "$PWD": true, "${W}": true,
		"fix x": false, "2>&1": false, "2>/dev/null": false, "mn-lg8.11": false,
		"60m": false, "--json": false, "": false, "ccp-rasq": false,
	} {
		if got := pathShaped(v); got != want {
			t.Errorf("pathShaped(%q) = %v; want %v", v, got, want)
		}
	}
}

// A hidden-exit retry is counted only for a form some call's own exit code
// proved wrong: `grep -n x 2>&1 | head` then `grep -rn x .` is exploration.
func TestMineFlagMisfires_CountsRetryInference_OnlyForProvenForm(t *testing.T) {
	piped := func(s string, seq int, action, detail string, flags ...string) event.Event {
		return flagEv(s, seq, "2026-10-12", action, detail, event.StatusUnmeasured, flags...)
	}
	unproven := []event.Event{
		piped("s1", 1, "grep", "grep -n x a.go", "-n"),
		flagEv("s1", 2, "2026-10-12", "grep", "grep -rn x .", event.StatusOK, "-rn"),
	}
	if rows := MineFlagMisfires(unproven); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — nothing proved -n wrong", rows)
	}
	proof := flagEv("s0", 1, "2026-10-11", "bd_list", "bd list --short", event.StatusFail, "--short")
	proof.Err = "Error: unknown flag: --short"
	proven := []event.Event{
		proof,
		piped("s1", 1, "bd_list", "bd list --short 2>&1", "--short"),
		flagEv("s1", 2, "2026-10-12", "bd_list", "bd list", event.StatusOK),
	}
	r := findFlagRow(MineFlagMisfires(proven), "sh:bd_list", "--short")
	if r == nil || r.Measured != 1 || r.Inferred != 1 || r.Sessions != 2 {
		t.Errorf("row = %+v; want measured=1 inferred=1 sessions=2", r)
	}
}

// A guess chain cannot blame a form a standalone call ran cleanly with.
func TestMineFlagMisfires_VetoesGuess_When_FormRanCleanlyAlone(t *testing.T) {
	left := flagEv("s1", 1, "2026-10-12", "bd_list", "bd list --all 2>/dev/null", event.StatusOK, "--all")
	left.Swallow, left.Compound = true, true
	right := flagEv("s1", 1, "2026-10-12", "bd_show", "bd show x", event.StatusOK)
	right.Compound = true
	clean := flagEv("s2", 1, "2026-10-12", "bd_list", "bd list --all", event.StatusOK, "--all")
	if rows := MineFlagMisfires([]event.Event{left, right, clean}); len(rows) != 0 {
		t.Errorf("rows = %+v; want none — bd list --all ran cleanly on its own", rows)
	}
}

// A retry that got past the usage error but was refused for state reasons
// (a peer holds the lock) still closes the chain.
func TestMineFlagMisfires_ClosesChain_When_RetryFailsForStateNotUsage(t *testing.T) {
	bad := flagEv("s1", 1, "2026-10-12", "loto_lock", "loto lock -t a.go", event.StatusFail, "-t")
	bad.Err = "Exit code 2\nusage: loto lock <target> -t \"why\""
	blocked := flagEv("s1", 2, "2026-10-12", "loto_lock", `loto lock a.go -t "x"`, event.StatusFail, "-t")
	blocked.Err = "Exit code 1\n✗ blocked count=1"
	if r := findFlagRow(MineFlagMisfires([]event.Event{bad, blocked}), "sh:loto_lock", "-t <path>"); r == nil || r.Measured != 1 {
		t.Errorf("row = %+v; want one measured chain", r)
	}
}

// When the error text names one of the dropped flags, only that one is blamed.
func TestMineFlagMisfires_BlamesNamedFlag_When_RetryDroppedSeveral(t *testing.T) {
	bad := flagEv("s1", 1, "2026-10-12", "gh_api", "gh api x --paginate --slurp --jq .", event.StatusFail, "--paginate", "--slurp", "--jq")
	bad.Err = "Exit code 1\nthe `--slurp` option is not supported with `--jq` or `--template`"
	evs := []event.Event{bad, flagEv("s1", 2, "2026-10-12", "gh_api", "gh api x", event.StatusOK)}
	rows := MineFlagMisfires(evs)
	if len(rows) != 2 || findFlagRow(rows, "sh:gh_api", "--paginate") != nil {
		t.Errorf("rows = %+v; want --slurp and --jq (both named), not --paginate", rows)
	}
}

// Asking for help is never a wrong flag, though `x --help` exits 2 with a
// usage block and the next call drops --help.
func TestMineFlagMisfires_NeverBlamesHelp(t *testing.T) {
	help := flagEv("s1", 1, "2026-10-12", "bash", "bash ship.sh --help", event.StatusFail, "--help")
	help.Err = "Exit code 2\nusage: ship.sh -m <subject>"
	evs := []event.Event{help, flagEv("s1", 2, "2026-10-12", "bash", "bash ship.sh -m x", event.StatusOK, "-m")}
	if rows := MineFlagMisfires(evs); len(rows) != 0 {
		t.Errorf("rows = %+v; want none", rows)
	}
}

// A cross-command fallback that drops several flags is ambiguous: no blame.
func TestMineFlagMisfires_SkipsAmbiguousGuess_When_FallbackDropsSeveralFlags(t *testing.T) {
	left := flagEv("s1", 1, "2026-10-12", "bd_list", "bd list --parent x --status open 2>/dev/null", event.StatusOK, "--parent", "--status")
	left.Swallow, left.Compound = true, true
	right := flagEv("s1", 1, "2026-10-12", "bd_show", "bd show x", event.StatusOK)
	right.Compound = true
	if rows := MineFlagMisfires([]event.Event{left, right}); len(rows) != 0 {
		t.Errorf("rows = %+v; want none", rows)
	}
}
