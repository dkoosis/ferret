package sweagent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dkoosis/ferret/internal/event"
)

func decode(t *testing.T, line string) *Row {
	t.Helper()
	r, err := DecodeRow([]byte(line))
	if err != nil {
		t.Fatalf("DecodeRow: %v", err)
	}
	return r
}

func TestActionParsing(t *testing.T) {
	cases := []struct {
		action     string
		wantKind   string
		wantAction string
		wantTarget string
	}{
		{"open astropy/io/core.py 120", event.KindTool, "open", "astropy/io/core.py"},
		{"edit 130:131", event.KindTool, "edit", "130:131"},
		{"find_file config.py", event.KindTool, "find_file", "config.py"},
		{"submit", event.KindTool, "submit", ""},
		{"python -m pytest tests/", event.KindShell, "python", "pytest"},
		{"git diff", event.KindShell, "git_diff", "diff"},
	}
	for _, c := range cases {
		ev := eventFromAction("inst", 0, c.action, "")
		if ev.Kind != c.wantKind || ev.Action != c.wantAction {
			t.Errorf("%q → kind=%q act=%q; want kind=%q act=%q",
				c.action, ev.Kind, ev.Action, c.wantKind, c.wantAction)
		}
		if ev.Target != c.wantTarget {
			t.Errorf("%q → target=%q; want %q", c.action, ev.Target, c.wantTarget)
		}
	}
}

func TestStatusHeuristic(t *testing.T) {
	cases := []struct {
		obs  string
		want string
	}{
		{"==== 12 passed ====", event.StatusOK},
		{"", event.StatusOK},
		{"Traceback (most recent call last):\n  ImportError", event.StatusFail},
		{"bash: foo: command not found", event.StatusFail},
		{"ls: cannot access '/x': No such file or directory", event.StatusFail},
	}
	for _, c := range cases {
		if got := statusFor(c.obs); got != c.want {
			t.Errorf("statusFor(%q) = %q, want %q", c.obs, got, c.want)
		}
	}
}

func TestEventsMapsTrajectory(t *testing.T) {
	row := decode(t, `{"instance_id":"x__y-1","target":false,"exit_status":"submitted",
		"trajectory":[
			{"role":"ai","action":"python test.py"},
			{"role":"user","content":"Traceback (most recent call last):"},
			{"role":"ai","action":"submit"},
			{"role":"user","content":"done"}]}`)
	if row.InstanceID != "x__y-1" || row.Target {
		t.Fatalf("row = %+v", row)
	}
	evs := Events(row)
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	if evs[0].Seq != 0 || evs[1].Seq != 1 {
		t.Errorf("seqs = %d,%d; want 0,1", evs[0].Seq, evs[1].Seq)
	}
	if evs[0].Status != event.StatusFail {
		t.Errorf("first status = %q, want fail (observation has Traceback)", evs[0].Status)
	}
	if evs[0].Project != Project {
		t.Errorf("project = %q, want %q", evs[0].Project, Project)
	}
}

// TestTolerantFieldNames covers the alternate spellings the defensive decoder
// accepts (resolved→target, messages→trajectory, content-inlined action).
func TestTolerantFieldNames(t *testing.T) {
	row := decode(t, `{"id":"alt-1","resolved":true,
		"messages":[{"role":"assistant","content":"open foo.py"},{"role":"tool","content":"ok"}]}`)
	if row.InstanceID != "alt-1" {
		t.Errorf("instance = %q, want alt-1 (id alias)", row.InstanceID)
	}
	if !row.Target {
		t.Errorf("target = false, want true (resolved alias)")
	}
	evs := Events(row)
	if len(evs) != 1 || evs[0].Action != "open" {
		t.Fatalf("events = %+v, want one open (content-inlined action)", evs)
	}
}

// TestTrajectoryAsJSONString covers the struct-column export that double-
// encodes the trajectory array as a quoted string.
func TestTrajectoryAsJSONString(t *testing.T) {
	row := decode(t, `{"instance_id":"s-1","target":true,
		"trajectory":"[{\"role\":\"ai\",\"action\":\"submit\"}]"}`)
	evs := Events(row)
	if len(evs) != 1 || evs[0].Action != "submit" {
		t.Fatalf("events = %+v, want one submit", evs)
	}
}

func TestMalformedRowErrors(t *testing.T) {
	if _, err := DecodeRow([]byte(`{"instance_id": not json`)); err == nil {
		t.Error("expected decode error on malformed row")
	}
}

// TestTruncRuneBoundary verifies trunc cuts at detailMax (160) without
// splitting a multibyte rune. CJK runes are 3 bytes; 160 is not a multiple
// of 3, so a naive byte slice would land mid-rune.
func TestTruncRuneBoundary(t *testing.T) {
	s := strings.Repeat("世", 100) // 300 bytes, exceeds detailMax=160
	got := trunc(s)
	if !utf8.ValidString(got) {
		t.Fatalf("trunc(%q) = %q: not valid UTF-8", s, got)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatalf("trunc(%q) = %q: contains U+FFFD", s, got)
	}
	if len(got) > detailMax {
		t.Fatalf("trunc length %d exceeds detailMax %d", len(got), detailMax)
	}
}
