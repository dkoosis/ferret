package friction

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSigFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, SigFileName)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadSignaturesMissingFileIsEmpty(t *testing.T) {
	got, err := LoadSignatures(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file must yield empty set, got %d", len(got))
	}
}

func TestLoadSignatures(t *testing.T) {
	p := writeSigFile(t,
		`{"fingerprint":"go_test go test <path>","label":"test loop"}`+"\n"+
			"\n"+ // blank line skipped
			`{"fingerprint":"git_checkout git checkout -b <path>","label":"branch-flip"}`+"\n")
	got, err := LoadSignatures(p)
	if err != nil {
		t.Fatalf("LoadSignatures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 signatures, got %d", len(got))
	}
	if got[0].Label != "test loop" || got[1].Fingerprint != "git_checkout git checkout -b <path>" {
		t.Errorf("parsed signatures wrong: %+v", got)
	}
}

func TestLoadSignaturesMalformedErrors(t *testing.T) {
	p := writeSigFile(t, `{"fingerprint":"ok"}`+"\n"+`{not json}`+"\n")
	if _, err := LoadSignatures(p); err == nil {
		t.Fatal("malformed signature line must error")
	}
}

// TestLoadThenDetect is the end-to-end contract: signatures loaded from a file
// seed a detector that flags a matching fresh event as a 2nd occurrence.
func TestLoadThenDetect(t *testing.T) {
	fp := Fingerprint("go_test go test ./internal/mine")
	p := writeSigFile(t, `{"fingerprint":"`+fp+`","label":"test loop"}`+"\n")
	sigs, err := LoadSignatures(p)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDetector(sigs)
	m, ok := d.Observe(failShell(7, "sess", "go_test", "go test ./internal/score"))
	if !ok {
		t.Fatal("loaded signature must flag a matching fresh event")
	}
	if m.Occurrence != 2 || m.Label != "test loop" {
		t.Errorf("match = %+v, want occurrence 2 label 'test loop'", m)
	}
}
