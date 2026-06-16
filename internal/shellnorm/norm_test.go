package shellnorm

import (
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

func TestSplitCompoundFlag(t *testing.T) {
	segs, fb := Split("go vet ./... && go test ./...")
	if fb {
		t.Fatal("unexpected fallback")
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
}
