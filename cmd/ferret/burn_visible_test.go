package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dkoosis/ferret/internal/mine"
)

func pricedBurnResult() *mine.BurnResult {
	zero, visible := 0, 1500000
	return &mine.BurnResult{
		Events: 3000, Sessions: 40,
		Rows: []mine.BurnRow{
			{Key: "Read", Bytes: 9000000, Calls: 1000, BytesPerCall: 9000, Sessions: 40},
			{Key: "at:skill_listing", Bytes: 7000000, Calls: 500, BytesPerCall: 14000, Sessions: 40,
				VisibleTokens: &visible, Pricing: mine.PricingCalibrated},
			{Key: "at:hook_success", Bytes: 18600000, Calls: 1200, BytesPerCall: 15500, Sessions: 40,
				VisibleTokens: &zero, UncalibratedBytes: 40000, Pricing: mine.PricingCalibrated},
			{Key: "at:nested_memory", Bytes: 2000000, Calls: 40, BytesPerCall: 50000, Sessions: 40,
				Overcount: true, Pricing: mine.PricingUncalibrated},
		},
	}
}

// ferret-z35: every at:* row shows its visible tokens beside its record bytes,
// or says it is not calibrated. A priced zero prints as a number.
func TestWriteBurnText_ShowsVisibleTokens_When_AttachRowIsCalibrated(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnText(&buf, pricedBurnResult(), 0, 0); err != nil {
		t.Fatalf("writeBurnText: %v", err)
	}
	rowsOut := burnRowsOut(t, buf.String())
	for key, want := range map[string]string{
		"at:skill_listing": "tok visible",
		"at:hook_success":  "~0 tok visible + ",
		"at:nested_memory": "not calibrated",
	} {
		line := ""
		for l := range strings.SplitSeq(rowsOut, "\n") {
			if strings.Contains(l, key) {
				line = l
			}
		}
		if !strings.Contains(line, want) || !strings.Contains(line, "bytes") {
			t.Errorf("%s row = %q, want %q beside its record bytes", key, line, want)
		}
	}
}

func TestWriteBurnJSON_CarriesVisibleTokensAndBytes_When_AttachRowIsCalibrated(t *testing.T) {
	var buf bytes.Buffer
	if err := writeBurnJSON(&buf, pricedBurnResult(), 0); err != nil {
		t.Fatalf("writeBurnJSON: %v", err)
	}
	var doc struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, row := range doc.Rows {
		key, _ := row["key"].(string)
		if !strings.HasPrefix(key, "at:") {
			continue
		}
		if _, ok := row["bytes"]; !ok {
			t.Errorf("%s: no bytes", key)
		}
		if _, ok := row["pricing"]; !ok {
			t.Errorf("%s: no pricing", key)
		}
		if _, ok := row["visibleTokens"]; row["pricing"] == mine.PricingCalibrated && !ok {
			t.Errorf("%s: calibrated row has no visibleTokens (a priced zero must survive omitempty)", key)
		}
	}
}
