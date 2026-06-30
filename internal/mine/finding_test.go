package mine

import "testing"

// tb is one token with its measured byte cost, for building test corpora.
type tb struct {
	tok   string
	bytes int
}

// bytesCorpus builds a Corpus from streams of (token, bytes) pairs so the
// burn measurement has real sizes to sum. Every stream gets the same key — use
// bytesCorpusKeyed when a test needs to distinguish streams (e.g. by surprise).
func bytesCorpus(streams [][]tb) *Corpus {
	keys := make([]string, len(streams))
	for i := range keys {
		keys[i] = "p/s@"
	}
	return bytesCorpusKeyed(streams, keys)
}

// bytesCorpusKeyed is bytesCorpus with an explicit per-stream key, so a test can
// attach a distinct surprise score to each stream.
func bytesCorpusKeyed(streams [][]tb, keys []string) *Corpus {
	c := &Corpus{}
	intern := map[string]uint32{}
	id := func(t string) uint32 {
		if v, ok := intern[t]; ok {
			return v
		}
		v := uint32(len(c.Vocab))
		intern[t] = v
		c.Vocab = append(c.Vocab, t)
		return v
	}
	for si, toks := range streams {
		st := make([]Tok, 0, len(toks))
		for i, x := range toks {
			st = append(st, Tok{ID: id(x.tok), Seq: i, Bytes: x.bytes})
		}
		c.Streams = append(c.Streams, st)
		c.StreamKeys = append(c.StreamKeys, keys[si])
	}
	return c
}

func idsFor(c *Corpus, toks ...string) []uint32 {
	out := make([]uint32, len(toks))
	for i, t := range toks {
		found := false
		for id, v := range c.Vocab {
			if v == t {
				out[i] = uint32(id)
				found = true
				break
			}
		}
		if !found {
			panic("idsFor: token not in vocab: " + t)
		}
	}
	return out
}

func TestMeasureCountsNonOverlappingOccurrences(t *testing.T) {
	// Two back-to-back a→b routines in one stream, one in another.
	c := bytesCorpus([][]tb{
		{{"a", 10}, {"b", 20}, {"a", 10}, {"b", 20}},
		{{"a", 10}, {"x", 5}, {"b", 20}},
	})
	count, sessions, burn := measure(c, idsFor(c, "a", "b"), 3)
	if count != 3 {
		t.Errorf("count = %d, want 3 (2 + 1 occurrences)", count)
	}
	if sessions != 2 {
		t.Errorf("sessions = %d, want 2", sessions)
	}
	// 3 occurrences × (10 + 20) member bytes = 90; the gap token x is not a member.
	if burn != 90 {
		t.Errorf("burn = %d, want 90 (member bytes only, gaps excluded)", burn)
	}
}

func TestMatchAtRespectsMaxGap(t *testing.T) {
	c := bytesCorpus([][]tb{{{"a", 1}, {"x", 1}, {"x", 1}, {"b", 1}}})
	st := c.Streams[0]
	if _, _, ok := matchAt(st, 0, idsFor(c, "a", "b"), 1); ok {
		t.Error("a→b should not match across 2 gap tokens at maxGap=1")
	}
	if _, _, ok := matchAt(st, 0, idsFor(c, "a", "b"), 3); !ok {
		t.Error("a→b should match across 2 gap tokens at maxGap=3")
	}
}

func TestFailRate(t *testing.T) {
	c := bytesCorpus([][]tb{{{"Edit!", 1}, {"Read", 1}}})
	got := failRate(c, idsFor(c, "Edit!", "Read"))
	if got != 0.5 {
		t.Errorf("failRate = %.2f, want 0.50 (1 of 2 fail-marked)", got)
	}
	if r := failRate(c, idsFor(c, "Read")); r != 0 {
		t.Errorf("clean motif failRate = %.2f, want 0", r)
	}
}

func TestBucketKindMapping(t *testing.T) {
	cases := []struct {
		bucket string
		kind   FindingKind
		action Action
	}{
		{BucketScript, KindRoutine, ActionScript},
		{BucketFriction, KindFriction, ActionHook},
		{BucketLoop, KindLoop, ActionTrim},
		{BucketWatch, KindNoise, ActionTrim},
	}
	for _, tc := range cases {
		k, a := bucketKind(tc.bucket)
		if k != tc.kind || a != tc.action {
			t.Errorf("bucketKind(%q) = (%s, %s), want (%s, %s)", tc.bucket, k, a, tc.kind, tc.action)
		}
	}
}

