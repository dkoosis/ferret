package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestComplete guards the ensureData completeness gate: a bare
// os.Stat is not enough. A 0-byte manifest (interrupted ingest) or a
// truncated/invalid-JSON manifest must NOT count as a complete corpus,
// or ensureData skips re-ingest and silently mines a partial events.jsonl.
func TestManifestComplete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "manifest.json")

	if manifestComplete(p) {
		t.Error("missing manifest must not be complete")
	}

	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if manifestComplete(p) {
		t.Error("0-byte manifest must not be complete (interrupted ingest)")
	}

	if err := os.WriteFile(p, []byte(`{"root":"/x","stats":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if manifestComplete(p) {
		t.Error("truncated/invalid-JSON manifest must not be complete")
	}

	if err := os.WriteFile(p, []byte(`{"root":"/x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !manifestComplete(p) {
		t.Error("non-empty, valid-JSON manifest must be complete")
	}
}

func TestMermaidLabelEscaping(t *testing.T) {
	for in, want := range map[string]string{
		`Grep:"foo"`:    "Grep:#quot;foo#quot;",
		"Read:a[0].go":  "Read:a#91;0#93;.go",
		"sh:awk {p}":    "sh:awk #123;p#125;",
		"sh:git_status": "sh:git_status",
	} {
		if got := mermaidLabel(in); got != want {
			t.Errorf("mermaidLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateFormat(t *testing.T) {
	c := &common{format: "josn"}
	if err := c.validate("text", "json"); err == nil {
		t.Error("unknown -format must fail loudly, not fall through to text")
	}
	c = &common{format: "json", maxBytes: 1024}
	err := c.validate("text", "json")
	if err == nil || !strings.Contains(err.Error(), "max-bytes") {
		t.Errorf("-max-bytes with json must be rejected, got %v", err)
	}
	c = &common{format: "json"}
	if err := c.validate("text", "json"); err != nil {
		t.Errorf("valid format rejected: %v", err)
	}
}
