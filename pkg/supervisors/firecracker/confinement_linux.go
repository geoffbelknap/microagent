package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
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

// jailArtifact is one file/socket placed inside a confined workspace's jail.
type jailArtifact struct {
	ID     string // identifier (drive id for extra disks; empty otherwise)
	Source string // original host path to stage from (empty for in-jail-created sockets/config)
	Host   string // host path of the artifact under the jail root
	Guest  string // path the jailed Firecracker process sees (absolute, rooted at the jail)
}

// jailLayout is the pure derivation of where a confined workspace's artifacts
// live on the host (under the per-VM jail root) and the paths the jailed
// Firecracker process sees after pivot_root into the jail root. Staging the
// artifacts (hard-link/bind-mount) happens elsewhere; this is only the map.
type jailLayout struct {
	Root       string
	Kernel     jailArtifact
	Rootfs     jailArtifact
	Disks      []jailArtifact
	ConfigFile jailArtifact
	APISocket  jailArtifact
	VsockUDS   jailArtifact
}

// confinedJailLayout derives the jail layout for a workspace. Pure: it reads
// only opts (StateDir/Name) and cfg (kernel/rootfs/disk source paths) and
// computes deterministic jail-relative guest paths. No filesystem effects.
func confinedJailLayout(opts Options, cfg *vmkit.Config) jailLayout {
	root := filepath.Join(opts.StateDir, opts.Name, "jail")
	mk := func(id, source, rel string) jailArtifact {
		return jailArtifact{ID: id, Source: source, Host: filepath.Join(root, rel), Guest: "/" + rel}
	}
	l := jailLayout{
		Root:       root,
		Kernel:     mk("", cfg.KernelPath, "kernel"),
		Rootfs:     mk("rootfs", cfg.RootfsPath, "rootfs.ext4"),
		ConfigFile: mk("", "", "firecracker.json"),
		APISocket:  mk("", "", "run/firecracker-api.sock"),
		VsockUDS:   mk("", "", "run/vsock.sock"),
	}
	for _, d := range cfg.Disks {
		l.Disks = append(l.Disks, mk(d.Name, d.Path, "disks/"+d.Name))
	}
	return l
}
