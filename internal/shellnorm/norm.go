// Package shellnorm splits compound bash commands and normalizes each
// segment to a stable token (git checkout -b x → git_checkout).
package shellnorm

import (
	"strings"

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
		segs = append(segs, fromStmt(st, printer)...)
	}
	return segs, false
}

func fromStmt(st *syntax.Stmt, pr *syntax.Printer) []Segment {
	if st == nil || st.Cmd == nil {
		return nil
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
			return append(fromStmt(c.X, pr), fromStmt(c.Y, pr)...)
		case syntax.Pipe, syntax.PipeAll:
			// a pipeline collapses to its first non-trivial command
			if left := fromStmt(c.X, pr); len(left) > 0 {
				return left
			}
			return fromStmt(c.Y, pr)
		}
	case *syntax.Subshell:
		return fromStmts(c.Stmts, pr)
	case *syntax.Block:
		return fromStmts(c.Stmts, pr)
	case *syntax.IfClause:
		segs := fromStmts(c.Cond, pr)
		return append(segs, fromStmts(c.Then, pr)...)
	case *syntax.WhileClause:
		segs := fromStmts(c.Cond, pr)
		return append(segs, fromStmts(c.Do, pr)...)
	case *syntax.ForClause:
		return fromStmts(c.Do, pr)
	case *syntax.TimeClause:
		return fromStmt(c.Stmt, pr)
	}
	return nil
}

func fromStmts(sts []*syntax.Stmt, pr *syntax.Printer) []Segment {
	out := make([]Segment, 0, len(sts))
	for _, st := range sts {
		out = append(out, fromStmt(st, pr)...)
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
		raw = raw[:160]
	}
	return Segment{Cmd: base, Raw: raw}, true
}
