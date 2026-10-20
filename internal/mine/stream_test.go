package mine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/lens"
)

// TestBuildMarksSidechainStreams pins the ferret-9j3 pool label at the source:
// Build must flag each stream's sidechain-ness so measure() can split burn by
// origin. A subagent transcript is a separate stream (distinct agent key) and
// carries sc=true on its events.
func TestBuildMarksSidechainStreams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	lines := `{"i":1,"p":"p","s":"s1","k":"tool","act":"Read","b":40}
{"i":2,"p":"p","s":"s1","a":"ag1","sc":true,"k":"tool","act":"Read","b":400}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := lens.Get("tool")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Build(path, l, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Streams) != 2 || len(c.Sidechain) != 2 {
		t.Fatalf("streams, sidechain flags = %d, %d, want 2, 2", len(c.Streams), len(c.Sidechain))
	}
	want := map[string]bool{"p/s1@": false, "p/s1@ag1": true}
	for si, key := range c.StreamKeys {
		if c.Sidechain[si] != want[key] {
			t.Errorf("stream %q sidechain = %v, want %v", key, c.Sidechain[si], want[key])
		}
	}
}

// windowFixture writes four streams that start on different days:
//
//	a: starts 10-18 (and runs on into 10-20 — judged by its START)
//	b: first event untimestamped, earliest timestamped event 10-19
//	c: starts 10-20
//	d: no timestamp at all
//
// Each stream has its own action so a dropped stream's token is detectable.
func windowFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lines := `{"i":1,"t":"2026-10-18T10:00:00Z","p":"p","s":"a","k":"tool","act":"Aonly","b":10}
{"i":2,"t":"2026-10-20T10:00:00Z","p":"p","s":"a","k":"tool","act":"Shared","b":10}
{"i":3,"p":"p","s":"b","k":"tool","act":"Bonly","b":10}
{"i":4,"t":"2026-10-19T08:00:00Z","p":"p","s":"b","k":"tool","act":"Shared","b":10}
{"i":5,"t":"2026-10-20T23:30:00Z","p":"p","s":"c","k":"tool","act":"Conly","b":10}
{"i":6,"p":"p","s":"d","k":"tool","act":"Donly","b":10}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func buildWindow(t *testing.T, opts Options) *Corpus {
	t.Helper()
	l, err := lens.Get("tool")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Build(windowFixture(t), l, opts)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func streamSessions(c *Corpus) []string {
	got := make([]string, 0, len(c.StreamKeys))
	for _, k := range c.StreamKeys {
		got = append(got, strings.TrimSuffix(strings.TrimPrefix(k, "p/"), "@"))
	}
	return got
}

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }

// endOfDay mirrors what the CLI hands in for an inclusive --until.
func endOfDay(d int) time.Time { return day(d).Add(24*time.Hour - time.Nanosecond) }

func TestBuildWindowKeepsStreamsByStart(t *testing.T) {
	tests := []struct {
		name     string
		opts     Options
		want     []string
		wantNoTS int
	}{
		{"no window keeps everything, untimestamped included", Options{}, []string{"a", "b", "c", "d"}, 0},
		{"since only", Options{Since: day(19)}, []string{"b", "c"}, 1},
		{"until only is inclusive of the whole day", Options{Until: endOfDay(19)}, []string{"a", "b"}, 1},
		{"since and until together", Options{Since: day(19), Until: endOfDay(19)}, []string{"b"}, 1},
		{"since after every start", Options{Since: day(21)}, nil, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := buildWindow(t, tt.opts)
			if got := streamSessions(c); !slices.Equal(got, tt.want) {
				t.Fatalf("kept streams = %v, want %v", got, tt.want)
			}
			if len(c.Streams) != len(c.StreamKeys) || len(c.Streams) != len(c.Sidechain) {
				t.Fatalf("parallel slices diverged: %d streams, %d keys, %d sidechain flags",
					len(c.Streams), len(c.StreamKeys), len(c.Sidechain))
			}
			if tt.opts.Since.IsZero() && tt.opts.Until.IsZero() {
				if c.Window != nil {
					t.Fatalf("Window = %+v, want nil when no window is set", c.Window)
				}
				return
			}
			w := c.Window
			if w == nil {
				t.Fatal("Window = nil, want the header counts when a window is set")
			}
			if w.Total != 4 || w.Kept != len(tt.want) || w.NoTime != tt.wantNoTS {
				t.Errorf("Window counts total/kept/notime = %d/%d/%d, want 4/%d/%d",
					w.Total, w.Kept, w.NoTime, len(tt.want), tt.wantNoTS)
			}
			if !w.Since.Equal(tt.opts.Since) || !w.Until.Equal(tt.opts.Until) {
				t.Errorf("Window bounds = %v..%v, want %v..%v", w.Since, w.Until, tt.opts.Since, tt.opts.Until)
			}
		})
	}
}

// A token that occurs only in dropped streams must not linger in the vocab:
// vocab and every token-count view read it.
func TestBuildWindowPrunesVocabOfDroppedStreams(t *testing.T) {
	c := buildWindow(t, Options{Since: day(19), Until: endOfDay(19)})
	if !slices.Equal(c.Vocab, []string{"Bonly", "Shared"}) {
		t.Fatalf("Vocab = %v, want [Bonly Shared]", c.Vocab)
	}
	got := c.Tokens([]uint32{c.Streams[0][0].ID, c.Streams[0][1].ID})
	if !slices.Equal(got, []string{"Bonly", "Shared"}) {
		t.Fatalf("remapped stream tokens = %v, want [Bonly Shared]", got)
	}
}

// Filtering runs before collapse, so a collapsed token only appears when kept
// streams carry the run.
func TestBuildWindowBeforeCollapse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lines := `{"i":1,"t":"2026-10-18T10:00:00Z","p":"p","s":"old","k":"tool","act":"Read","b":10}
{"i":2,"t":"2026-10-18T10:01:00Z","p":"p","s":"old","k":"tool","act":"Read","b":10}
{"i":3,"t":"2026-10-20T10:00:00Z","p":"p","s":"new","k":"tool","act":"Edit","b":10}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := lens.Get("tool")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Build(path, l, Options{Collapse: true, Since: day(19)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Vocab, []string{"Edit"}) || len(c.Streams) != 1 {
		t.Fatalf("Vocab=%v streams=%d, want [Edit] and 1", c.Vocab, len(c.Streams))
	}
}
