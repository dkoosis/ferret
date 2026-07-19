package friction

import "testing"

func TestFingerprint(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain command unchanged", "go test", "go test"},
		{"case and space normalized", "  GO   Test  ", "go test"},
		{
			"absolute path masked",
			"go test /Users/vcto/Projects/ferret/internal/mine",
			"go test <path>",
		},
		{
			"relative path masked",
			"cat ./internal/score/retrieval.go",
			"cat <path>",
		},
		{
			"home path masked",
			"ls ~/Projects/dk/Project/ferret",
			"ls <path>",
		},
		{
			"git sha masked",
			"git checkout f0bd8f0abc123",
			"git checkout <hash>",
		},
		{
			"two shas fingerprint identically to one shape",
			"git diff 04d9986 cca7bf4",
			"git diff <hash> <hash>",
		},
		{
			"line number masked, bare filename kept",
			"gates.go:242 nilaway complaint",
			"gates.go:<n> nilaway complaint",
		},
		{
			"pathed filename and line both masked",
			"internal/score/gates.go:242 nilaway",
			"<path>:<n> nilaway",
		},
		{
			"timestamp masked",
			"build 2026-07-18T12:30:45Z failed",
			"build <ts> failed",
		},
		{
			"clock time masked",
			"job at 12:30:45 aborted",
			"job at <ts> aborted",
		},
		{
			"uuid masked",
			"session 3b1780d4-e150-4e19-81ff-53e59137c817 error",
			"session <uuid> error",
		},
		{
			"quoted literal masked whole",
			`rg -n "some/volatile/path.go" src`,
			"rg -n <str> src",
		},
		{
			"single-quoted literal masked",
			"make lint FILE='cmd/ferret/main.go'",
			"make lint file=<str>",
		},
		{
			"bare number masked",
			"exit code 137 killed",
			"exit code <n> killed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Fingerprint(c.in); got != c.want {
				t.Errorf("Fingerprint(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestFingerprintStableAcrossSessions is the core contract: the same friction
// with different volatile parts (paths, SHAs, line numbers) must collapse to one
// fingerprint. If these diverge, a known signature never re-matches and the
// detector is useless.
func TestFingerprintStableAcrossSessions(t *testing.T) {
	a := Fingerprint("git_checkout git checkout -b fix/ferret-5jb /Users/a/repo @ f0bd8f0")
	b := Fingerprint("git_checkout git checkout -b fix/ferret-9xy /Users/b/other @ cca7bf4")
	if a != b {
		t.Errorf("same friction shape produced different fingerprints:\n a=%q\n b=%q", a, b)
	}
}

// TestFingerprintDistinguishesDifferentFriction guards the opposite failure:
// genuinely different friction must NOT collapse to one signature (which would
// flag false recurrences).
func TestFingerprintDistinguishesDifferentFriction(t *testing.T) {
	a := Fingerprint("go_test go test ./internal/mine")
	b := Fingerprint("go_build go build ./cmd/ferret")
	if a == b {
		t.Errorf("distinct friction collapsed to one fingerprint: %q", a)
	}
}
