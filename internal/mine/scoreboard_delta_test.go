package mine

import (
	"testing"
	"time"

	"github.com/dkoosis/ferret/internal/fixes"
)

// TestBuildScoreboard_DeltaCountsBelowSupportMotif: a fix recorded for a motif
// that is not a finding (below min-support) carries a counted baseline, so its
// "after" must be counted too. Right after `fixes add` nothing has changed:
// the row reads before == after, not "-100%".
func TestBuildScoreboard_DeltaCountsBelowSupportMotif(t *testing.T) {
	corpus := bytesCorpus([][]tb{
		{{"A", 800}, {"B", 800}},
		{{"A", 800}, {"B", 800}},
	})
	now := CountMotif(corpus, []string{"A", "B"}, ScoreboardMaxGap)
	if now.Burn == 0 {
		t.Fatal("setup: motif must have burn")
	}
	ledger := []fixes.Entry{{
		Motif: fixes.MotifKey([]string{"A", "B"}), Fix: "x", AddedAt: time.Now(),
		BaselineBurn: now.Burn, BaselineSessions: now.Sessions,
	}}
	sb := BuildScoreboard(corpus, nil, WasteReport{}, ledger, 3)
	if len(sb.Delta) != 1 {
		t.Fatalf("delta rows = %d, want 1", len(sb.Delta))
	}
	if d := sb.Delta[0]; d.AfterBytes != d.BeforeBytes || d.AfterBytes == 0 {
		t.Errorf("delta = %+v, want after == before != 0 for an unchanged non-finding", d)
	}
}
