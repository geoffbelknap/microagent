package firecracker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"golang.org/x/sys/unix"
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
	Root        string
	Kernel      jailArtifact
	Rootfs      jailArtifact
	Firecracker jailArtifact
	Disks       []jailArtifact
	ConfigFile  jailArtifact
	APISocket   jailArtifact
	VsockUDS    jailArtifact
}

// confinedJailLayout derives the jail layout for a workspace. Pure: it reads
// only opts (StateDir/Name) and cfg (kernel/rootfs/disk source paths) and
// computes deterministic jail-relative guest paths. No filesystem effects.
func confinedJailLayout(opts Options, cfg *vmkit.Config, firecrackerPath string) jailLayout {
	root := filepath.Join(opts.StateDir, opts.Name, "jail")
	mk := func(id, source, rel string) jailArtifact {
		return jailArtifact{ID: id, Source: source, Host: filepath.Join(root, rel), Guest: "/" + rel}
	}
	l := jailLayout{
		Root:        root,
		Kernel:      mk("", cfg.KernelPath, "kernel"),
		Rootfs:      mk("rootfs", cfg.RootfsPath, "rootfs.ext4"),
		Firecracker: mk("", firecrackerPath, "firecracker"),
		ConfigFile:  mk("", "", "firecracker.json"),
		APISocket:   mk("", "", "run/firecracker-api.sock"),
		VsockUDS:    mk("", "", "run/vsock.sock"),
	}
	for _, d := range cfg.Disks {
		l.Disks = append(l.Disks, mk(d.Name, d.Path, "disks/"+d.Name))
	}
	return l
}

// stageJailArtifacts creates the jail directory tree and stages the
// source-backed artifacts (kernel, rootfs, disks) into it. Sockets and the
// config file have no source — only their parent directories are created here;
// they are created/written in place later. Device nodes are bound inside the
// launch namespace, not here.
func stageJailArtifacts(l jailLayout) error {
	for _, dir := range []string{l.Root, filepath.Join(l.Root, "dev"), filepath.Dir(l.APISocket.Host), filepath.Dir(l.VsockUDS.Host)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("jail mkdir %s: %w", dir, err)
		}
	}
	sourced := append([]jailArtifact{l.Kernel, l.Rootfs, l.Firecracker}, l.Disks...)
	for _, a := range sourced {
		if a.Source == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(a.Host), 0o700); err != nil {
			return fmt.Errorf("jail mkdir %s: %w", filepath.Dir(a.Host), err)
		}
		if err := stageFile(a.Source, a.Host); err != nil {
			return fmt.Errorf("stage %s -> %s: %w", a.Source, a.Host, err)
		}
	}
	return nil
}

// stageFile hard-links src to dst (cheap, same-inode so guest writes to the
// rootfs persist), removing any stale dst first. On a cross-device link error
// it falls back to a read-write bind mount.
func stageFile(src, dst string) error {
	_ = os.Remove(dst)
	err := os.Link(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) {
		return err
	}
	// Different filesystem: a hard link is impossible, so bind-mount instead.
	f, cerr := os.Create(dst)
	if cerr != nil {
		return cerr
	}
	_ = f.Close()
	return unix.Mount(src, dst, "", unix.MS_BIND, "")
}

// resolveConfinementMode resolves the effective confinement mode for this host
// from the (normalized) knob, the effective uid, and whether unprivileged user
// namespaces are available. It fails closed for an explicitly requested mode
// the host cannot satisfy.
func resolveConfinementMode(opts Options) (confinementMode, error) {
	userNSOK, _ := unprivilegedUserNSEnabled()
	return selectConfinementMode(normalizeConfinementKnob(opts.Confinement), os.Geteuid(), userNSOK)
}
