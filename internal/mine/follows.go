package mine

import "sort"

// Edge is one directly-follows transition A → B with its count.
type Edge struct {
	From, To uint32
	Count    int
}

// Cycle is an A→B→A bounce — a friction signature. The pair is undirected
// (A < B): A→B→A and B→A→B bounces aggregate into one cycle.
type Cycle struct {
	A, B  uint32
	Count int
}

// Follows holds the directly-follows graph for one corpus.
type Follows struct {
	Edges  []Edge
	Cycles []Cycle
}

// BuildFollows computes transitions and 2-cycles in one pass per stream.
//
// The directly-follows graph is the core abstraction of process mining: van
// der Aalst, Weijters & Maruster, "Workflow Mining: Discovering Process
// Models from Event Logs", IEEE TKDE 16(9), 2004 (the α-algorithm); survey
// treatment in van der Aalst, "Process Mining: Data Science in Action",
// Springer 2016. Detecting length-two loops (A→B→A) as a distinct construct
// follows Alves de Medeiros, van Dongen, van der Aalst & Weijters, "Process
// Mining: Extending the α-algorithm to Mine Short Loops", BETA Working Paper
// 113, TU Eindhoven, 2004.
func BuildFollows(c *Corpus) *Follows {
	edges := map[[2]uint32]int{}
	cycles := map[[2]uint32]int{}
	for _, st := range c.Streams {
		for i := 1; i < len(st); i++ {
			edges[[2]uint32{st[i-1].ID, st[i].ID}]++
			if i >= 2 && st[i-2].ID == st[i].ID && st[i-1].ID != st[i].ID {
				// undirected key: A→B→A and B→A→B are the same bounce
				cycles[[2]uint32{min(st[i].ID, st[i-1].ID), max(st[i].ID, st[i-1].ID)}]++
			}
		}
	}
	f := &Follows{}
	for k, n := range edges {
		f.Edges = append(f.Edges, Edge{From: k[0], To: k[1], Count: n})
	}
	for k, n := range cycles {
		f.Cycles = append(f.Cycles, Cycle{A: k[0], B: k[1], Count: n})
	}
	f.sort()
	return f
}

// sort orders by count desc, ties broken by ID so output is deterministic
// across runs (map iteration order isn't).
func (f *Follows) sort() {
	sort.Slice(f.Edges, func(i, j int) bool {
		a, b := f.Edges[i], f.Edges[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	sort.Slice(f.Cycles, func(i, j int) bool {
		a, b := f.Cycles[i], f.Cycles[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.A != b.A {
			return a.A < b.A
		}
		return a.B < b.B
	})
}
