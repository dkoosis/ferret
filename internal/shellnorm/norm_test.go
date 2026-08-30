package shellnorm

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func cmds(segs []Segment) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = s.Cmd
	}
	return out
}

func TestSplit(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"git checkout -b feature/auth", []string{"git_checkout"}},
		{"go test ./...", []string{"go_test"}},
		{"cd /x && go build ./... && go test ./...", []string{"go_build", "go_test"}},
		{"echo hi; pwd", nil},
		{"cat f.json | jq '.x[]' | head -3", []string{"cat"}},
		{"FOO=bar make lint", []string{"make_lint"}},
		{"rg -n 'pattern' src/", []string{"rg"}},
		{"git -C /repo status", []string{"git"}}, // flag, not subcommand
	}
	for _, c := range cases {
		segs, _ := Split(c.in)
		got := cmds(segs)
		if len(got) != len(c.want) {
			t.Errorf("Split(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Split(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestSplitFallbackRuneBoundary covers the fallback path (AST parse fails) for
// a command whose multibyte rune straddles the 160-byte truncation point. The
// truncated Segment.Raw must remain valid UTF-8 — never split mid-rune.
func TestSplitFallbackRuneBoundary(t *testing.T) {
	// A 3-byte rune (€) whose first byte lands at offset 159 → bytes 159,160,161.
	// A naive raw[:160] keeps only byte 159, producing an invalid UTF-8 tail.
	// "git " (4) + padding (155) puts the rune's first byte at index 159.
	// An unterminated single quote forces the bash AST parse to fail.
	cmd := "git " + strings.Repeat("a", 155) + "€'unterminated"
	if cmd[159] == 0 || utf8.RuneStart(cmd[160]) {
		t.Fatalf("test setup: rune not straddling byte 160 (cmd[159]=%d cmd[160]=%d)", cmd[159], cmd[160])
	}

	segs, fb := Split(cmd)
	if !fb {
		t.Fatal("expected fallback path (parse failure)")
	}
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d", len(segs))
	}
	if got := segs[0].Raw; !utf8.ValidString(got) {
		t.Errorf("Segment.Raw is not valid UTF-8: %q", got)
	}
}

// TestSplitDeepNestingDegradesToComplex covers pathologically nested bash
// (e.g. thousands of nested subshells) that would otherwise mirror its depth
// into fromStmt's recursion. Past the depth ceiling it must degrade to
// Segment{Cmd:"complex"} instead of recursing further.
func TestSplitDeepNestingDegradesToComplex(t *testing.T) {
	n := 200
	cmd := strings.Repeat("( ", n) + "cat f.json" + strings.Repeat(" )", n)
	segs, fb := Split(cmd)
	if fb {
		t.Fatal("unexpected fallback (AST parse failure)")
	}
	if len(segs) != 1 {
		t.Fatalf("want 1 segment, got %d: %v", len(segs), cmds(segs))
	}
	if segs[0].Cmd != "complex" {
		t.Errorf("Split(%d-deep nesting)[0].Cmd = %q, want %q", n, segs[0].Cmd, "complex")
	}
}

// TestSwallows covers the SWALLOWED-ERROR predicate (ferret-cax): true only
// when stderr-silencing and an `||` alternative appear together in the same
// left arm. Either half alone leaves a trace — `2>/dev/null` still surfaces
// the return code, `||` alone still prints the error — so only the pair hides
// a failure from ferret's is_error signal.
func TestSwallows(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// Positives — every silencing form paired with an alternative.
		{"stderr to devnull", "bd show x 2>/dev/null || bd list", true},
		{"stderr appended to devnull", "bd show x 2>>/dev/null || bd list", true},
		{"stdout then stderr dup", "command -v foo >/dev/null 2>&1 || echo missing", true},
		{"all-output shorthand", "rg pat &>/dev/null || rg -F pat", true},
		{"all-output append shorthand", "rg pat &>>/dev/null || rg -F pat", true},
		{"fallback is trivial", "jq '.[0]' out.json 2>/dev/null || true", true},
		{"nested in subshell", "make lint && (go test ./... 2>/dev/null || go test ./x)", true},
		{"redirect on a block arm", "{ go build ./...; } 2>/dev/null || go vet ./...", true},
		{"redirect inside a block arm", "{ go build ./... 2>/dev/null; } || go vet ./...", true},
		{"swallow in a later statement", "git status; git show 2>/dev/null || git log", true},

		// Negatives — one half only, or the wrong half.
		{"silence without alternative", "bd show x 2>/dev/null", false},
		{"alternative without silence", "bd show x || bd list", false},
		{"plain command", "go test ./...", false},
		{"and-chain, not or", "bd show x 2>/dev/null && bd list", false},
		{"silence on the right arm", "bd show x || bd list 2>/dev/null", false},
		{"stdout only", "bd show x >/dev/null || bd list", false},
		// `2>&1` before `>/dev/null` points stderr at the *old* stdout, so the
		// error still prints — order decides the verdict.
		{"reversed dup order", "bd show x 2>&1 >/dev/null || bd list", false},
		// A later `2>&1` re-binds an already-nulled fd 2 back to the visible
		// fd 1 — the whole list has to run before the verdict is known.
		{"dup after devnull restores stderr", "bd show x 2>/dev/null 2>&1 || bd list", false},
		{"redirect to a real file", "bd show x 2>errs.log || bd list", false},
		{"pipe, not or", "bd show x 2>/dev/null | jq .", false},
		{"empty", "", false},
		// A command that will not parse yields no verdict — the count this
		// predicate feeds must stay a floor, never a guess from raw text.
		{"parse failure", "bd show 'unterminated 2>/dev/null || bd list", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Swallows(c.in); got != c.want {
				t.Errorf("Swallows(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestSwallowsDeepNestingTerminates pairs with
// TestSplitDeepNestingDegradesToComplex: the swallow walk shares fromStmt's
// depth ceiling, so pathological nesting returns rather than recursing away.
func TestSwallowsDeepNestingTerminates(t *testing.T) {
	n := 200
	cmd := strings.Repeat("( ", n) + "cat f.json 2>/dev/null || true" + strings.Repeat(" )", n)
	if got := Swallows(cmd); got {
		t.Errorf("Swallows(%d-deep nesting) = true, want false at the depth ceiling", n)
	}
}

// TestSplitMarksSwallowedSegment checks the per-segment flag ingest carries
// forward: only the left arm — the command whose failure gets hidden — is
// marked, never the fallback that runs in its place.
func TestSplitMarksSwallowedSegment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []bool // parallel to the returned segments
	}{
		{"left arm only", "bd show x 2>/dev/null || bd list", []bool{true, false}},
		{"trivial fallback drops out", "jq '.[0]' 2>/dev/null || true", []bool{true}},
		{"nested or inside subshell", "make lint && (go test ./... 2>/dev/null || go vet ./...)", []bool{false, true, false}},
		{"whole left pipeline marked", "cat f.json 2>/dev/null || echo none", []bool{true}},
		{"and-chain marks nothing", "bd show x 2>/dev/null && bd list", []bool{false, false}},
		{"plain compound marks nothing", "go vet ./... && go test ./...", []bool{false, false}},
		// The `||` left arm is itself an `&&` chain: only the branch carrying
		// the redirect loses its errors, so `bd list` must stay unmarked.
		{"chained arm marks only the silenced branch", "bd show x 2>/dev/null && bd list || go vet ./...", []bool{true, false, false}},
		{"chained arm, silence on the second branch", "bd show x && bd list 2>/dev/null || go vet ./...", []bool{false, true, false}},
		// A redirect riding the arm's own statement still covers everything
		// under it — narrowing applies to chains, not to grouped commands.
		{"block-level redirect marks the whole arm", "{ bd show x; bd list; } 2>/dev/null || go vet ./...", []bool{true, true, false}},
		// Inside a group the same narrowing applies: a redirect on one member
		// leaves its visible siblings unmarked.
		{"grouped arm marks only the silenced member", "{ bd show x; bd list 2>/dev/null; } || go vet ./...", []bool{false, true, false}},
		{"subshell arm marks only the silenced member", "( bd show x 2>/dev/null; bd list ) || go vet ./...", []bool{true, false, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, fb := Split(c.in)
			if fb {
				t.Fatalf("unexpected fallback for %q", c.in)
			}
			if len(segs) != len(c.want) {
				t.Fatalf("Split(%q) = %v, want %d segments", c.in, cmds(segs), len(c.want))
			}
			for i, want := range c.want {
				if segs[i].Swallowed != want {
					t.Errorf("Split(%q)[%d] (%s).Swallowed = %v, want %v",
						c.in, i, segs[i].Cmd, segs[i].Swallowed, want)
				}
			}
		})
	}
}

func TestSplitCompoundFlag(t *testing.T) {
	segs, fb := Split("go vet ./... && go test ./...")
	if fb {
		t.Fatal("unexpected fallback")
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
}

// TestSplitMarksPipedSegment covers the PIPE-ERASURE tell (ferret-cax item 3
// escape hatch): a pipeline collapses to its first non-trivial command
// (fromBinaryCmd's Pipe/PipeAll arm), but that collapse throws away the fact
// a pipe was ever there — a substitution detector reading only Event.Action
// cannot tell `rg foo | head` from a bare `rg foo`. Piped captures the shape
// at Split time, before it is lost.
func TestSplitMarksPipedSegment(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []bool // parallel to the returned segments
	}{
		{"simple pipe", "rg foo | head", []bool{true}},
		{"pipe-all shorthand", "rg foo |& head", []bool{true}},
		{"pipe inside and-chain", "a && b | c", []bool{false, true}},
		{"trivial left falls through, still marked", "echo x | rg foo", []bool{true}},
		{"plain command unmarked", "rg foo", []bool{false}},
		{"and-chain, no pipe, unmarked", "go vet ./... && go test ./...", []bool{false, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, fb := Split(c.in)
			if fb {
				t.Fatalf("unexpected fallback for %q", c.in)
			}
			if len(segs) != len(c.want) {
				t.Fatalf("Split(%q) = %v, want %d segments", c.in, cmds(segs), len(c.want))
			}
			for i, want := range c.want {
				if segs[i].Piped != want {
					t.Errorf("Split(%q)[%d] (%s).Piped = %v, want %v",
						c.in, i, segs[i].Cmd, segs[i].Piped, want)
				}
			}
		})
	}
}

// TestArgv covers the shellnorm.Argv helper (ferret-cax item 3): the literal
// argv of a plain, single, redirect-free command, or plain=false whenever the
// text carries a shape a substitution detector cannot safely reason about —
// a redirect, an expansion, or anything that fails to parse.
func TestArgv(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantArgv  []string
		wantPlain bool
	}{
		{"literal argv", "rg -i foo src/", []string{"rg", "-i", "foo", "src/"}, true},
		{"single-quoted arg is literal", "sed -n '10,20p' f", []string{"sed", "-n", "10,20p", "f"}, true},
		{"redirect", "rg foo > out.txt", nil, false},
		{"append redirect", "rg foo >> out.txt", nil, false},
		{"param expansion", "cat $F", nil, false},
		{"command substitution", "cat $(ls)", nil, false},
		{"unparseable", "cat 'unterminated", nil, false},
		{"env assignment prefix", "RIPGREP_CONFIG_PATH=custom rg foo", nil, false},
		{"bare assignment", "FOO=1", nil, false},
		// A bare glob is a Lit like any other, but the shell may hand the
		// program a dozen filenames — the written argv is not the real one.
		{"unquoted star glob", "cat *.go", nil, false},
		{"unquoted bracket glob", "cat f[0-9].txt", nil, false},
		{"unquoted question glob", "cat f?.txt", nil, false},
		{"quoted glob is a literal name", "cat '*.go'", []string{"cat", "*.go"}, true},
		// mvdan keeps the backslashes in Lit.Value, so escapes have to be
		// resolved before the argv is trusted: an escaped metacharacter is a
		// filename, and an escaped space is one argument, not two.
		{"escaped glob is a literal name", `cat \*.go`, []string{"cat", "*.go"}, true},
		{"escaped space stays one argument", `cat a\ b.txt`, []string{"cat", "a b.txt"}, true},
		{"pipe is not a single call", "rg foo | head", nil, false},
		{"and-chain is not a single call", "rg foo && head", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			argv, plain := Argv(c.in)
			if plain != c.wantPlain {
				t.Fatalf("Argv(%q) plain = %v, want %v (argv=%v)", c.in, plain, c.wantPlain, argv)
			}
			if !plain {
				return
			}
			if len(argv) != len(c.wantArgv) {
				t.Fatalf("Argv(%q) = %v, want %v", c.in, argv, c.wantArgv)
			}
			for i := range argv {
				if argv[i] != c.wantArgv[i] {
					t.Errorf("Argv(%q)[%d] = %q, want %q", c.in, i, argv[i], c.wantArgv[i])
				}
			}
		})
	}
}

// TestFlags pins the option-name scan the cmd lens rides on (ferret-jtv):
// names survive in argv order, values never do, and — unlike Argv — a value
// the parser cannot read as a plain literal is skipped rather than failing
// the whole command, because a $VAR or a glob is never a flag name.
func TestFlags(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"no flags", "bd list", nil},
		{"long flags with values", "bd list --status in_progress --json", []string{"--status", "--json"}},
		{"equals form keeps only the name", "git log --max-count=10", []string{"--max-count"}},
		{"short flag drops its argument", "git log --oneline -n 10", []string{"--oneline", "-n"}},
		{"bundled shorts stay one token", "ls -la", []string{"-la"}},
		{"bare dash is stdin", "jq -r .id -", []string{"-r"}},
		{"double dash terminates", "go test ./... -- --not-a-flag", nil},
		{"param expansion skipped, not fatal", "rg -n $PATTERN internal/", []string{"-n"}},
		{"glob skipped, not fatal", "rg --files *.go", []string{"--files"}},
		{"redirect does not hide flags", "go test --json ./... > out.txt", []string{"--json"}},
		{"env assignment does not hide flags", "GOFLAGS=-mod=mod go test --json ./...", []string{"--json"}},
		{"quoted value dropped like any other", "rg --glob '!vendor' -n", []string{"--glob", "-n"}},
		{"parse failure yields nothing", "rg --json 'unclosed", nil},
		{"pipeline is not a single call", "git log --oneline | head -5", nil},
		{"empty", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Flags(c.raw)
			if !slices.Equal(got, c.want) {
				t.Errorf("Flags(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

// TestSplitSegmentFlags pins the flags Split attaches per segment (ferret-dep).
//
// The compound shapes are the point. `Flags` alone declines anything that is
// not one bare CallExpr — the pipeline case in TestFlags above returns nil —
// but Split has already reduced each of these to a single statement by the
// time a flag scan runs, so every arm keeps its own options. That is why the
// stored field, not a re-parse of the joined command line, is what the cmd
// lens reads.
func TestSplitSegmentFlags(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want [][]string // one entry per segment, in order
	}{
		{"plain call", "bd list --status open --json", [][]string{{"--status", "--json"}}},
		{"no options", "chezmoi status", [][]string{nil}},
		{
			"pipeline keeps the left arm's options",
			"bd list --status open --json | jq -r '.[]'",
			[][]string{{"--status", "--json"}},
		},
		{
			"and-chain keeps both arms' options",
			"git add -A && git commit -m x --no-verify",
			[][]string{{"-A"}, {"-m", "--no-verify"}},
		},
		{
			"subshell keeps the inner options",
			"(cd /x; go build -v ./...)",
			[][]string{{"-v"}},
		},
		{
			"trivial leading command is dropped, its partner keeps options",
			"cd /x && go test ./... -run TestY",
			[][]string{{"-run"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, fallback := Split(c.raw)
			if fallback {
				t.Fatalf("Split(%q) fell back", c.raw)
			}
			if len(segs) != len(c.want) {
				t.Fatalf("Split(%q) gave %d segments, want %d", c.raw, len(segs), len(c.want))
			}
			for i, seg := range segs {
				if !slices.Equal(seg.Flags, c.want[i]) {
					t.Errorf("segment %d (%s) flags = %v, want %v", i, seg.Cmd, seg.Flags, c.want[i])
				}
			}
		})
	}
}

// TestSplitFlagsSurviveDetailTruncation is the defect ferret-dep fixes, stated
// as a test: the segment carries options that a 160-byte Detail no longer
// contains, so a downstream re-parse of Detail cannot recover them.
//
// The shape is the one that dominates the corpus — a long quoted positional
// pushing the option list past the cut. Measured 2026-08-30: 13,747 shell
// events, 9.6% of those carrying a Detail.
func TestSplitFlagsSurviveDetailTruncation(t *testing.T) {
	const detailMax = 160 // event.DetailMax; duplicated to keep shellnorm free of that import
	raw := `bd create "` + strings.Repeat("x", 200) + `" --type task --priority 1`

	segs, fallback := Split(raw)
	if fallback || len(segs) != 1 {
		t.Fatalf("Split gave fallback=%v, %d segments; want a single parsed segment", fallback, len(segs))
	}
	want := []string{"--type", "--priority"}
	if !slices.Equal(segs[0].Flags, want) {
		t.Fatalf("segment flags = %v, want %v", segs[0].Flags, want)
	}

	truncated := segs[0].Raw
	if len(truncated) > detailMax {
		truncated = truncated[:detailMax]
	}
	if got := Flags(truncated); len(got) != 0 {
		t.Fatalf("re-parsing the truncated Detail recovered %v — the premise of storing "+
			"Segment.Flags is that it cannot; adjust the test, not the field", got)
	}
}
