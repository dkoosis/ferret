package main

import (
	"strings"
	"testing"
)

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
