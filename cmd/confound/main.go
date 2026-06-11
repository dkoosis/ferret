// Command confound is a THROWAWAY analysis for the 1k SWE-agent sample:
// does the exact-lens LOOP/WATCH anti-correlation with failure survive
// stratification by model and trajectory length, or is it a confound?
//
// Usage: go run ./cmd/confound -data /tmp/swe -sample /tmp/swe-sample.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/dkoosis/ferret/internal/event"
	"github.com/dkoosis/ferret/internal/lens"
	"github.com/dkoosis/ferret/internal/mine"
	"github.com/dkoosis/ferret/internal/sweagent"
)

func main() {
	data := flag.String("data", "/tmp/swe", "artifact dir")
	sample := flag.String("sample", "/tmp/swe-sample.jsonl", "raw sample jsonl")
	lensName := flag.String("lens", "exact", "lens")
	minSupport := flag.Int("min-support", 20, "")
	maxGap := flag.Int("max-gap", 3, "")
	flag.Parse()

	l, err := lens.Get(*lensName)
	if err != nil {
		log.Fatal(err)
	}
	cor, err := mine.Build(filepath.Join(*data, "events.jsonl"), l, mine.Options{MarkFail: true, Collapse: true})
	if err != nil {
		log.Fatal(err)
	}
	outcomes, err := event.ReadOutcomes(filepath.Join(*data, "outcomes.jsonl"))
	if err != nil {
		log.Fatal(err)
	}
	pats, _ := mine.MineSeqs(cor, mine.SeqOpts{MinSupport: *minSupport, MaxGap: *maxGap, MaxLen: 5, MaxPatterns: 10000})
	cards, _ := mine.RankPatterns(cor, pats, mine.DefaultRankOpts())

	meta := loadMeta(*sample) // stream key → {model, aiMsgs}

	byBucket := map[string][]*mine.Card{}
	for _, c := range cards {
		byBucket[c.Bucket] = append(byBucket[c.Bucket], c)
	}

	for _, bucket := range []string{"loop", "watch", "friction", "script"} {
		bc := byBucket[bucket]
		if len(bc) == 0 {
			continue
		}
		member := supporting(cor, bc, *maxGap)
		fmt.Printf("\n== %s (lens=%s, %d patterns, %d streams) ==\n", bucket, *lensName, len(bc), len(member))
		stratify(cor, member, outcomes, meta)
	}
}

type rowMeta struct {
	model  string
	aiMsgs int
}

type cell struct{ n, resolved int }

// loadMeta replays the ingest rollout-suffix logic so stream keys line up.
func loadMeta(path string) map[string]rowMeta {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	meta := map[string]rowMeta{}
	rollouts := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<27)
	for sc.Scan() {
		var r struct {
			InstanceID string          `json:"instance_id"`
			ModelName  string          `json:"model_name"`
			Trajectory json.RawMessage `json:"trajectory"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		var msgs []struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal(r.Trajectory, &msgs)
		ai := 0
		for _, m := range msgs {
			if m.Role == "ai" {
				ai++
			}
		}
		id := r.InstanceID
		rollouts[id]++
		if n := rollouts[id]; n > 1 {
			id = fmt.Sprintf("%s#%d", id, n)
		}
		meta[sweagent.Project+"/"+id+"@"] = rowMeta{model: r.ModelName, aiMsgs: ai}
	}
	return meta
}

// stratify prints resolve rates in/out of the bucket per model stratum and
// per trajectory-length tercile.
func stratify(cor *mine.Corpus, member map[int]bool, outcomes map[string]event.Outcome, meta map[string]rowMeta) {
	inM, outM := map[string]*cell{}, map[string]*cell{}
	var lens []int // ai-msg lengths for tercile cuts
	for si := range cor.StreamKeys {
		m, ok := meta[cor.StreamKeys[si]]
		if ok {
			lens = append(lens, m.aiMsgs)
		}
	}
	sort.Ints(lens)
	if len(lens) == 0 {
		return // no metadata matched; nothing to stratify
	}
	t1, t2 := lens[len(lens)/3], lens[2*len(lens)/3]
	tier := func(n int) string {
		switch {
		case n <= t1:
			return fmt.Sprintf("len<=%d", t1)
		case n <= t2:
			return fmt.Sprintf("len<=%d", t2)
		default:
			return fmt.Sprintf("len>%d", t2)
		}
	}
	add := func(m map[string]*cell, k string, resolved bool) {
		c := m[k]
		if c == nil {
			c = &cell{}
			m[k] = c
		}
		c.n++
		if resolved {
			c.resolved++
		}
	}
	for si, key := range cor.StreamKeys {
		o, ok := outcomes[key]
		if !ok {
			continue
		}
		mt, ok := meta[key]
		if !ok {
			continue
		}
		dst := outM
		if member[si] {
			dst = inM
		}
		add(dst, "model="+mt.model, o.Target)
		add(dst, tier(mt.aiMsgs), o.Target)
		add(dst, "ALL", o.Target)
	}
	keys := map[string]bool{}
	for k := range inM {
		keys[k] = true
	}
	for k := range outM {
		keys[k] = true
	}
	ks := make([]string, 0, len(keys))
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Printf("%-28s %18s %18s\n", "stratum", "in-bucket", "out-of-bucket")
	for _, k := range ks {
		fmt.Printf("%-28s %18s %18s\n", k, rate(inM[k]), rate(outM[k]))
	}
}

func rate(c *cell) string {
	if c == nil || c.n == 0 {
		return "—"
	}
	return fmt.Sprintf("%4.1f%% (n=%d)", 100*float64(c.resolved)/float64(c.n), c.n)
}

// supporting mirrors mine.Validate's gap-bounded containment.
func supporting(c *mine.Corpus, cards []*mine.Card, maxGap int) map[int]bool {
	set := map[int]bool{}
	for si, st := range c.Streams {
		ids := make([]uint32, len(st))
		for i, t := range st {
			ids[i] = t.ID
		}
		for _, card := range cards {
			if containsSubseqGap(ids, card.IDs, maxGap) {
				set[si] = true
				break
			}
		}
	}
	return set
}

func containsSubseqGap(seq, sub []uint32, maxGap int) bool {
	if len(sub) == 0 {
		return false
	}
	var ends []int
	for p, id := range seq {
		if id == sub[0] {
			ends = append(ends, p)
		}
	}
	for k := 1; k < len(sub) && len(ends) > 0; k++ {
		var next []int
		lastEnd := -1
		for _, e := range ends {
			for p := e + 1; p <= e+maxGap && p < len(seq); p++ {
				if seq[p] == sub[k] && p > lastEnd {
					next = append(next, p)
					lastEnd = p
				}
			}
		}
		ends = next
	}
	return len(ends) > 0
}
