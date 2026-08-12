package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/out"
)

// ---- graph ----

func cmdGraph() error {
	cmd := &CLI.Graph
	c, err := fromCommonFlags(cmd.CommonFlags)
	if err != nil {
		return err
	}
	if c.limit == 0 {
		c.limit = 40
	}
	lo := fromLensFlags(cmd.LensFlags)
	if err := c.validate("text", "json", "mermaid", "dot", "sankey"); err != nil {
		return err
	}
	if err := c.ensureData(); err != nil {
		return err
	}
	corpus, l, err := lo.corpus(c.eventsPath())
	if err != nil {
		return err
	}
	f := mine.BuildFollows(corpus)

	edges := f.Edges[:0:0]
	for _, e := range f.Edges {
		if e.Count >= cmd.MinCount {
			edges = append(edges, e)
		}
	}
	totalEdges := len(edges)
	if c.limit > 0 && len(edges) > c.limit {
		edges = edges[:c.limit]
	}

	switch c.format {
	case fmtJSON:
		type je struct {
			From  string `json:"from"`
			To    string `json:"to"`
			Count int    `json:"count"`
		}
		type jc struct {
			A, B  string
			Count int
		}
		rows := make([]je, len(edges))
		for i, e := range edges {
			rows[i] = je{corpus.Vocab[e.From], corpus.Vocab[e.To], e.Count}
		}
		var cyc []jc
		for i, cy := range f.Cycles {
			if i >= 20 {
				break
			}
			cyc = append(cyc, jc{corpus.Vocab[cy.A], corpus.Vocab[cy.B], cy.Count})
		}
		return out.JSON(os.Stdout, map[string]any{
			keyLens: l.Name(),
			"edges": rows, "edgesTotal": totalEdges, keyTruncated: len(rows) < totalEdges,
			"cycles": cyc, "cyclesTotal": len(f.Cycles),
		})
	case "mermaid", "dot", "sankey":
		return writeGraph(os.Stdout, c.format, corpus, edges)
	}

	sink := out.NewSink(os.Stdout, c.limit, c.maxBytes)
	about(sink,
		"≡ graph: action→action transition counts (what typically follows what).",
		"≡ --loops adds A⇄B bounce cycles — back-and-forth churn, often friction.")
	sink.Head("graph lens=%s edges=%d (min-count=%d)", l.Name(), len(edges), cmd.MinCount)
	emptyNote(sink, len(edges), "edges")
	for _, e := range edges {
		sink.Row("%6dx  %s → %s", e.Count, corpus.Vocab[e.From], corpus.Vocab[e.To])
	}
	if err := sink.Close(); err != nil {
		return err
	}
	if cmd.Loops {
		// cycles get their own budget — they're the friction report, not overflow
		ls := out.NewSink(os.Stdout, 20, c.maxBytes)
		ls.Head("bounce cycles (A→B→A):")
		for _, cy := range f.Cycles {
			if !ls.Row("%6dx  %s ⇄ %s", cy.Count, corpus.Vocab[cy.A], corpus.Vocab[cy.B]) {
				break
			}
		}
		return ls.Close()
	}
	return nil
}

func writeGraph(w *os.File, format string, c *mine.Corpus, edges []mine.Edge) error {
	nodeID := map[uint32]string{}
	id := func(t uint32) string {
		if n, ok := nodeID[t]; ok {
			return n
		}
		n := fmt.Sprintf("n%d", len(nodeID))
		nodeID[t] = n
		return n
	}
	switch format {
	case "mermaid":
		fmt.Fprintln(w, "flowchart LR")
		for _, e := range edges {
			fmt.Fprintf(w, "  %s[\"%s\"] -->|%d| %s[\"%s\"]\n",
				id(e.From), mermaidLabel(c.Vocab[e.From]), e.Count, id(e.To), mermaidLabel(c.Vocab[e.To]))
		}
		return nil
	case "sankey":
		// mermaid sankey-beta: a header line, a blank line, then CSV rows
		// "source,target,value" — weights are the raw transition counts.
		fmt.Fprintln(w, "sankey-beta")
		fmt.Fprintln(w)
		for _, e := range edges {
			fmt.Fprintf(w, "%s,%s,%d\n", sankeyField(c.Vocab[e.From]), sankeyField(c.Vocab[e.To]), e.Count)
		}
		return nil
	}
	fmt.Fprintln(w, "digraph ferret {")
	fmt.Fprintln(w, "  rankdir=LR;")
	for _, e := range edges {
		fmt.Fprintf(w, "  %q -> %q [label=%d];\n", c.Vocab[e.From], c.Vocab[e.To], e.Count)
	}
	fmt.Fprintln(w, "}")
	return nil
}

// mermaidLabel escapes characters that break a quoted mermaid node label.
// Exact-lens tokens carry raw targets (paths, patterns) that can contain any of them.
func mermaidLabel(s string) string {
	r := strings.NewReplacer(`"`, "#quot;", "[", "#91;", "]", "#93;", "{", "#123;", "}", "#125;")
	return r.Replace(s)
}

// sankeyField renders one CSV field of a mermaid sankey-beta row (RFC 4180):
// a value containing a comma, quote, or line break (LF or CR) is wrapped in
// quotes with embedded quotes doubled. Exact-lens tokens can carry raw command
// text with commas, and an unquoted comma — or a bare CR, which CSV parsers
// treat as a record boundary — would silently corrupt the row.
func sankeyField(s string) string {
	if !strings.ContainsAny(s, `",`+"\n\r") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