func TestFindingsSortsByBurnAndProjectsKind(t *testing.T) {
	// A cheap routine and a heavy friction motif; burn must order them.
	c2 := bytesCorpus([][]tb{{{"a", 1}, {"b", 1}}, {{"c", 1000}, {"d", 1000}}})
	cards := []*Card{
		{IDs: idsFor(c2, "a", "b"), Bucket: BucketScript},
		{IDs: idsFor(c2, "c", "d"), Bucket: BucketFriction},
	}
	got := Findings(c2, cards, 3, nil, 0)
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2", len(got))
	}
	if got[0].Burn < got[1].Burn {
		t.Errorf("findings not sorted by burn desc: %d then %d", got[0].Burn, got[1].Burn)
	}
	if got[0].Kind != KindFriction || got[0].Action != ActionHook {
		t.Errorf("heaviest finding kind/action = %s/%s, want friction/hook", got[0].Kind, got[0].Action)
	}
}

func TestFindingsSkipsPhantomMotif(t *testing.T) {
	c := bytesCorpus([][]tb{{{"a", 1}, {"b", 1}}})
	// A card whose motif never matches (z not in corpus would panic on vocab;
	// use a real-but-absent ordering instead: b then a never occurs).
	cards := []*Card{{IDs: idsFor(c, "b", "a"), Bucket: BucketScript}}
	if got := Findings(c, cards, 3, nil, 0); len(got) != 0 {
		t.Errorf("got %d findings for a never-matching motif, want 0", len(got))
	}
}

// TestAttachDialogueRollsUpPerMotif guards ferret-bbp.6: the per-session dialogue
// Outcome + Hop2 retrieval grade roll onto each motif's Finding, aggregated over the
// streams the motif recurs in (the surprise grain). The WORST outcome and WORST Hop2
// grade across host sessions surface (so a motif that ends abandoned in any of its
// sessions reads that way), and repair/accept turns SUM. A nil index is the off
// switch, and Hop1 (the bbp.5 LLM leg) is never set here.
func TestAttachDialogueRollsUpPerMotif(t *testing.T) {
	// motif a→b recurs in two sessions; c→d only in the third.
	c := bytesCorpusKeyed([][]tb{
		{{"a", 1}, {"b", 1}},
		{{"a", 1}, {"b", 1}},
		{{"c", 1}, {"d", 1}},
	}, []string{"p/s1@", "p/s2@", "p/s3@"})

	idx := map[string]StreamDialogue{
		"p/s1@": {Outcome: "success", Hop2: "high", Accepts: 1},
		"p/s2@": {Outcome: "abandoned", Hop2: "low", Repairs: 2},
		"p/s3@": {Outcome: "repair-heavy", Hop2: "mid", Repairs: 1, Accepts: 1},
	}

	cards := []*Card{
		{IDs: idsFor(c, "a", "b"), Bucket: BucketScript},
		{IDs: idsFor(c, "c", "d"), Bucket: BucketScript},
	}
	got := Findings(c, cards, 3, nil, 0)
	AttachDialogue(got, c, idx, 3)

	byMotif := func(tok string) *Finding {
		t.Helper()
		want := idsFor(c, tok)[0]
		for _, f := range got {
			if f.IDs[0] == want {
				return f
			}
		}
		t.Fatalf("no finding rooted at %q", tok)
		return nil
	}

	ab := byMotif("a")
	if ab.Outcome != "abandoned" {
		t.Errorf("a→b Outcome = %q, want abandoned (worst across s1+s2)", ab.Outcome)
	}
	if ab.Hop2 != "low" {
		t.Errorf("a→b Hop2 = %q, want low (worst across s1+s2)", ab.Hop2)
	}
	if ab.Repairs != 2 || ab.Accepts != 1 {
		t.Errorf("a→b repairs/accepts = %d/%d, want 2/1 (summed across s1+s2)", ab.Repairs, ab.Accepts)
	}
	if ab.Hop1 != "" {
		t.Errorf("a→b Hop1 = %q, want empty (bbp.5 not built)", ab.Hop1)
	}

	cd := byMotif("c")
	if cd.Outcome != "repair-heavy" || cd.Hop2 != "mid" {
		t.Errorf("c→d outcome/hop2 = %q/%q, want repair-heavy/mid (s3 only)", cd.Outcome, cd.Hop2)
	}
}

