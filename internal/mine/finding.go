package mine

import "sort"

// Finding is the shared projection both renderers consume: a mined motif
// classified into an action verb, with its measured cost. Mine once, render
// twice — the agent (JSON) and the human (md) reports are two views of this.
//
// burn is REAL context cost, not an estimate: every occurrence of the motif is
// re-matched against the corpus and its member events' measured byte sizes are
// summed, then converted to tokens. burn is the sort key — a rare motif that
// drags megabytes of file contents back into context outranks a frequent but
// cheap one.
type Finding struct {
	IDs      []uint32    // motif token ids (render via Corpus.Tokens)
	Kind     FindingKind // routine | friction | loop | noise
	Action   Action      // script | hook | tool-fix | trim
	Count    int         // total occurrences across the corpus
	Sessions int         // distinct streams containing the motif
	FailRate float64     // share of member tokens that are fail-marked
	Burn     int         // measured tokens of context the motif's occurrences consumed
	Surprise float64     // mean bits/tok of the sessions the motif recurs in (0 = no signal)
	ExStream int         // exemplar location
	ExSeq    int
}

// FindingKind is what the motif IS.
type FindingKind string

// Action is what to DO about it — every Finding ends in a verb.
type Action string

const (
	KindRoutine  FindingKind = "routine"  // low-entropy chain you repeat by hand
	KindFriction FindingKind = "friction" // a failure and its recovery
	KindLoop     FindingKind = "loop"     // revisits a step — redundant re-work
	KindNoise    FindingKind = "noise"    // frequent but not actionable

	ActionScript  Action = "script"   // codify the routine
	ActionHook    Action = "hook"     // guard the failure with a hook
	ActionToolFix Action = "tool-fix" // the tool itself needs fixing
	ActionTrim    Action = "trim"     // cut the redundant context
)

// bytesPerToken is the standard rough tokenizer ratio: burn is reported in
// tokens so it reads as "what this costs the model", not raw bytes.
const bytesPerToken = 4

// bucketKind maps a rank bucket to its Finding kind + default action.
func bucketKind(bucket string) (FindingKind, Action) {
	switch bucket {
	case BucketScript:
		return KindRoutine, ActionScript
	case BucketFriction:
		return KindFriction, ActionHook
	case BucketLoop:
		return KindLoop, ActionTrim
	default: // BucketWatch
		return KindNoise, ActionTrim
	}
}

// Findings projects ranked cards into the shared Finding model, measuring each
// motif's real occurrence count and burn against the corpus, then sorting by
// burn (the cost-leak ranking). maxGap must match the value that mined the
// cards so the re-match sees the same gapped occurrences.
//
// surprise (stream key → mean bits/tok from ScoreSurprise) splits the ambiguous
// routine bucket: a recurring chain whose host sessions are more surprising than
// cut isn't a routine to script — it's friction to fix, so it's re-filed under
// friction/hook. A nil surprise index disables the split and leaves base kinds
// intact (the fix-baseline path, which only needs burn, passes nil).
func Findings(c *Corpus, cards []*Card, maxGap int, surprise map[string]float64, cut float64) []*Finding {
	out := make([]*Finding, 0, len(cards))
	for _, card := range cards {
		kind, action := bucketKind(card.Bucket)
		count, sessions, burnBytes := measure(c, card.IDs, maxGap)
		if count == 0 {
			continue // motif no longer matches (shouldn't happen, but never emit a phantom)
		}
		surp := 0.0
		if s, ok := motifSurprise(c, card.IDs, maxGap, surprise); ok {
			surp = s
			// low-surprise recurring seq → routine (script it);
			// high-surprise recurring seq → friction (a loop wearing routine's clothes).
			if kind == KindRoutine && s > cut {
				kind, action = KindFriction, ActionHook
			}
		}
		out = append(out, &Finding{
			IDs: card.IDs, Kind: kind, Action: action,
			Count: count, Sessions: sessions,
			FailRate: failRate(c, card.IDs),
			Burn:     burnBytes / bytesPerToken,
			Surprise: surp,
			ExStream: card.ExStream, ExSeq: card.ExSeq,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Burn != out[j].Burn {
			return out[i].Burn > out[j].Burn
		}
		return out[i].Count > out[j].Count
	})
	return out
}

// measure re-matches the motif across every stream and returns total
// occurrences, distinct streams, and summed member-byte cost. Matching is
// greedy and non-overlapping: once a motif completes, the scan resumes past
// its end, so a stream of N back-to-back routines counts as N, not N-choose-k.
func measure(c *Corpus, ids []uint32, maxGap int) (count, sessions, burnBytes int) {
	for _, st := range c.Streams {
		hits := 0
		i := 0
		for i < len(st) {
			end, bytes, ok := matchAt(st, i, ids, maxGap)
			if !ok {
				i++
				continue
			}
			hits++
			burnBytes += bytes
			i = end + 1
		}
		if hits > 0 {
			count += hits
			sessions++
		}
	}
	return count, sessions, burnBytes
}

// matchAt greedily matches ids[0] at st[start], then each subsequent id within
// maxGap positions of the prior match. Returns the end position and summed
// member bytes on success.
func matchAt(st []Tok, start int, ids []uint32, maxGap int) (end, bytes int, ok bool) {
	if len(ids) == 0 || start >= len(st) || st[start].ID != ids[0] {
		return 0, 0, false
	}
	pos := start
	bytes = st[start].Bytes
	for k := 1; k < len(ids); k++ {
		next := -1
		for p := pos + 1; p <= pos+maxGap && p < len(st); p++ {
			if st[p].ID == ids[k] {
				next = p
				break
			}
		}
		if next < 0 {
			return 0, 0, false
		}
		bytes += st[next].Bytes
		pos = next
	}
	return pos, bytes, true
}

// motifSurprise returns the mean surprise (bits/tok) of the scored streams the
// motif occurs in, and whether any such stream existed. Streams absent from the
// index (too short to score, or no surprise computed) don't contribute; a motif
// seen only in unscored streams yields ok=false, so the caller keeps its base
// kind. A nil index always yields ok=false — the split is off.
func motifSurprise(c *Corpus, ids []uint32, maxGap int, idx map[string]float64) (float64, bool) {
	if idx == nil {
		return 0, false
	}
	sum, n := 0.0, 0
	for si, st := range c.Streams {
		if !containsMotif(st, ids, maxGap) {
			continue
		}
		if bits, ok := idx[c.StreamKeys[si]]; ok {
			sum += bits
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// containsMotif reports whether the motif matches anywhere in the stream, under
// the same gapped greedy match the burn measurement uses.
func containsMotif(st []Tok, ids []uint32, maxGap int) bool {
	for i := range st {
		if _, _, ok := matchAt(st, i, ids, maxGap); ok {
			return true
		}
	}
	return false
}

// failRate is the share of the motif's member tokens carrying a fail mark —
// near 1 for a friction motif, 0 for a clean routine.
func failRate(c *Corpus, ids []uint32) float64 {
	if len(ids) == 0 {
		return 0
	}
	fails := 0
	for _, id := range ids {
		if failMarked(c.Vocab[id]) {
			fails++
		}
	}
	return float64(fails) / float64(len(ids))
}
