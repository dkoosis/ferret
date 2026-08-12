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
	// Swallowed marks a segment sitting in the left arm of an `||` whose
	// stderr is redirected to /dev/null — `cmd 2>/dev/null || fallback`. Such a
	// segment can fail without leaving any trace ferret can see: the error text
	// is discarded and the chain's exit code is the *fallback's*, so the
	// tool_result carries no is_error flag and internal/event's resolve() marks
	// the whole call ok. Set by Split so ingest can carry the tell forward
	// per-segment; the predicate form for a whole command line is Swallows.
	Swallowed bool
	// Piped marks a segment that came from a pipeline (`a | b`) — info
	// fromBinaryCmd's Pipe/PipeAll arm dissolves by collapsing to a single
	// command, the same one-way loss Swallowed exists to capture. A
	// substitution detector needs this: `rg foo | head` truncates rg's output,
	// so the pipe itself is an escape hatch from "rg alone could replace this."
	Piped bool
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
	if seg, capped := recurseCap(st, pr, depth); capped {
		return seg
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		if seg, ok := fromCall(c, st, pr); ok {
			return []Segment{seg}
		}
		return nil
	case *syntax.BinaryCmd:
		return fromBinaryCmd(c, pr, depth)
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

// fromBinaryCmd handles the && / || / | / |& operators — split out of
// fromStmt so its nested op-switch doesn't count against fromStmt's own
// cognitive-complexity budget.
func fromBinaryCmd(c *syntax.BinaryCmd, pr *syntax.Printer, depth int) []Segment {
	switch c.Op {
	case syntax.AndStmt, syntax.OrStmt:
		left := fromStmt(c.X, pr, depth+1)
		if c.Op == syntax.OrStmt && silencesStderr(c.X, depth+1) {
			markSwallowed(left)
		}
		return append(left, fromStmt(c.Y, pr, depth+1)...)
	case syntax.Pipe, syntax.PipeAll:
		// a pipeline collapses to its first non-trivial command
		if left := fromStmt(c.X, pr, depth+1); len(left) > 0 {
			markPiped(left)
			return left
		}
		right := fromStmt(c.Y, pr, depth+1)
		markPiped(right)
		return right
	}
	return nil
}

// recurseCap reports whether depth has hit maxRecurseDepth — pathologically
// nested input degrades to a single Segment{Cmd:"complex"} at the ceiling
// instead of mirroring its AST depth into fromStmt's unbounded recursion.
func recurseCap(st *syntax.Stmt, pr *syntax.Printer, depth int) ([]Segment, bool) {
	if depth > maxRecurseDepth {
		return []Segment{{Cmd: "complex", Raw: printStmt(st, pr)}}, true
	}
	return nil, false
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

// The SWALLOWED-ERROR tell (ferret-cax) ------------------------------------
//
// ferret's misfire signal is downstream of one bit: the tool_result is_error
// flag that internal/event/build.go's resolve() reads. A guess-chain written
// as `cmd 2>/dev/null || fallback` defeats that bit twice over — the error
// text goes to /dev/null, and the chain's exit code is the fallback's, so the
// shell exits 0 and the whole call records as a success. The failure that
// motivated the retry is invisible to `ferret misfires`.
//
// Recovering it needs no new runtime signal, only the command text: the shape
// itself is the tell. Both halves are load-bearing and neither alone qualifies
// — `2>/dev/null` on its own silences the message but the return code still
// surfaces, and `|| fallback` on its own still prints the error that ends the
// chain. Only the pair hides a failure completely.

// Swallows reports whether command hides a failure from ferret's is_error
// signal: it contains an `||` whose LEFT arm redirects stderr to /dev/null.
// The recognized silencing forms are `2>/dev/null`, `2>>/dev/null`,
// `&>/dev/null`, `&>>/dev/null`, and `>/dev/null 2>&1` (in that order — the
// reversed `2>&1 >/dev/null` sends stderr to the *old* stdout and is
// deliberately not a match). Redirects on the right arm do not count: a
// swallowed right arm still leaves the left arm's error on the terminal.
//
// A command that fails to parse yields false — a swallow is never guessed
// from raw text, so the resulting count stays a floor rather than an estimate.
func Swallows(command string) bool {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	return anySwallows(file.Stmts, 0)
}

func anySwallows(sts []*syntax.Stmt, depth int) bool {
	for _, st := range sts {
		if swallowsStmt(st, depth) {
			return true
		}
	}
	return false
}

// swallowsStmt looks for an OrStmt whose left arm silences stderr, descending
// through compound commands under the same maxRecurseDepth ceiling fromStmt
// respects — a nested `a && (b 2>/dev/null || c)` is still a swallow.
func swallowsStmt(st *syntax.Stmt, depth int) bool {
	if st == nil || st.Cmd == nil || depth > maxRecurseDepth {
		return false
	}
	if bc, ok := st.Cmd.(*syntax.BinaryCmd); ok && bc.Op == syntax.OrStmt && silencesStderr(bc.X, depth+1) {
		return true
	}
	return anySwallows(childStmts(st.Cmd), depth+1)
}

// silencesStderr reports whether st — or any statement nested inside it, as in
// `{ cmd 2>/dev/null; } || fallback` — discards stderr. The redirect can also
// ride the compound statement itself (`{ cmd; } 2>/dev/null`), which is why
// st.Redirs is checked before descending.
func silencesStderr(st *syntax.Stmt, depth int) bool {
	if st == nil || depth > maxRecurseDepth {
		return false
	}
	if redirsSilenceStderr(st.Redirs) {
		return true
	}
	if st.Cmd == nil {
		return false
	}
	for _, child := range childStmts(st.Cmd) {
		if silencesStderr(child, depth+1) {
			return true
		}
	}
	return false
}

// redirsSilenceStderr scans one statement's redirect list in source order,
// because order decides the verdict: `>/dev/null 2>&1` points fd 2 at the
// already-nulled fd 1, while `2>&1 >/dev/null` points it at the terminal fd 1
// and only then nulls stdout — the error still prints.
func redirsSilenceStderr(redirs []*syntax.Redirect) bool {
	stdoutNulled := false
	for _, r := range redirs {
		switch {
		case toDevNull(r) && redirFD(r) == "2":
			return true // 2>/dev/null, 2>>/dev/null
		case toDevNullAll(r):
			return true // &>/dev/null, &>>/dev/null
		case toDevNull(r) && (redirFD(r) == "" || redirFD(r) == "1"):
			stdoutNulled = true
		case stdoutNulled && r.Op == syntax.DplOut && redirFD(r) == "2" && wordLit(r.Word) == "1":
			return true // >/dev/null 2>&1
		}
	}
	return false
}

func toDevNull(r *syntax.Redirect) bool {
	return (r.Op == syntax.RdrOut || r.Op == syntax.AppOut) && wordLit(r.Word) == devNull
}

func toDevNullAll(r *syntax.Redirect) bool {
	return (r.Op == syntax.RdrAll || r.Op == syntax.AppAll) && wordLit(r.Word) == devNull
}

// redirFD is the explicit file descriptor a redirect names ("2" in
// `2>/dev/null`), or "" when the form leaves it implicit (`>/dev/null`, `&>`).
func redirFD(r *syntax.Redirect) string {
	if r.N == nil {
		return ""
	}
	return r.N.Value
}

const devNull = "/dev/null"

// childStmts lists the statements a compound command owns, covering the same
// node types fromStmt descends into so the swallow walk and the segment walk
// never disagree about what "inside this command" means.
func childStmts(cmd syntax.Command) []*syntax.Stmt {
	switch c := cmd.(type) {
	case *syntax.BinaryCmd:
		return []*syntax.Stmt{c.X, c.Y}
	case *syntax.Subshell:
		return c.Stmts
	case *syntax.Block:
		return c.Stmts
	case *syntax.IfClause:
		return concatStmts(c.Cond, c.Then)
	case *syntax.WhileClause:
		return concatStmts(c.Cond, c.Do)
	case *syntax.ForClause:
		return c.Do
	case *syntax.TimeClause:
		return []*syntax.Stmt{c.Stmt}
	}
	return nil
}

func concatStmts(a, b []*syntax.Stmt) []*syntax.Stmt {
	out := make([]*syntax.Stmt, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

// markSwallowed flags every segment Split produced from a swallowing left arm.
// The whole arm is marked, not just its first command: in
// `a | b 2>/dev/null || fallback` any of the arm's normalized tokens is a
// plausible owner of the hidden failure, and over-attributing within one arm
// is cheaper than dropping the tell.
func markSwallowed(segs []Segment) {
	for i := range segs {
		segs[i].Swallowed = true
	}
}

// markPiped flags every segment fromBinaryCmd's Pipe/PipeAll arm produced —
// sibling of markSwallowed, same all-segments-in-the-arm rationale: a
// pipeline's collapsed-to command may itself be compound (rare, but
// `a && b | c` nests), and every segment in that arm shares the same
// "this ran inside a pipe" fact.
func markPiped(segs []Segment) {
	for i := range segs {
		segs[i].Piped = true
	}
}

// Argv (ferret-cax item 3) extracts the literal argv of a plain, single,
// redirect-free shell command — the scan internal/mine's substitution
// detector reads verb/flags/args from. plain is false whenever raw is not
// exactly that shape: a parse failure, more than one top-level statement, a
// negated/backgrounded/coprocess statement, any redirect, a compound command
// (pipe, &&/||, subshell, block, ...), or an argument that is not a pure
// literal (a parameter expansion `$VAR`, command substitution `$( )`,
// arithmetic expansion, process substitution, extended glob, or brace
// expansion). A redirect or expansion changes what the program actually
// receives or does — exactly the cases where guessing from text would be
// unsafe — so the detector must decline rather than parse around them.
//
// Single- and double-quoted literal spans (`'pattern'`, `"10,20p"`) do count
// as plain: the quotes are shell syntax, not part of the value the program
// receives, so they are stripped like Split's wordLit already does for the
// argv0/subcommand case.
func Argv(raw string) (argv []string, plain bool) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(raw), "")
	if err != nil || len(file.Stmts) != 1 {
		return nil, false
	}
	st := file.Stmts[0]
	if st.Negated || st.Background || st.Coprocess || len(st.Redirs) > 0 {
		return nil, false
	}
	call, ok := st.Cmd.(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	argv = make([]string, 0, len(call.Args))
	for _, w := range call.Args {
		lit, ok := plainWordLit(w)
		if !ok {
			return nil, false
		}
		argv = append(argv, lit)
	}
	return argv, true
}

// plainWordLit returns a word's literal value when every part is a bare
// literal or a single-/double-quoted span whose own contents are pure
// literals too — the forms a plain argv token can take. Any expansion fails
// the word, mirroring Word.Lit()'s all-*Lit rule but additionally accepting
// quoted spans (Word.Lit alone returns "" for `'pattern'`, which would wrongly
// reject a quoted literal argument as non-plain).
func plainWordLit(w *syntax.Word) (string, bool) {
	var sb strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			sb.WriteString(p.Value)
		case *syntax.SglQuoted:
			sb.WriteString(p.Value)
		case *syntax.DblQuoted:
			s, ok := plainWordPartsLit(p.Parts)
			if !ok {
				return "", false
			}
			sb.WriteString(s)
		default:
			return "", false
		}
	}
	return sb.String(), true
}

// plainWordPartsLit requires every part of a double-quoted span to be a bare
// literal — a `"$VAR"` interpolation fails here even though the outer word is
// double-quoted, because the inner part is a ParamExp, not a Lit.
func plainWordPartsLit(parts []syntax.WordPart) (string, bool) {
	var sb strings.Builder
	for _, part := range parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		sb.WriteString(lit.Value)
	}
	return sb.String(), true
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
