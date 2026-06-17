package conform

import "testing"

// obs is a terse ObsCall builder for tests: index implied by position is not
// load-bearing here, so callers pass call index + step explicitly.
func obs(call int, step string) ObsCall { return ObsCall{Call: call, Tool: "Bash", Step: step} }

// countMoves tallies the three move types in an alignment.
func countMoves(moves []Move) (sync, model, log int) {
	for _, mv := range moves {
		switch mv.Type {
		case MoveSync:
			sync++
		case MoveModel:
			model++
		case MoveLog:
			log++
		}
	}
	return
}

func TestAlign(t *testing.T) {
	tests := []struct {
		name                    string
		reference               []string
		observed                []ObsCall
		wantSync, wantModel     int
		wantLog                 int
		wantFitness, wantPrecis float64
	}{
		{
			name:      "perfect replay scores 1/1",
			reference: []string{"list", "review", "merge"},
			observed:  []ObsCall{obs(0, "list"), obs(1, "review"), obs(2, "merge")},
			wantSync:  3, wantFitness: 1, wantPrecis: 1,
		},
		{
			name:      "skipped step is a model move",
			reference: []string{"list", "review", "wait-green", "merge"},
			observed:  []ObsCall{obs(0, "list"), obs(1, "review"), obs(2, "merge")},
			wantSync:  3, wantModel: 1,
			// cost 1, worst 4+3=7 → 1-1/7
			wantFitness: 1 - 1.0/7, wantPrecis: 1,
		},
		{
			name:      "off-plan call is a log move",
			reference: []string{"list", "merge"},
			observed:  []ObsCall{obs(0, "list"), obs(1, ""), obs(2, "merge")},
			wantSync:  2, wantLog: 1,
			// cost 1, worst 2+3=5
			wantFitness: 1 - 1.0/5, wantPrecis: 2.0 / 3,
		},
		{
			name:      "label mismatch costs a model + a log move",
			reference: []string{"a", "b"},
			observed:  []ObsCall{obs(0, "a"), obs(1, "x")},
			wantSync:  1, wantModel: 1, wantLog: 1,
			// cost 2, worst 4
			wantFitness: 0.5, wantPrecis: 0.5,
		},
		{
			name:      "empty observed: all steps skipped, precision defined as 1",
			reference: []string{"a", "b"},
			observed:  nil,
			wantModel: 2,
			// cost 2, worst 2 → 0
			wantFitness: 0, wantPrecis: 1,
		},
		{
			name:      "all off-plan: nothing served the single-step plan",
			reference: []string{"a"},
			observed:  []ObsCall{obs(0, ""), obs(1, "")},
			wantModel: 1, wantLog: 2,
			// cost 3, worst 1+2=3 → 0
			wantFitness: 0, wantPrecis: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Align(tt.reference, tt.observed)
			sync, model, log := countMoves(res.Moves)
			if sync != tt.wantSync || model != tt.wantModel || log != tt.wantLog {
				t.Errorf("moves: sync=%d model=%d log=%d; want %d/%d/%d",
					sync, model, log, tt.wantSync, tt.wantModel, tt.wantLog)
			}
			if sync != res.Sync || model != res.ModelMoves || log != res.LogMoves {
				t.Errorf("move counts disagree with tally: res=%d/%d/%d moves=%d/%d/%d",
					res.Sync, res.ModelMoves, res.LogMoves, sync, model, log)
			}
			if !approx(res.Fitness, tt.wantFitness) {
				t.Errorf("fitness=%.4f; want %.4f", res.Fitness, tt.wantFitness)
			}
			if !approx(res.Precision, tt.wantPrecis) {
				t.Errorf("precision=%.4f; want %.4f", res.Precision, tt.wantPrecis)
			}
		})
	}
}

// TestAlignDeterministic guards the determinism contract: the same inputs must
// produce byte-identical move sequences across runs (no map iteration, no time).
func TestAlignDeterministic(t *testing.T) {
	ref := []string{"list", "review", "undraft", "wait-green", "merge", "cleanup"}
	ob := []ObsCall{obs(0, "list"), obs(1, "review"), obs(2, "review"), obs(3, "undraft"),
		obs(4, ""), obs(5, "merge"), obs(6, "cleanup")}
	first := Align(ref, ob)
	for i := range 5 {
		got := Align(ref, ob)
		if len(got.Moves) != len(first.Moves) {
			t.Fatalf("run %d: move count %d != %d", i, len(got.Moves), len(first.Moves))
		}
		for k := range got.Moves {
			if got.Moves[k] != first.Moves[k] {
				// Move holds a pointer; compare by value fields instead.
				a, b := got.Moves[k], first.Moves[k]
				if a.Type != b.Type || a.Step != b.Step {
					t.Fatalf("run %d move %d: %+v != %+v", i, k, a, b)
				}
			}
		}
	}
}

// TestAlignLocalizesSkippedGate is the load-bearing case from the validated
// slice (loto/6ccedb07): the agent stated a wait-green gate, then merged without
// it. The alignment must localize that as a model move on "wait-green" while
// every other step syncs in order.
func TestAlignLocalizesSkippedGate(t *testing.T) {
	ref := []string{"list", "review", "undraft", "wait-green", "merge", "cleanup"}
	ob := []ObsCall{obs(0, "list"), obs(1, "review"), obs(2, "undraft"),
		obs(30, "merge"), obs(33, "cleanup")}
	res := Align(ref, ob)

	var skipped []string
	for _, mv := range res.Moves {
		if mv.Type == MoveModel {
			skipped = append(skipped, mv.Step)
		}
	}
	if len(skipped) != 1 || skipped[0] != "wait-green" {
		t.Fatalf("expected exactly the wait-green step skipped, got %v", skipped)
	}
	if res.Sync != 5 {
		t.Errorf("sync=%d; want 5 (every other step in order)", res.Sync)
	}
}

func approx(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
