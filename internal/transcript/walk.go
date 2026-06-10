// Package transcript discovers and decodes Claude Code session transcripts.
package transcript

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// Source is one transcript file. Agent is non-empty for subagent files.
type Source struct {
	Path    string
	Project string
	Session string
	Agent   string
}

// Walk finds every *.jsonl under root (~/.claude/projects layout):
// <project-slug>/<session>.jsonl and <project-slug>/<session>/subagents/agent-*.jsonl.
func Walk(root string) ([]Source, error) {
	var out []Source
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			return nil
		}
		s := Source{Path: p, Project: parts[0]}
		base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if len(parts) >= 4 && parts[len(parts)-2] == "subagents" {
			s.Session = parts[len(parts)-3]
			s.Agent = strings.TrimPrefix(base, "agent-")
		} else {
			s.Session = base
		}
		out = append(out, s)
		return nil
	})
	return out, err
}
