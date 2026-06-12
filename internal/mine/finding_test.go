package mine

import "testing"

// tb is one token with its measured byte cost, for building test corpora.
type tb struct {
	tok   string
	bytes int
}

// bytesCorpus builds a Corpus from streams of (token, bytes) pairs so the
// burn measurement has real sizes to sum.
func bytesCorpus(streams [][]tb) *Corpus {
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
	for _, toks := range streams {
		st := make([]Tok, 0, len(toks))
		for i, x := range toks {
			st = append(st, Tok{ID: id(x.tok), Seq: i, Bytes: x.bytes})
		}
		c.Streams = append(c.Streams, st)
		c.StreamKeys = append(c.StreamKeys, "p/s@")
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
	got := Findings(c2, cards, 3)
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
	if got := Findings(c, cards, 3); len(got) != 0 {
		t.Errorf("got %d findings for a never-matching motif, want 0", len(got))
	}
}
