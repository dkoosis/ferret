package event

import "testing"

// TestReadDecodesPreErrCaptureRow_As_EmptyErr_NotErrorOut is the bead's named
// AC test (ferret-54q): a corpus row written before Event.Err existed carries
// no "err" key at all. SchemaVersion was deliberately NOT bumped for this
// field (the d01 way — see Event.Err's doc comment), so such a row must still
// decode cleanly, with Err reading as "" — the caller's job (mine.MineReasons)
// is what turns that into "not captured", not this layer.
func TestReadDecodesPreErrCaptureRow_As_EmptyErr_NotErrorOut(t *testing.T) {
	body := `{"i":1,"p":"proj","s":"sess","k":"tool","act":"Edit","st":"fail"}` + "\n"
	path := writeArtifact(t, body)

	var got []*Event
	if err := Read(path, func(ev *Event) error {
		cp := *ev
		got = append(got, &cp)
		return nil
	}); err != nil {
		t.Fatalf("Read returned error on a pre-ferret-54q row with no err field: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if got[0].Status != StatusFail {
		t.Fatalf("Status = %q, want %q", got[0].Status, StatusFail)
	}
	if got[0].Err != "" {
		t.Errorf("Err = %q, want \"\" (field absent in the source row)", got[0].Err)
	}
}
