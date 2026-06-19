package lens

import (
	"path/filepath"
	"strings"

	"github.com/dkoosis/ferret/internal/event"
)

// ---- coarse: behavior classes (read, search, edit, test, vcs, agent…) ----

type coarse struct{}

func (coarse) Name() string { return "coarse" }

const (
	clsRead   = "read"
	clsSearch = "search"
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
	"cat": clsRead, "bat": clsRead, "head": clsRead, "tail": clsRead,
	"ls": clsRead, "eza": clsRead, "tree": clsRead, "dtree": clsRead, "wc": clsRead, "jq": clsRead,
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
			return "vcs", true
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
