package mine

import "testing"

// ferret-z35: a calibrated at:* row ranks by the bytes the model sees, so a
// hook whose records never reach a request drops below a tool row it used to
// out-rank on record bytes. An uncalibrated row keeps its record-byte rank.
//
//	at:hook_success     4000B record, 0B visible, 300B uncalibrated → ranks at 300
//	Read                1000B
//	at:nested_memory     600B record, not calibrated               → ranks at 600
//	at:skill_listing     500B record, 400B visible                 → ranks at 400
func TestBurn_RanksAttachRowsByVisibleBytes_When_Calibrated(t *testing.T) {
	lines := `{"i":1,"p":"proj","s":"s1","k":"attach","act":"hook_success","tgt":"PreToolUse","b":3700,"cal":true}
{"i":2,"p":"proj","s":"s1","k":"attach","act":"hook_success","tgt":"Stop","b":300}
{"i":3,"p":"proj","s":"s1","k":"tool","act":"Read","b":1000}
{"i":4,"p":"proj","s":"s1","k":"attach","act":"nested_memory","b":600}
{"i":5,"p":"proj","s":"s1","k":"attach","act":"skill_listing","b":500,"vb":400,"cal":true}
`
	res, err := Burn(writeBurnFixture(t, lines))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Read", "at:nested_memory", "at:skill_listing", "at:hook_success"}
	if len(res.Rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(res.Rows), len(want))
	}
	for i, k := range want {
		if res.Rows[i].Key != k {
			t.Fatalf("Rows[%d] = %q, want %q (order %v)", i, res.Rows[i].Key, k, want)
		}
	}

	hook := findBurnRow(t, res, "at:hook_success")
	if hook.Pricing != PricingCalibrated || hook.VisibleTokens == nil || *hook.VisibleTokens != 0 || hook.UncalibratedBytes != 300 {
		t.Errorf("hook_success: pricing=%q tokens=%v uncalibrated=%d, want calibrated, 0, 300", hook.Pricing, hook.VisibleTokens, hook.UncalibratedBytes)
	}
	if skill := findBurnRow(t, res, "at:skill_listing"); skill.VisibleTokens == nil || *skill.VisibleTokens != 400/BytesPerToken {
		t.Errorf("skill_listing tokens = %v, want %d", skill.VisibleTokens, 400/BytesPerToken)
	}
	if nm := findBurnRow(t, res, "at:nested_memory"); nm.Pricing != PricingUncalibrated || nm.VisibleTokens != nil {
		t.Errorf("nested_memory: pricing=%q tokens=%v, want not calibrated and no number", nm.Pricing, nm.VisibleTokens)
	}
	if read := findBurnRow(t, res, "Read"); read.Pricing != "" || read.VisibleTokens != nil {
		t.Errorf("Read row carries attachment pricing: %q %v", read.Pricing, read.VisibleTokens)
	}
}
