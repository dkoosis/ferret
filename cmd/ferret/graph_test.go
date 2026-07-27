package main

import (
	"io"
	"os"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
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

// TestWriteGraphSankey guards ferret-567.1: the sankey emitter must render
// mermaid sankey-beta syntax (a "sankey-beta" header, blank line, then
// "source,target,count" rows in edge order) with weights carrying the raw
// transition counts, and fields containing a comma CSV-quoted so a labeled
// token (e.g. an exact-lens target) can't corrupt the column count.
func TestWriteGraphSankey(t *testing.T) {
	corpus := &mine.Corpus{Vocab: []string{"Read", "Edit", "Bash(git status, diff)"}}
	edges := []mine.Edge{
		{From: 0, To: 1, Count: 5},
		{From: 1, To: 2, Count: 3},
		{From: 0, To: 2, Count: 1},
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGraph(w, "sankey", corpus, edges); err != nil {
		t.Fatalf("writeGraph(sankey): %v", err)
	}
	_ = w.Close()
	gotBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	want := "sankey-beta\n\n" +
		"Read,Edit,5\n" +
		`Edit,"Bash(git status, diff)",3` + "\n" +
		`Read,"Bash(git status, diff)",1` + "\n"
	if got := string(gotBytes); got != want {
		t.Errorf("writeGraph(sankey) =\n%q\nwant\n%q", got, want)
	}
}

// TestSankeyFieldEscaping guards the CSV-quoting rule sankeyField applies:
// a value containing a comma or a double quote must be wrapped in quotes
// with embedded quotes doubled, matching RFC 4180 — otherwise a comma in a
// token label would silently add a phantom sankey column.
func TestSankeyFieldEscaping(t *testing.T) {
	for in, want := range map[string]string{
		"Read":                  "Read",
		"Bash(a, b)":            `"Bash(a, b)"`,
		`Grep:"foo"`:            `"Grep:""foo"""`,
		"sh:git status && diff": "sh:git status && diff",
		"echo a\rb":             "\"echo a\rb\"", // bare CR is a CSV record boundary — must quote
		"line\nbreak":           "\"line\nbreak\"",
	} {
		if got := sankeyField(in); got != want {
			t.Errorf("sankeyField(%q) = %q, want %q", in, got, want)
		}
	}
}
