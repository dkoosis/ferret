package shellnorm_test

import (
	"testing"

	"github.com/dkoosis/ferret/internal/shellnorm"
)

func FuzzSplit(f *testing.F) {
	for _, seed := range []string{
		"", "ls", "git checkout -b x",
		"cd /tmp && go test ./... | rg -n foo",
		"if true; then echo hi; fi",
		"$(", "'unterminated", "a\xffb",
		"( ( ( ( echo deep ) ) ) )",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		segs, fallback := shellnorm.Split(cmd)
		// Invariant 1 (free): Split never panics on arbitrary input.
		// Invariant 2: the crude fallback path yields at most one segment.
		if fallback && len(segs) > 1 {
			t.Fatalf("fallback=true with %d segments for %q", len(segs), cmd)
		}
	})
}
