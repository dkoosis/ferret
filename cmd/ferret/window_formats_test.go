package main

import (
	"strings"
	"testing"
)

const wantWindowLine = "window since=2026-10-19 kept=2 of 4 streams · dropped no-timestamp=1"

// The markdown cost report and the diagram formats have no text header, so a
// window would silently pass for the whole corpus unless each carries it.
func TestReportMD_Window_NamesTheWindow(t *testing.T) {
	saved := CLI.Report
	t.Cleanup(func() { CLI.Report = saved })
	CLI.Report.Data, CLI.Report.Format, CLI.Report.Lens = writeWindowCorpus(t), fmtMD, "cmd"
	CLI.Report.MinSupport, CLI.Report.MaxGap, CLI.Report.MaxLen = 2, 3, 8
	CLI.Report.Order, CLI.Report.Top, CLI.Report.Limit = 3, 10, -1

	CLI.Report.Since = "2026-10-19"
	got := captureStdout(t, cmdReport)
	if !strings.Contains(got, "\n_"+wantWindowLine+"_\n") {
		t.Errorf("windowed markdown report lacks the window note:\n%s", got)
	}

	CLI.Report.Since = ""
	if plain := captureStdout(t, cmdReport); strings.Contains(plain, "window") {
		t.Errorf("unwindowed markdown report mentions a window:\n%s", plain)
	}
}

func TestGraphDiagrams_Window_NameTheWindowAsAComment(t *testing.T) {
	data := writeWindowCorpus(t)
	for format, comment := range map[string]string{
		"mermaid": "%% " + wantWindowLine + "\n",
		"sankey":  "%% " + wantWindowLine + "\n",
		"dot":     "  // " + wantWindowLine + "\n",
	} {
		t.Run(format, func(t *testing.T) {
			saved := CLI.Graph
			t.Cleanup(func() { CLI.Graph = saved })
			CLI.Graph.Data, CLI.Graph.Format, CLI.Graph.Limit = data, format, -1
			CLI.Graph.Lens, CLI.Graph.MinCount = "cmd", 1

			CLI.Graph.Since = "2026-10-19"
			got := captureStdout(t, cmdGraph)
			if !strings.Contains(got, comment) {
				t.Errorf("windowed %s lacks the comment %q:\n%s", format, comment, got)
			}

			CLI.Graph.Since = ""
			if plain := captureStdout(t, cmdGraph); strings.Contains(plain, "window") {
				t.Errorf("unwindowed %s mentions a window:\n%s", format, plain)
			}
		})
	}
}
