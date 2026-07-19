package lens

import (
	"slices"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

// TestConfidenceLens verifies the confidence lens surfaces snipe's fallback
// bit: an event tagged Approx tokenizes with a "~approx" suffix so the sequence
// miner reads "snipe~approx → rg" as friction-with-cause, while served calls
// and unrelated events tokenize exactly like the tool lens.
func TestConfidenceLens(t *testing.T) {
	l, err := Get("confidence")
	if err != nil {
		t.Fatalf("Get(confidence): %v", err)
	}
	tests := []struct {
		name string
		ev   *event.Event
		want string
		ok   bool
	}{
		{
			name: "snipe fallback tagged with marker",
			ev:   &event.Event{Kind: event.KindShell, Action: "snipe_callers", Approx: true},
			want: "sh:snipe_callers~approx",
			ok:   true,
		},
		{
			name: "snipe served has no marker",
			ev:   &event.Event{Kind: event.KindShell, Action: "snipe_callers"},
			want: "sh:snipe_callers",
			ok:   true,
		},
		{
			name: "non-snipe shell unchanged",
			ev:   &event.Event{Kind: event.KindShell, Action: "rg"},
			want: "sh:rg",
			ok:   true,
		},
		{
			name: "tool event unchanged",
			ev:   &event.Event{Kind: event.KindTool, Action: "Read"},
			want: "Read",
			ok:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := l.Token(tc.ev)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Token() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestConfidenceLensRegistered ensures the lens is discoverable by name.
func TestConfidenceLensRegistered(t *testing.T) {
	if !slices.Contains(Names(), "confidence") {
		t.Errorf("confidence not in Names() = %v", Names())
	}
}

// TestIsVCS pins the reusable VCS-family gate (extracted from the coarse lens,
// reused by the kuv.8 outcome label). It is the FAMILY classifier: true for
// read-only git/gh calls too, so a consumer wanting the terminal subset must
// narrow further. Non-VCS shell verbs and tool names are false.
func TestIsVCS(t *testing.T) {
	cases := []struct {
		action string
		want   bool
	}{
		{"git", true},
		{"git_commit", true},
		{"git_push", true},
		{"git_status", true}, // family member even though read-only
		{"gh", true},
		{"gh_pr", true},
		{"go_test", false},
		{"gitk", false}, // not "git" and no "git_" prefix
		{"github", false},
		{"rg", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsVCS(c.action); got != c.want {
			t.Errorf("IsVCS(%q) = %v, want %v", c.action, got, c.want)
		}
	}
}

// TestShellClass pins the exported string-facing shell classifier — the single
// source internal/reach projects its reach channels onto. It must agree with the
// coarse event lens's KindShell branch (same table + IsVCS gate) so the two never
// drift.
func TestShellClass(t *testing.T) {
	cases := []struct {
		cmd     string
		wantCls string
		wantOK  bool
	}{
		{"rg", ClassSearch, true},
		{"grep", ClassSearch, true},
		{"ag", ClassSearch, true}, // migrated in from reach's old grepCmds
		{"cat", ClassRead, true},
		{"wc", ClassRead, true},
		{"git_log", ClassVCS, true},    // family gate: read-only member
		{"git_commit", ClassVCS, true}, // family gate: mutating member too
		{"gh_pr", ClassVCS, true},
		{"make", "build", true},
		{"trixi_search", "", false}, // not in lens's general taxonomy
		{"", "", false},
	}
	for _, c := range cases {
		gotCls, gotOK := ShellClass(c.cmd)
		if gotCls != c.wantCls || gotOK != c.wantOK {
			t.Errorf("ShellClass(%q) = (%q,%v), want (%q,%v)", c.cmd, gotCls, gotOK, c.wantCls, c.wantOK)
		}
		// Agreement with the coarse event lens: when ShellClass classifies, the
		// coarse lens must emit the same token for the equivalent shell event.
		if c.wantOK {
			tok, ok := (coarse{}).Token(&event.Event{Kind: event.KindShell, Action: c.cmd})
			if !ok || tok != c.wantCls {
				t.Errorf("coarse lens drift for %q: token=(%q,%v), ShellClass=%q", c.cmd, tok, ok, c.wantCls)
			}
		}
	}
}