// TestAttachDialogueNilIndexIsOff proves the off switch: a nil index leaves every
// dialogue field at its zero value (the fix-baseline path, which only needs burn).
func TestAttachDialogueNilIndexIsOff(t *testing.T) {
	c := bytesCorpusKeyed([][]tb{{{"a", 1}, {"b", 1}}}, []string{"p/s1@"})
	got := Findings(c, []*Card{{IDs: idsFor(c, "a", "b"), Bucket: BucketScript}}, 3, nil, 0)
	AttachDialogue(got, c, nil, 3)
	f := got[0]
	if f.Outcome != "" || f.Hop2 != "" || f.Repairs != 0 || f.Accepts != 0 {
		t.Errorf("nil index should leave dialogue fields zero, got %+v", f)
	}
}

// TestAttachDialogueSkipsUnscoredStreams proves a motif seen only in streams absent
// from the index keeps its zero-value dialogue fields — no phantom signal.
func TestAttachDialogueSkipsUnscoredStreams(t *testing.T) {
	c := bytesCorpusKeyed([][]tb{{{"a", 1}, {"b", 1}}}, []string{"p/s1@"})
	got := Findings(c, []*Card{{IDs: idsFor(c, "a", "b"), Bucket: BucketScript}}, 3, nil, 0)
	AttachDialogue(got, c, map[string]StreamDialogue{"other@": {Outcome: "abandoned", Hop2: "low"}}, 3)
	if f := got[0]; f.Outcome != "" || f.Hop2 != "" {
		t.Errorf("motif in an unscored stream should stay zero, got Outcome=%q Hop2=%q", f.Outcome, f.Hop2)
	}
}

// TestFindingsSplitsRoutineBySurprise guards ferret-c7r: the SAME recurring
// motif is a routine to script when its host sessions are predictable, but
// friction to fix when those sessions are surprising. Surprise — not the motif
// itself — decides routine vs friction; a nil surprise index leaves the base
// kind untouched (the fix-baseline path), and the chosen kind drives the action.
func TestFindingsSplitsRoutineBySurprise(t *testing.T) {
	// One routine motif a→b→c recurring across two distinctly-keyed sessions.
	c := bytesCorpusKeyed([][]tb{
		{{"a", 1}, {"b", 1}, {"c", 1}},
		{{"a", 1}, {"b", 1}, {"c", 1}},
	}, []string{"p/lo@", "p/hi@"})

	cases := []struct {
		name       string
		surprise   map[string]float64
		cut        float64
		wantKind   FindingKind
		wantAction Action
	}{
		{"low surprise stays routine",
			map[string]float64{"p/lo@": 0.5, "p/hi@": 0.7}, 1.0, KindRoutine, ActionScript},
		{"high surprise becomes friction",
			map[string]float64{"p/lo@": 2.0, "p/hi@": 3.0}, 1.0, KindFriction, ActionHook},
		{"no surprise signal keeps base kind",
			nil, 1.0, KindRoutine, ActionScript},
		{"unscored streams keep base kind",
			map[string]float64{"other@": 9.0}, 1.0, KindRoutine, ActionScript},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cards := []*Card{{IDs: idsFor(c, "a", "b", "c"), Bucket: BucketScript}}
			got := Findings(c, cards, 3, tc.surprise, tc.cut)
			if len(got) != 1 {
				t.Fatalf("got %d findings, want 1", len(got))
			}
			if got[0].Kind != tc.wantKind || got[0].Action != tc.wantAction {
				t.Errorf("kind/action = %s/%s, want %s/%s (surprise=%v cut=%.1f)",
					got[0].Kind, got[0].Action, tc.wantKind, tc.wantAction, tc.surprise, tc.cut)
			}
		})
	}
}
