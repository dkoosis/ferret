package mine

import (
	"os"
	"path/filepath"
	"testing"

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
