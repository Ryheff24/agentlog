package transcript

import "testing"

func TestSkipsOrdersUnknownFirst(t *testing.T) {
	tr := &Transcript{}
	tr.Ignore("mode")
	tr.Ignore("mode")
	tr.Ignore("cost-state")
	tr.Unknown("brand-new")
	tr.Unknown("brand-new")
	tr.Unknown("other-new")

	var got []string
	for _, s := range tr.Skips() {
		got = append(got, s.Type)
	}
	// counts descending; at equal counts an unknown type sorts first because
	// it is the one worth acting on.
	want := []string{"brand-new", "mode", "other-new", "cost-state"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("Skips() = %v, want %v", got, want)
		}
	}
	if tr.Ignored["mode"] != 2 || tr.Unknowns["brand-new"] != 2 {
		t.Errorf("counts = ignored %v unknown %v", tr.Ignored, tr.Unknowns)
	}
}
