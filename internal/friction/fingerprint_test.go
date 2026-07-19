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
		{
			"unix absolute path masks",
			"/Users/x/foo.go",
			"<path>",
		},
		{
			"windows drive path masks fully (no c: residue)",
			"C:/Users/x/foo.go",
			"<path>",
		},
		{
			"windows backslash path masks",
			`C:\Users\x\foo.go`,
			"<path>",
		},
		{
			"bare single-slash identifier pair is NOT a path",
			"openai/ferret",
			"openai/ferret",
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
	a := Fingerprint("go_test go test /Users/a/ferret/internal/mine @ f0bd8f0")
	b := Fingerprint("go_test go test /Users/b/other/internal/mine @ cca7bf4")
	if a != b {
		t.Errorf("same friction shape produced different fingerprints:\n a=%q\n b=%q", a, b)
	}
}

// TestFingerprintPathBoundaries guards Bug 2: a bare single-slash identifier
// pair must NOT collapse to <path> (that would flag a false recurrence between
// "openai/ferret" and "openai/gym"), while genuine filesystem paths — unix
// absolute and Windows drive — must mask fully.
func TestFingerprintPathBoundaries(t *testing.T) {
	if Fingerprint("openai/ferret") == Fingerprint("openai/gym") {
		t.Error("distinct repo slugs collapsed to one fingerprint (over-masking)")
	}
	for _, p := range []string{"/Users/x/foo.go", "C:/Users/x/foo.go", `C:\Users\x\foo.go`} {
		if got := Fingerprint(p); got != "<path>" {
			t.Errorf("Fingerprint(%q) = %q, want %q (under-masking)", p, got, "<path>")
		}
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
