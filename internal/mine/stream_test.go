package mine

import (
	"os"
	"path/filepath"
	"slices"
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

// TestBuildWindowKeepsStreamsByStart pins ferret-pr4: a window keeps a stream
// by its earliest timestamped event, judges a subagent stream by its own first
// event, and drops a stream with no timestamp only when a window is set.
func TestBuildWindowKeepsStreamsByStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// s1 starts 10-17, s2 starts 10-19 (its subagent ag1 starts 10-21), s3
	// starts 10-21, s4 has no timestamp. A later event never moves a start.
	lines := `{"i":1,"p":"p","s":"s1","t":"2026-10-17T09:00:00Z","k":"tool","act":"Read"}
{"i":2,"p":"p","s":"s1","t":"2026-10-20T09:00:00Z","k":"tool","act":"Edit"}
{"i":1,"p":"p","s":"s2","t":"2026-10-19T23:59:00Z","k":"tool","act":"Read"}
{"i":2,"p":"p","s":"s2","a":"ag1","sc":true,"t":"2026-10-21T08:00:00Z","k":"tool","act":"Grep"}
{"i":1,"p":"p","s":"s3","t":"2026-10-21T00:00:00Z","k":"tool","act":"Read"}
{"i":1,"p":"p","s":"s4","k":"tool","act":"Read"}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := lens.Get("tool")
	if err != nil {
		t.Fatal(err)
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	endOf := func(d int) time.Time { return day(d + 1).Add(-time.Nanosecond) }
	cases := []struct {
		name    string
		w       Window
		want    []string
		untimed int
	}{
		{"none", Window{}, []string{"p/s1@", "p/s2@", "p/s2@ag1", "p/s3@", "p/s4@"}, 0},
		{"since", Window{Since: day(19)}, []string{"p/s2@", "p/s2@ag1", "p/s3@"}, 1},
		{"until", Window{Until: endOf(19)}, []string{"p/s1@", "p/s2@"}, 1},
		{"both", Window{Since: day(19), Until: endOf(19)}, []string{"p/s2@"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Build(path, l, Options{Window: tc.w})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(c.StreamKeys, tc.want) {
				t.Fatalf("streams = %v, want %v", c.StreamKeys, tc.want)
			}
			if len(c.Streams) != len(tc.want) || len(c.Sidechain) != len(tc.want) {
				t.Fatalf("streams/sidechain lengths = %d/%d, want %d", len(c.Streams), len(c.Sidechain), len(tc.want))
			}
			if c.Window.Untimed != tc.untimed {
				t.Errorf("untimed = %d, want %d", c.Window.Untimed, tc.untimed)
			}
			if tc.w.Set() && c.Window.Total != 5 {
				t.Errorf("total = %d, want 5", c.Window.Total)
			}
			// Vocab holds exactly the tokens the kept streams hold.
			held := map[string]bool{}
			for _, st := range c.Streams {
				for _, tok := range st {
					held[c.Vocab[tok.ID]] = true
				}
			}
			if len(held) != len(c.Vocab) {
				t.Errorf("vocab = %v, want only the held tokens %v", c.Vocab, held)
			}
		})
	}
}
