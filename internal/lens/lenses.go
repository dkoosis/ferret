package lens

import (
	"path/filepath"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/shellnorm"
)

// ---- coarse: behavior classes (read, search, edit, test, vcs, agent…) ----

type coarse struct{}

func (coarse) Name() string { return "coarse" }

// Coarse behavior classes. Exported so string-facing consumers (internal/reach)
// project their own buckets onto the SAME class source the event lens uses,
// instead of re-listing commands in a parallel table that drifts.
const (
	ClassRead   = "read"
	ClassSearch = "search"
	ClassVCS    = "vcs"
)

// Short internal aliases for the table literals below.
const (
	clsRead   = ClassRead
	clsSearch = ClassSearch
)

var coarseTool = map[string]string{
	"Read": clsRead, "NotebookRead": clsRead,
	"Grep": clsSearch, "Glob": clsSearch, "WebSearch": clsSearch, "ToolSearch": clsSearch,
	"Edit": "edit", "Write": "edit", "NotebookEdit": "edit",
	"Task": "agent", "Agent": "agent",
	"WebFetch":  "fetch",
	"TodoWrite": "plan", "ExitPlanMode": "plan", "EnterPlanMode": "plan",
	"AskUserQuestion": "ask",
	"Skill":           "skill", "SlashCommand": "skill",
}

var coarseShell = map[string]string{
	"go_test": "test", "go_build": "build", "go_vet": "lint", "make": "build",
	"npm_test": "test", "npm_run": "build", "vitest": "test", "pytest": "test",
	"golangci-lint": "lint",
	"rg":            clsSearch, "grep": clsSearch, "fd": clsSearch, "find": clsSearch,
	"egrep": clsSearch, "fgrep": clsSearch, "ripgrep": clsSearch, "ag": clsSearch,
	"cat": clsRead, "bat": clsRead, "head": clsRead, "tail": clsRead,
	"ls": clsRead, "eza": clsRead, "tree": clsRead, "dtree": clsRead, "wc": clsRead, "jq": clsRead,
}

