package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// ferretLimitHelperEnv makes the test binary run main() instead of the tests,
// so a test can watch the real process exit code and stderr.
const ferretLimitHelperEnv = "FERRET_LIMIT_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(ferretLimitHelperEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runFerret runs the real CLI in a child process and returns stdout, stderr
// and the exit code.
func runFerret(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], args...)
	cmd.Env = append(os.Environ(), ferretLimitHelperEnv+"=1")
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run ferret: %v", err)
	}
	return so.String(), se.String(), code
}

// parseNgramsArgs parses args into CLI.Ngrams through kong, the way main does,
// and restores CLI.Ngrams when the test ends.
func parseNgramsArgs(t *testing.T, args ...string) error {
	t.Helper()
	saved := CLI.Ngrams
	t.Cleanup(func() { CLI.Ngrams = saved })
	parser, err := kong.New(&CLI, kong.Name("ferret"), kong.Exit(func(int) { t.Fatal("kong exited") }))
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	_, err = parser.Parse(append([]string{"ngrams"}, args...))
	return err
}

// ferret-38r: --limit all reads as no cap, the same rows as --limit=-1.
func TestNgramsLimit_AllMatchesMinusOne_When_FlagParsed(t *testing.T) {
	data := writeContainsCorpus(t)
	run := func(limitArgs ...string) string {
		t.Helper()
		args := append([]string{"--data", data, "--lens", "cmd", "--n", "2-5", "--min-count", "1", "--min-sessions", "2"}, limitArgs...)
		if err := parseNgramsArgs(t, args...); err != nil {
			t.Fatalf("parse %v: %v", limitArgs, err)
		}
		return captureStdout(t, cmdNgrams)
	}
	all := run("--limit", "all")
	minusOne := run("--limit=-1")
	if got := dataRows(all); len(got) < 3 {
		t.Fatalf("fixture must mine at least 3 rows so a cap of 2 cuts; got %d", len(got))
	}
	if all != minusOne {
		t.Errorf("--limit all:\n%s\nwant same as --limit=-1:\n%s", all, minusOne)
	}
	// A positive cap really does cut, so "all" matching -1 proves no cap.
	if capped := run("--limit", "2"); len(dataRows(capped)) != 2 {
		t.Errorf("--limit 2 printed %d data rows, want 2", len(dataRows(capped)))
	}
}

// --limit=-1, --limit 5 and an unset --limit keep their meaning.
func TestLimitFlag_KeepsExistingMeanings_When_NumbersAndUnset(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int // CommonFlags.Limit after parse
		def  int // c.limit after applyDefaultLimit(c, 30)
	}{
		{"unset", nil, 0, 30},
		{"zero", []string{"--limit", "0"}, 0, 30},
		{"five", []string{"--limit", "5"}, 5, 5},
		{"equals minus one", []string{"--limit=-1"}, -1, 0},
		{"all", []string{"--limit", "all"}, -1, 0},
		{"ALL", []string{"--limit=ALL"}, -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := parseNgramsArgs(t, tc.args...); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := CLI.Ngrams.Limit; int(got) != tc.want {
				t.Errorf("Limit = %d, want %d", got, tc.want)
			}
			c, err := fromCommonFlags(CLI.Ngrams.CommonFlags)
			if err != nil {
				t.Fatal(err)
			}
			applyDefaultLimit(c, 30)
			if c.limit != tc.def {
				t.Errorf("resolved limit = %d, want %d", c.limit, tc.def)
			}
		})
	}
}

// --limit bogus is a usage error naming both accepted forms, exit code 2.
func TestLimitFlag_ExitsUsage_When_ValueIsNeitherAllNorInteger(t *testing.T) {
	for _, bad := range []string{"bogus", "5.5", ""} {
		_, stderr, code := runFerret(t, "ngrams", "--limit="+bad)
		if code != exitUsage {
			t.Errorf("--limit=%q exit = %d, want %d", bad, code, exitUsage)
		}
		for _, want := range []string{"all", "integer"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("--limit=%q stderr lacks %q:\n%s", bad, want, stderr)
			}
		}
	}
}

func TestNgramsHelp_NamesLimitAll_When_HelpRequested(t *testing.T) {
	stdout, _, code := runFerret(t, "ngrams", "--help")
	if code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(collapseSpace(stdout), "--limit all") {
		t.Errorf("ngrams --help does not name --limit all:\n%s", stdout)
	}
}

func TestParseLimit_ErrorIsUsage_When_ValueIsBogus(t *testing.T) {
	_, err := parseLimit("bogus")
	if got := exitCodeFor(err); got != exitUsage {
		t.Errorf("exitCodeFor(parseLimit(bogus)) = %d, want %d", got, exitUsage)
	}
}
