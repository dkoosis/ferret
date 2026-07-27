package mine

import (
	"maps"
	"testing"
)

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
	count, sessions, burn, side := measure(c, idsFor(c, "a", "b"), 3)
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
	if side != 0 {
		t.Errorf("side = %d, want 0 (no sidechain streams marked)", side)
	}
}

// TestMeasureSplitsSidechainBurn pins the ferret-9j3 pool label: burn from a
// subagent sidechain stream lands in sideBytes so the report can say which
// pool an all-streams number draws from. Total burn is unchanged — sideBytes
// is a slice of it, not an addition.
func TestMeasureSplitsSidechainBurn(t *testing.T) {
	c := bytesCorpus([][]tb{
		{{"a", 10}, {"b", 20}},                           // main stream: 30 bytes
		{{"a", 100}, {"b", 200}, {"a", 100}, {"b", 200}}, // sidechain: 600 bytes
	})
	c.Sidechain = []bool{false, true}
	count, sessions, burn, side := measure(c, idsFor(c, "a", "b"), 3)
	if count != 3 || sessions != 2 {
		t.Errorf("count, sessions = %d, %d, want 3, 2", count, sessions)
	}
	if burn != 630 {
		t.Errorf("burn = %d, want 630 (main 30 + sidechain 600)", burn)
	}
	if side != 600 {
		t.Errorf("side = %d, want 600 (sidechain stream's bytes only)", side)
	}
}