// ShellReadSearch returns every normalized shell command coarseShell classifies
// as generic read or search — the set reach projects onto ReachGrep. Exported so
// the reach drift guard iterates the real table instead of a hand-copied subset:
// a search/read tool added to coarseShell is then covered for free.
func ShellReadSearch() []string {
	var cmds []string
	for cmd, cls := range coarseShell {
		if cls == ClassRead || cls == ClassSearch {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func (coarse) Token(e *event.Event) (string, bool) {
	switch e.Kind {
	case event.KindPrompt:
		return "prompt", true
	case event.KindTool:
		if isMCP(e.Action) {
			return "mcp", true
		}
		if c, ok := coarseTool[e.Action]; ok {
			return c, true
		}
		return "tool", true
	case event.KindShell:
		if c, ok := coarseShell[e.Action]; ok {
			return c, true
		}
		if IsVCS(e.Action) {
			return ClassVCS, true
		}
		return "sh", true
	}
	return "", false
}

// IsVCS reports whether a normalized shell command (shellnorm's Segment.Cmd —
// "git", "git_commit", "gh", "gh_pr", …) belongs to the version-control family.
// It is the single source of truth for the coarse lens's "vcs" class and is
// reused by the terminal-action outcome label (ferret-kuv.8) so the two never
// drift on what counts as a VCS call. NOTE: this is the FAMILY gate — it is true
// for read-only calls too (git_status, git_diff). A consumer that needs the
// mutating/terminal subset (commit/push/PR) must narrow further itself.
func IsVCS(action string) bool {
	return strings.HasPrefix(action, "git_") || action == "git" ||
		action == "gh" || strings.HasPrefix(action, "gh_")
}

// ShellClass returns the coarse behavior class for a shellnorm-normalized command
// (Segment.Cmd, e.g. "rg", "git_log", "make"). It is the string-facing twin of
// coarse.Token's KindShell branch — same coarseShell table, same IsVCS family
// gate — exported so command-string consumers (internal/reach's reach-channel
// taxonomy) classify against THIS single source instead of re-listing the
// commands, so the two never drift. A search/read tool added to coarseShell then
// buckets consistently in every consumer. ok=false means unclassified (the coarse
// lens would emit the "sh" catch-all). NOTE: the "vcs" class is the FAMILY gate
// (true for git_commit/push/status too); a consumer wanting only the read subset
// must narrow further itself.
func ShellClass(cmd string) (cls string, ok bool) {
	if c, ok := coarseShell[cmd]; ok {
		return c, true
	}
	if IsVCS(cmd) {
		return ClassVCS, true
	}
	return "", false
}

// ---- tool: tool identity (Read, sh:git_diff, mcp:trixi.set_nug) ----

type tool struct{}

func (tool) Name() string { return "tool" }

func (tool) Token(e *event.Event) (string, bool) {
	switch e.Kind {
	case event.KindPrompt:
		return "prompt", true
	case event.KindTool:
		if isMCP(e.Action) {
			return mcpShort(e.Action), true
		}
		return e.Action, true
	case event.KindShell:
		return "sh:" + e.Action, true
	}
	return "", false
}

// ---- target: tool + target class (Edit:.go, Read:.md) ----

type target struct{}

func (target) Name() string { return "target" }

func (target) Token(e *event.Event) (string, bool) {
	base, ok := tool{}.Token(e)
	if !ok {
		return "", false
	}
	if e.Kind == event.KindTool && e.Target != "" {
		if ext := filepath.Ext(e.Target); ext != "" && len(ext) <= 8 && !strings.ContainsAny(ext, " /*") {
			return base + ":" + ext, true
		}
	}
	return base, true
}

// ---- confidence: tool identity + snipe's fallback marker ----
//
// Identical to the tool lens except a call snipe served via fuzzy/semantic
// fallback (Event.Approx, parsed from the tool_result body) gets a "~approx"
// suffix. That splits one opaque "sh:snipe_callers" token into served vs
// almost-missed, so the sequence miner reads "snipe~approx → sh:rg" as
// friction-with-cause rather than two indistinguishable calls.
type confidence struct{}

func (confidence) Name() string { return "confidence" }

func (confidence) Token(e *event.Event) (string, bool) {
	base, ok := tool{}.Token(e)
	if !ok {
		return "", false
	}
	if e.Approx {
		return base + "~approx", true
	}
	return base, true
}

// ---- cmd: tool identity + option names, values stripped ----
//
// The lens between tool and exact (ferret-jtv). For shell, tool is too coarse
// to tell `bd list --status in_progress --json` from a bare `bd list`, while
// exact is unusable: every path and search string is unique, so a shell
// sequence repeats corpus-wide only a couple of dozen times and the miner
// sees no routine at all. Keeping the flag NAMES and dropping their values
// splits the invocations that genuinely differ and folds the ones that differ
// only in an argument — which is what lets a repeated sequence read as one
// consolidation candidate rather than as noise.
//
// Non-shell events are unchanged from the tool lens: their identity is the
// tool name, and Detail there is a target, not a command line.
type cmd struct{}

func (cmd) Name() string { return "cmd" }

func (cmd) Token(e *event.Event) (string, bool) {
	base, ok := tool{}.Token(e)
	if !ok {
		return "", false
	}
	if e.Kind != event.KindShell || e.Detail == "" {
		return base, true
	}
	flags := shellnorm.Flags(e.Detail)
	if len(flags) == 0 {
		return base, true
	}
	return base + " " + strings.Join(flags, " "), true
}

// ---- exact: tool + full normalized target ----

type exact struct{}

func (exact) Name() string { return "exact" }

func (exact) Token(e *event.Event) (string, bool) {
	base, ok := tool{}.Token(e)
	if !ok {
		return "", false
	}
	if e.Target != "" {
		return base + ":" + e.Target, true
	}
	if e.Kind == event.KindShell && e.Detail != "" {
		return base + ":" + e.Detail, true
	}
	return base, true
}
