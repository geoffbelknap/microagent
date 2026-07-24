// Package confine holds the pure decision logic for VMM-process confinement
// posture, shared so every caller resolves it identically and cannot drift:
//
//   - the Firecracker supervisor, which enforces the resolved mode for each
//     launch and reports it in its host response;
//   - the diagnostics host probe behind `microagent doctor`, which REPORTS the
//     mode the host will apply.
//
// The package is a dependency-free leaf (no other microagent imports, no build
// tag) so it compiles on every platform. Host-specific probing (reading the
// effective uid, exercising the user-namespace jail) lives in the callers; only
// the pure knob normalization and mode selection live here.
package confine

import (
	"fmt"
	"strings"
)

// EnvVar is the operator knob selecting VMM-process confinement.
const EnvVar = "MICROAGENT_CONFINEMENT"

// Knob values are the operator-facing names accepted in EnvVar.
const (
	KnobAuto     = "auto"
	KnobOff      = "off"
	KnobJailer   = "jailer"
	KnobRootless = "rootless"
)

// Mode is the resolved confinement strategy for a launch.
type Mode int

const (
	ModeOff Mode = iota
	ModeJailer
	ModeRootless
)

func (m Mode) String() string {
	switch m {
	case ModeJailer:
		return "jailer"
	case ModeRootless:
		return "rootless"
	default:
		return "off"
	}
}

// NormalizeKnob lower-cases/trims the knob. Confinement is on-by-default:
// empty/unset (and any unrecognized value) maps to "auto" — the strongest mode
// the host supports, falling back to off where it supports neither a root
// jailer nor rootless user namespaces. Operators opt out explicitly with "off",
// or pin a specific posture with "jailer"/"rootless".
func NormalizeKnob(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case KnobOff:
		return KnobOff
	case KnobJailer:
		return KnobJailer
	case KnobRootless:
		return KnobRootless
	default:
		return KnobAuto
	}
}

// SelectMode resolves the effective confinement mode from the (normalized) knob
// and host facts. It fails closed: an explicitly requested mode the host cannot
// satisfy returns an error rather than silently downgrading to a weaker
// posture. "auto" never errors — it picks the strongest available mode, falling
// back to off when the host supports neither. Callers pass a NormalizeKnob'd
// knob; an un-normalized value returns an "unknown confinement mode" error.
func SelectMode(knob string, euid int, userNSEnabled bool) (Mode, error) {
	switch knob {
	case KnobOff:
		return ModeOff, nil
	case KnobJailer:
		if euid != 0 {
			return ModeOff, fmt.Errorf("confinement %q requires root (euid 0), have euid %d", knob, euid)
		}
		return ModeJailer, nil
	case KnobRootless:
		if !userNSEnabled {
			return ModeOff, fmt.Errorf("confinement %q requires unprivileged user namespaces, which are disabled on this host", knob)
		}
		return ModeRootless, nil
	case KnobAuto:
		switch {
		case euid == 0:
			return ModeJailer, nil
		case userNSEnabled:
			return ModeRootless, nil
		default:
			return ModeOff, nil
		}
	default:
		return ModeOff, fmt.Errorf("unknown confinement mode %q", knob)
	}
}