// TestMeasureNilSidechainIndex covers corpora built without per-stream
// sidechain flags (older artifacts, hand-built tests): every stream counts as
// main, no panic.
func TestMeasureNilSidechainIndex(t *testing.T) {
	c := bytesCorpus([][]tb{{{"a", 10}, {"b", 20}}})
	c.Sidechain = nil
	_, _, burn, side := measure(c, idsFor(c, "a", "b"), 3)
	if burn != 30 || side != 0 {
		t.Errorf("burn, side = %d, %d, want 30, 0", burn, side)
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

// oddsRatioCorpus builds a Corpus of nStreams single-token streams (motif
// "z" alone, so it never matches any real motif), for padding the labeled
// corpus-wide baseline in odds-ratio tests without affecting host counts.
func oddsRatioCorpus(prefix string, n int) ([][]tb, []string) {
	streams := make([][]tb, n)
	keys := make([]string, n)
	for i := range streams {
		streams[i] = []tb{{"z", 1}}
		keys[i] = prefix + "/pad" + string(rune('a'+i)) + "@"
	}
	return streams, keys
}

// TestAttachOddsRatioSurfacesLowBurnHighSignalMotif guards ferret-qus: a cheap
// (low-burn) motif whose host streams correlate strongly with failure gets a
// meaningful odds ratio, while an expensive (high-burn) motif with too few
// labeled host streams to trust is suppressed — so ranking findings by
// |OutcomeOddsRatio-1| surfaces the low-burn motif on top, exactly the
// burn-only-buries-it case the ferret-567.4 spike measured (a 5-step
// git-workflow motif ranked 30th by burn but showing a 36% fail rate vs 25%
// corpus base). Burn-only ordering itself must stay unchanged (asserted first).
func TestAttachOddsRatioSurfacesLowBurnHighSignalMotif(t *testing.T) {
	// "bad" motif: cheap (burn ~2), 12 host streams, mostly-fail (2 success / 10
	// fail) -- well past MinOddsRatioSupport and starkly worse than the corpus
	// base rate.
	badStreams := make([][]tb, 12)
	badKeys := make([]string, 12)
	for i := range badStreams {
		badStreams[i] = []tb{{"bad1", 1}, {"bad2", 1}}
		badKeys[i] = "p/bad" + string(rune('a'+i)) + "@"
	}
	badIdx := map[string]StreamDialogue{}
	for i := range 12 {
		outcome := "abandoned" // fail bucket
		if i < 2 {
			outcome = "success"
		}
		badIdx[badKeys[i]] = StreamDialogue{Outcome: outcome}
	}

	// "pricey" motif: expensive (burn ~2000), but only 3 host streams -- below
	// MinOddsRatioSupport, so it must come back suppressed regardless of how
	// its thin sample looks.
	priceyStreams := [][]tb{
		{{"pricey1", 1000}, {"pricey2", 1000}},
		{{"pricey1", 1000}, {"pricey2", 1000}},
		{{"pricey1", 1000}, {"pricey2", 1000}},
	}
	priceyKeys := []string{"p/px_a@", "p/px_b@", "p/px_c@"}
	priceyIdx := map[string]StreamDialogue{
		"p/px_a@": {Outcome: "success"},
		"p/px_b@": {Outcome: "success"},
		"p/px_c@": {Outcome: "abandoned"},
	}

	// Pad the corpus-wide labeled baseline with plain successes so the
	// contingency table has a real "rest of corpus" leg to compare against.
	padStreams, padKeys := oddsRatioCorpus("p", 8)
	padIdx := map[string]StreamDialogue{}
	for _, k := range padKeys {
		padIdx[k] = StreamDialogue{Outcome: "success"}
	}

	allStreams := append(append(append([][]tb{}, badStreams...), priceyStreams...), padStreams...)
	allKeys := append(append(append([]string{}, badKeys...), priceyKeys...), padKeys...)
	c := bytesCorpusKeyed(allStreams, allKeys)

	idx := map[string]StreamDialogue{}
	maps.Copy(idx, badIdx)
	maps.Copy(idx, priceyIdx)
	maps.Copy(idx, padIdx)

	cards := []*Card{
		{IDs: idsFor(c, "bad1", "bad2"), Bucket: BucketScript},
		{IDs: idsFor(c, "pricey1", "pricey2"), Bucket: BucketScript},
	}
	got := Findings(c, cards, 3, nil, 0)
	if got[0].Kind == "" {
		t.Fatalf("Findings returned no kind, setup broken")
	}
	// Burn-only default ordering is unchanged: the expensive motif still
	// outranks the cheap one by Burn.
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
	bad := byMotif("bad1")
	pricey := byMotif("pricey1")
	if pricey.Burn <= bad.Burn {
		t.Fatalf("pricey.Burn=%d should stay > bad.Burn=%d (default sort unaffected)", pricey.Burn, bad.Burn)
	}
	if got[0].IDs[0] != pricey.IDs[0] {
		t.Fatalf("default Findings order must still rank by burn first (pricey first), got motif rooted at %v", got[0].IDs)
	}

	AttachOddsRatio(got, c, idx, 3, MinOddsRatioSupport)

	if bad.OddsRatioSupport != 12 {
		t.Errorf("bad.OddsRatioSupport = %d, want 12 (2 success + 10 fail host streams)", bad.OddsRatioSupport)
	}
	if bad.OutcomeOddsRatio == nil {
		t.Fatalf("bad.OutcomeOddsRatio = nil, want a trusted ratio (support=12 clears MinOddsRatioSupport=%d)", MinOddsRatioSupport)
	}
	if *bad.OutcomeOddsRatio >= 1.0 {
		t.Errorf("bad.OutcomeOddsRatio = %.4f, want < 1 (host streams correlate with failure)", *bad.OutcomeOddsRatio)
	}

	if pricey.OddsRatioSupport != 3 {
		t.Errorf("pricey.OddsRatioSupport = %d, want 3 (below MinOddsRatioSupport=%d)", pricey.OddsRatioSupport, MinOddsRatioSupport)
	}
	if pricey.OutcomeOddsRatio != nil {
		t.Errorf("pricey.OutcomeOddsRatio = %.4f, want nil (suppressed: support=3 < MinOddsRatioSupport=%d)",
			*pricey.OutcomeOddsRatio, MinOddsRatioSupport)
	}

	// The point of the feature: ranking by |ratio-1| (only among TRUSTED
	// ratios) surfaces the cheap, fail-correlated motif — burn-only structurally
	// cannot, since pricey outranks bad on burn alone.
	if bad.OutcomeOddsRatio == nil {
		t.Fatal("bad ratio unexpectedly nil, cannot compare signal strength")
	}
	badSignal := absFloat(*bad.OutcomeOddsRatio - 1.0)
	if pricey.OutcomeOddsRatio != nil {
		t.Fatalf("pricey ratio should be suppressed (nil) so it can't even enter the |ratio-1| ranking")
	}
	if badSignal <= 0 {
		t.Errorf("bad |ratio-1| = %.4f, want a nonzero, meaningful deviation from 1", badSignal)
	}
}

// absFloat is a tiny local abs, so the test doesn't reach for math.Abs over a
// single call site.
func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// TestAttachOddsRatioSuppressesBelowMinSupport guards the ferret-567.4 spike's
// core caveat: a motif with ZERO labeled host streams (a=b=0) must report
// OutcomeOddsRatio as nil (suppressed/unknown), never a spurious value —
// Laplace smoothing alone can produce a nontrivial ratio purely reflecting the
// corpus base rate, with no motif-specific evidence behind it.
func TestAttachOddsRatioSuppressesBelowMinSupport(t *testing.T) {
	// The motif's only host stream carries NO dialogue signal at all (absent
	// from idx) -- support must come back 0.
	padStreams, padKeys := oddsRatioCorpus("p", 20)
	motifStreams := make([][]tb, 1, 1+len(padStreams))
	motifStreams[0] = []tb{{"m1", 1}, {"m2", 1}}
	motifKeys := make([]string, 1, 1+len(padKeys))
	motifKeys[0] = "p/unlabeled@"
	padIdx := map[string]StreamDialogue{}
	for i, k := range padKeys {
		outcome := "success"
		if i%4 == 0 {
			outcome = "repair-heavy"
		}
		padIdx[k] = StreamDialogue{Outcome: outcome}
	}

	c := bytesCorpusKeyed(append(motifStreams, padStreams...), append(motifKeys, padKeys...))
	cards := []*Card{{IDs: idsFor(c, "m1", "m2"), Bucket: BucketScript}}
	got := Findings(c, cards, 3, nil, 0)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1", len(got))
	}

	AttachOddsRatio(got, c, padIdx, 3, MinOddsRatioSupport)

	f := got[0]
	if f.OddsRatioSupport != 0 {
		t.Errorf("OddsRatioSupport = %d, want 0 (motif's only host stream carries no dialogue signal)", f.OddsRatioSupport)
	}
	if f.OutcomeOddsRatio != nil {
		t.Errorf("OutcomeOddsRatio = %.4f, want nil (suppressed: support=0 < MinOddsRatioSupport=%d) -- "+
			"the spike's core caveat: Laplace smoothing alone must not leak through as a spurious ratio",
			*f.OutcomeOddsRatio, MinOddsRatioSupport)
	}
}

// TestAttachOddsRatioNilIndexIsOff proves the off switch, mirroring
// TestAttachDialogueNilIndexIsOff: a nil index leaves both new fields at zero.
func TestAttachOddsRatioNilIndexIsOff(t *testing.T) {
	c := bytesCorpusKeyed([][]tb{{{"a", 1}, {"b", 1}}}, []string{"p/s1@"})
	got := Findings(c, []*Card{{IDs: idsFor(c, "a", "b"), Bucket: BucketScript}}, 3, nil, 0)
	AttachOddsRatio(got, c, nil, 3, MinOddsRatioSupport)
	f := got[0]
	if f.OutcomeOddsRatio != nil || f.OddsRatioSupport != 0 {
		t.Errorf("nil index should leave odds-ratio fields zero, got support=%d ratio=%v", f.OddsRatioSupport, f.OutcomeOddsRatio)
	}
}

// TestFrictionAddTurnsToGoal locks the goal-state-aware rollup: once any session
// reaches the goal, TurnsToGoal is drawn from reached sessions only, so an
// unreached session's larger total-turn cost can't inflate the rendered ttg
// beside a "goal reached" verdict (bbp.10 review, PR #58).
func TestFrictionAddTurnsToGoal(t *testing.T) {
	tests := []struct {
		name            string
		base, other     Friction
		wantTTG         int
		wantGoalReached bool
	}{
		{
			name:            "reached folds unreached-longer: reached path wins",
			base:            Friction{GoalReached: true, TurnsToGoal: 5},
			other:           Friction{GoalReached: false, TurnsToGoal: 10},
			wantTTG:         5,
			wantGoalReached: true,
		},
		{
			name:            "unreached-first folds reached: reached path adopted",
			base:            Friction{GoalReached: false, TurnsToGoal: 10},
			other:           Friction{GoalReached: true, TurnsToGoal: 5},
			wantTTG:         5,
			wantGoalReached: true,
		},
		{
			name:            "both reached: worst path wins",
			base:            Friction{GoalReached: true, TurnsToGoal: 5},
			other:           Friction{GoalReached: true, TurnsToGoal: 8},
			wantTTG:         8,
			wantGoalReached: true,
		},
		{
			name:            "neither reached: longest cost is the lower bound",
			base:            Friction{GoalReached: false, TurnsToGoal: 7},
			other:           Friction{GoalReached: false, TurnsToGoal: 12},
			wantTTG:         12,
			wantGoalReached: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.base
			f.add(tt.other)
			if f.TurnsToGoal != tt.wantTTG {
				t.Errorf("TurnsToGoal = %d, want %d", f.TurnsToGoal, tt.wantTTG)
			}
			if f.GoalReached != tt.wantGoalReached {
				t.Errorf("GoalReached = %v, want %v", f.GoalReached, tt.wantGoalReached)
			}
		})
	}
}
