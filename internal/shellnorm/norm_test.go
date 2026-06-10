package shellnorm

import "testing"

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

func TestSplitCompoundFlag(t *testing.T) {
	segs, fb := Split("go vet ./... && go test ./...")
	if fb {
		t.Fatal("unexpected fallback")
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %d", len(segs))
	}
}
