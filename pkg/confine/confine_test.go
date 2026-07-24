package confine

import "testing"

func TestNormalizeKnob(t *testing.T) {
	cases := map[string]string{
		"":         KnobAuto,
		"   ":      KnobAuto,
		"AUTO":     KnobAuto,
		"nonsense": KnobAuto,
		"off":      KnobOff,
		" Off ":    KnobOff,
		"JAILER":   KnobJailer,
		"rootless": KnobRootless,
	}
	for in, want := range cases {
		if got := NormalizeKnob(in); got != want {
			t.Errorf("NormalizeKnob(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectMode(t *testing.T) {
	cases := []struct {
		name      string
		knob      string
		euid      int
		userNS    bool
		want      Mode
		wantError bool
	}{
		{"auto root -> jailer", KnobAuto, 0, false, ModeJailer, false},
		{"auto non-root with userns -> rootless", KnobAuto, 1000, true, ModeRootless, false},
		{"auto non-root without userns -> off", KnobAuto, 1000, false, ModeOff, false},
		{"off is always off", KnobOff, 0, true, ModeOff, false},
		{"jailer non-root fails closed", KnobJailer, 1000, true, ModeOff, true},
		{"jailer root", KnobJailer, 0, false, ModeJailer, false},
		{"rootless without userns fails closed", KnobRootless, 1000, false, ModeOff, true},
		{"rootless with userns", KnobRootless, 1000, true, ModeRootless, false},
		{"unknown knob errors", "bogus", 0, true, ModeOff, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SelectMode(tc.knob, tc.euid, tc.userNS)
			if (err != nil) != tc.wantError {
				t.Fatalf("SelectMode(%q, %d, %v) error = %v, wantError %v", tc.knob, tc.euid, tc.userNS, err, tc.wantError)
			}
			if got != tc.want {
				t.Errorf("SelectMode(%q, %d, %v) = %v, want %v", tc.knob, tc.euid, tc.userNS, got, tc.want)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	cases := map[Mode]string{ModeOff: "off", ModeJailer: "jailer", ModeRootless: "rootless"}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", m, got, want)
		}
	}
}
