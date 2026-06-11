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
		if strings.HasPrefix(e.Action, "git_") || e.Action == "git" || e.Action == "gh" || strings.HasPrefix(e.Action, "gh_") {
			return "vcs", true
		}
		return "sh", true
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
