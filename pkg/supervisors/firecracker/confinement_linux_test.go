package firecracker

import "testing"

func TestNormalizeConfinementKnob(t *testing.T) {
	cases := map[string]string{
		"":         confinementAuto,
		"   ":      confinementAuto,
		"AUTO":     confinementAuto,
		"auto":     confinementAuto,
		"off":      confinementOffKnob,
		" Off ":    confinementOffKnob,
		"Jailer":   confinementJailerKnob,
		"rootless": confinementRootlessKnob,
		"nonsense": confinementAuto,
	}
	for in, want := range cases {
		if got := normalizeConfinementKnob(in); got != want {
			t.Errorf("normalizeConfinementKnob(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectConfinementMode(t *testing.T) {
	cases := []struct {
		name          string
		knob          string
		euid          int
		userNSEnabled bool
		want          confinementMode
		wantErr       bool
	}{
		{"off is always off", confinementOffKnob, 0, true, confinementOff, false},
		{"jailer as root", confinementJailerKnob, 0, false, confinementJailer, false},
		{"jailer non-root fails closed", confinementJailerKnob, 1000, true, confinementOff, true},
		{"rootless with userns", confinementRootlessKnob, 1000, true, confinementRootless, false},
		{"rootless without userns fails closed", confinementRootlessKnob, 1000, false, confinementOff, true},
		{"auto+root -> jailer", confinementAuto, 0, false, confinementJailer, false},
		{"auto+userns -> rootless", confinementAuto, 1000, true, confinementRootless, false},
		{"auto, neither -> off", confinementAuto, 1000, false, confinementOff, false},
		{"unknown fails closed", "bogus", 0, true, confinementOff, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectConfinementMode(tc.knob, tc.euid, tc.userNSEnabled)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}
