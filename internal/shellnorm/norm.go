// Package shellnorm splits compound bash commands and normalizes each
// segment to a stable token (git checkout -b x → git_checkout).
package shellnorm

import (
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// Segment is one normalized command from a (possibly compound) bash string.
type Segment struct {
	Cmd string // normalized: base command, or base_subcommand
	Raw string // printed source of the statement, for exemplars
}

// subcmdTools take a significant first subcommand worth keeping.
var subcmdTools = map[string]bool{
	"git": true, "go": true, "npm": true, "pnpm": true, "yarn": true,
	"cargo": true, "docker": true, "gh": true, "kubectl": true,
	"make": true, "bd": true, "snipe": true, "trixi": true, "mage": true,
	"brew": true, "pip": true, "uv": true, "bun": true, "loto": true,
}

// trivial commands carry no behavioral signal on their own.
var trivial = map[string]bool{
	"cd": true, "echo": true, "true": true, "false": true, "pwd": true,
	"export": true, "set": true, "source": true, ".": true, "printf": true,
	"sleep": true, "exit": true,
}

// maxRecurseDepth bounds fromStmt's descent through nested compound commands
// (subshells, blocks, if/while/for). Pathologically nested input degrades to
// Segment{Cmd:"complex"} at the ceiling instead of mirroring its AST depth
// into unbounded recursion.
const maxRecurseDepth = 64

// Split parses a bash command line into normalized segments.
// fallback=true means the AST parse failed and a crude first-word
// normalization was used instead.
func Split(command string) (segs []Segment, fallback bool) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		if s, ok := fallbackSegment(command); ok {
			return []Segment{s}, true
		}
		return nil, true
	}
	printer := syntax.NewPrinter()
	for _, st := range file.Stmts {
		segs = append(segs, fromStmt(st, printer, 0)...)
	}
	return segs, false
}

func fromStmt(st *syntax.Stmt, pr *syntax.Printer, depth int) []Segment {
	if st == nil || st.Cmd == nil {
		return nil
	}
	if depth > maxRecurseDepth {
		return []Segment{{Cmd: "complex", Raw: printStmt(st, pr)}}
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		if seg, ok := fromCall(c, st, pr); ok {
			return []Segment{seg}
		}
		return nil
	case *syntax.BinaryCmd:
		switch c.Op {
		case syntax.AndStmt, syntax.OrStmt:
			return append(fromStmt(c.X, pr, depth+1), fromStmt(c.Y, pr, depth+1)...)
		case syntax.Pipe, syntax.PipeAll:
			// a pipeline collapses to its first non-trivial command
			if left := fromStmt(c.X, pr, depth+1); len(left) > 0 {
				return left
			}
			return fromStmt(c.Y, pr, depth+1)
		}
	case *syntax.Subshell:
		return fromStmts(c.Stmts, pr, depth+1)
	case *syntax.Block:
		return fromStmts(c.Stmts, pr, depth+1)
	case *syntax.IfClause:
		segs := fromStmts(c.Cond, pr, depth+1)
		return append(segs, fromStmts(c.Then, pr, depth+1)...)
	case *syntax.WhileClause:
		segs := fromStmts(c.Cond, pr, depth+1)
		return append(segs, fromStmts(c.Do, pr, depth+1)...)
	case *syntax.ForClause:
		return fromStmts(c.Do, pr, depth+1)
	case *syntax.TimeClause:
		return fromStmt(c.Stmt, pr, depth+1)
	}
	return nil
}

func fromStmts(sts []*syntax.Stmt, pr *syntax.Printer, depth int) []Segment {
	out := make([]Segment, 0, len(sts))
	for _, st := range sts {
		out = append(out, fromStmt(st, pr, depth)...)
	}
	return out
}

func fromCall(c *syntax.CallExpr, st *syntax.Stmt, pr *syntax.Printer) (Segment, bool) {
	if len(c.Args) == 0 {
		return Segment{}, false // pure assignment (FOO=bar)
	}
	argv0 := wordLit(c.Args[0])
	if argv0 == "" {
		return Segment{Cmd: "complex", Raw: printStmt(st, pr)}, true
	}
	base := argv0
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if trivial[base] {
		return Segment{}, false
	}
	cmd := base
	if subcmdTools[base] && len(c.Args) > 1 {
		if sub := wordLit(c.Args[1]); sub != "" && !strings.HasPrefix(sub, "-") {
			cmd = base + "_" + sub
		}
	}
	return Segment{Cmd: cmd, Raw: printStmt(st, pr)}, true
}

func wordLit(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	return w.Lit()
}

func printStmt(st *syntax.Stmt, pr *syntax.Printer) string {
	var sb strings.Builder
	_ = pr.Print(&sb, st)
	return sb.String()
}

func fallbackSegment(command string) (Segment, bool) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return Segment{}, false
	}
	base := fields[0]
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if trivial[base] {
		return Segment{}, false
	}
	raw := command
	if len(raw) > 160 {
		// Walk back to a rune boundary so the tail is never split mid-rune,
		// keeping Segment.Raw valid UTF-8 for exact-lens tokens and graph labels.
		end := 160
		for end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
		raw = raw[:end]
	}
	return Segment{Cmd: base, Raw: raw}, true
}
