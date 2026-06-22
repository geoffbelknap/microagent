package firecracker

import (
	"fmt"
	"os"
	"strings"
)

// confinementEnv is the operator knob selecting VMM-process confinement for the
// Firecracker backend. Values: "auto" (default), "off", "jailer", "rootless".
const confinementEnv = "MICROAGENT_CONFINEMENT"

// Knob string values (the operator-facing names).
const (
	confinementAuto         = "auto"
	confinementOffKnob      = "off"
	confinementJailerKnob   = "jailer"
	confinementRootlessKnob = "rootless"
)

// confinementMode is the resolved confinement strategy for a single launch.
type confinementMode int

const (
	confinementOff confinementMode = iota
	confinementJailer
	confinementRootless
)

func (m confinementMode) String() string {
	switch m {
	case confinementJailer:
		return "jailer"
	case confinementRootless:
		return "rootless"
	default:
		return "off"
	}
}

// resolveConfinementKnob reads MICROAGENT_CONFINEMENT and normalizes it,
// defaulting to "auto" when unset or unrecognized.
func resolveConfinementKnob() string {
	return normalizeConfinementKnob(os.Getenv(confinementEnv))
}

// normalizeConfinementKnob lower-cases/trims the knob and maps anything
// unrecognized (including empty) to "auto" — the safe default that resolves to
// the strongest available mode at launch.
func normalizeConfinementKnob(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case confinementOffKnob:
		return confinementOffKnob
	case confinementJailerKnob:
		return confinementJailerKnob
	case confinementRootlessKnob:
		return confinementRootlessKnob
	default:
		return confinementAuto
	}
}

// selectConfinementMode resolves the effective confinement mode from the
// (normalized) knob and host facts. It fails closed: an explicitly requested
// mode the host cannot satisfy returns an error rather than silently
// downgrading to a weaker posture. "auto" never errors — it picks the strongest
// available mode, falling back to off when the host supports neither.
func selectConfinementMode(knob string, euid int, userNSEnabled bool) (confinementMode, error) {
	switch knob {
	case confinementOffKnob:
		return confinementOff, nil
	case confinementJailerKnob:
		if euid != 0 {
			return confinementOff, fmt.Errorf("confinement %q requires root (euid 0), have euid %d", knob, euid)
		}
		return confinementJailer, nil
	case confinementRootlessKnob:
		if !userNSEnabled {
			return confinementOff, fmt.Errorf("confinement %q requires unprivileged user namespaces, which are disabled on this host", knob)
		}
		return confinementRootless, nil
	case confinementAuto:
		switch {
		case euid == 0:
			return confinementJailer, nil
		case userNSEnabled:
			return confinementRootless, nil
		default:
			return confinementOff, nil
		}
	default:
		return confinementOff, fmt.Errorf("unknown confinement mode %q", knob)
	}
}
