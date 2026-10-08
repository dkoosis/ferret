package friction

import (
	"errors"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/event"
)

func TestSignatureFromNug_MatchesLiveFrictionText(t *testing.T) {
	n := Nug{ID: "abc123abc123", Body: "tool: git checkout -b fix/ferret-5jb. problem: branch-flip loop. resolution: stay on the branch."}
	sig, err := SignatureFromNug(n)
	if err != nil {
		t.Fatalf("SignatureFromNug: %v", err)
	}
	// What ingest stores for the same command failing live.
	live, ok := FrictionText(event.Event{
		Kind: event.KindShell, Action: "git_checkout", Detail: "git checkout -b fix/ferret-9zz", Status: event.StatusFail,
	})
	if !ok {
		t.Fatal("live event must be friction")
	}
	if got := Fingerprint(live); got != sig.Fingerprint {
		t.Errorf("seeded fingerprint %q != live fingerprint %q", sig.Fingerprint, got)
	}
	if want := "branch-flip loop (nug abc123abc123)"; sig.Label != want {
		t.Errorf("label = %q, want %q", sig.Label, want)
	}
}

func TestSignatureFromNug_NoProblemClauseLabelsByTool(t *testing.T) {
	sig, err := SignatureFromNug(Nug{ID: "x", Body: "tool: make check"})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Label != "make check (nug x)" {
		t.Errorf("label = %q", sig.Label)
	}
}

func TestSignatureFromNug_NoToolIsErrNoTool(t *testing.T) {
	_, err := SignatureFromNug(Nug{ID: "x", Body: "problem: something. resolution: nothing."})
	if !errors.Is(err, ErrNoTool) {
		t.Errorf("err = %v, want ErrNoTool", err)
	}
}

func TestSignatureFromNug_LongProblemIsTruncated(t *testing.T) {
	long := strings.Repeat("é", labelMax+10)
	sig, err := SignatureFromNug(Nug{ID: "x", Body: "tool: go build ./... problem: " + long})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sig.Label, strings.Repeat("é", labelMax)+"…") {
		t.Errorf("label not rune-truncated: %q", sig.Label)
	}
}

func TestClauses(t *testing.T) {
	tool, problem := clauses("tool: bd update --description $(pipeline). problem: awk failed. resolution: use a file.")
	if tool != "bd update --description $(pipeline)" {
		t.Errorf("tool = %q", tool)
	}
	if problem != "awk failed" {
		t.Errorf("problem = %q", problem)
	}
}
