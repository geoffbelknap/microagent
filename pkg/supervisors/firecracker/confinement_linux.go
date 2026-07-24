package firecracker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/confine"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"golang.org/x/sys/unix"
)

// confinementEnv is the operator knob selecting VMM-process confinement for the
// Firecracker backend. Values: "auto" (default; strongest mode the host
// supports, falling back to off), "jailer", "rootless", "off" (opt out).
// Confinement posture constants and the pure knob/mode decision live in the
// dependency-free pkg/confine leaf so the supervisor (which enforces the mode)
// and `microagent doctor` (which reports it) can never drift. These aliases
// keep the existing supervisor call sites unchanged.
const confinementEnv = confine.EnvVar

const (
	confinementAuto         = confine.KnobAuto
	confinementOffKnob      = confine.KnobOff
	confinementJailerKnob   = confine.KnobJailer
	confinementRootlessKnob = confine.KnobRootless
)

type confinementMode = confine.Mode

const (
	confinementOff      = confine.ModeOff
	confinementJailer   = confine.ModeJailer
	confinementRootless = confine.ModeRootless
)

// resolveConfinementKnob reads MICROAGENT_CONFINEMENT and normalizes it,
// defaulting to "auto" (on-by-default; strongest mode the host supports,
// falling back to off) when unset.
func resolveConfinementKnob() string {
	return confine.NormalizeKnob(os.Getenv(confinementEnv))
}

func normalizeConfinementKnob(v string) string { return confine.NormalizeKnob(v) }

func selectConfinementMode(knob string, euid int, userNSEnabled bool) (confinementMode, error) {
	return confine.SelectMode(knob, euid, userNSEnabled)
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
// rootfs persist), removing any stale dst first. A hard link can be rejected for
// a file on another filesystem (EXDEV) or a protected hard link to a file we
// don't own (EPERM under fs.protected_hardlinks — e.g. a root-owned firecracker
// binary); in either case it copies instead. A host-side bind mount is not an
// option: staging runs before the user namespace, so a rootless launcher has no
// privilege to mount.
func stageFile(src, dst string) error {
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}
	_ = os.Remove(dst)
	err := os.Link(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EPERM) {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	// copyFile creates dst with default perms; preserve the source mode so an
	// executable (the firecracker binary) stays executable inside the jail.
	if info, statErr := os.Stat(src); statErr == nil {
		return os.Chmod(dst, info.Mode().Perm())
	}
	return nil
}

// resolveConfinementMode resolves the effective confinement mode for this host
// from the (normalized) knob, the effective uid, and whether the rootless user
// namespace jail actually works here. It fails closed for an explicitly
// requested mode the host cannot satisfy. The jail probe only runs when the
// decision depends on it, so a root (jailer) or opted-out launch pays nothing.
func resolveConfinementMode(opts Options) (confinementMode, error) {
	knob := normalizeConfinementKnob(opts.Confinement)
	euid := os.Geteuid()
	userNSOK := false
	if knob == confinementRootlessKnob || (knob == confinementAuto && euid != 0) {
		userNSOK = userNamespaceJailUsable()
	}
	return selectConfinementMode(knob, euid, userNSOK)
}

// userNamespaceJailUsable reports whether the rootless jail can actually be
// built on this host. The live self-map probe is authoritative when it can
// run: it exercises the exact unshare invocation the jail uses, so policy
// layers that allow namespace creation but deny the uid_map self-write (Ubuntu
// 24.04's kernel.apparmor_restrict_unprivileged_userns=1 default) are caught,
// and a targeted AppArmor profile that re-enables the jail is honored. Only
// when the probe cannot run at all does the sysctl gate decide, preserving the
// prior behavior on hosts without the probe's helper binaries.
func userNamespaceJailUsable() bool {
	err := ProbeSelfMapUserNamespace()
	if err == nil {
		return true
	}
	if errors.Is(err, ErrUserNSProbeUnavailable) {
		enabled, _ := unprivilegedUserNSEnabled()
		return enabled
	}
	return false
}

// unshareJailNamespaceFlags returns the namespace flags passed to the unshare
// binary for the rootless jail: a new mount namespace, plus --map-root-user (a
// new user namespace whose root map unshare writes to its own
// /proc/self/uid_map) unless the process is already root inside pasta's user
// namespace. ProbeSelfMapUserNamespace must use exactly these flags so it
// exercises the same kernel and LSM path the launch does.
func unshareJailNamespaceFlags(mapRoot bool) []string {
	flags := []string{}
	if mapRoot {
		flags = append(flags, "--map-root-user")
	}
	return append(flags, "--mount")
}

// ErrUserNSProbeUnavailable reports that the self-map probe could not run at
// all (no unshare or no-op helper binary on this host), as opposed to running
// and being denied. Callers fall back to the sysctl gates in that case.
var ErrUserNSProbeUnavailable = errors.New("user namespace self-map probe unavailable")

// ProbeSelfMapUserNamespace verifies that this user can enter a new user +
// mount namespace and self-map root exactly the way the rootless jail (and
// pasta) do: it runs the unshare binary with the jail's namespace flags and a
// no-op payload, so unshare itself — already confined by any LSM transition the
// namespace creation triggered — performs the /proc/self/uid_map write. A
// Go-native CLONE_NEWUSER probe with parent-written uid maps is not
// equivalent: Ubuntu 24.04's kernel.apparmor_restrict_unprivileged_userns=1
// default allows namespace creation and the unconfined parent's map write,
// while the confined child's own write fails with "unshare: write failed
// /proc/self/uid_map: Operation not permitted" — exactly how the jail dies.
func ProbeSelfMapUserNamespace() error {
	unsharePath, err := exec.LookPath("unshare")
	if err != nil {
		return fmt.Errorf("%w: unshare binary not found (util-linux)", ErrUserNSProbeUnavailable)
	}
	helper, err := lookupNoopHelper()
	if err != nil {
		return fmt.Errorf("%w: no no-op helper binary (true) found", ErrUserNSProbeUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, unsharePath, append(unshareJailNamespaceFlags(true), helper)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("unshare --map-root-user --mount failed: %s", message)
	}
	return nil
}

func lookupNoopHelper() (string, error) {
	if path, err := exec.LookPath("true"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/usr/bin/true", "/bin/true"} {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

// confinedExecArgs builds the argv passed to the unshare binary that launches
// the supervisor's --confined-exec handler in a fresh user (when mapRoot) +
// mount namespace; the handler sets up the jail and execs Firecracker. mapRoot
// adds --map-root-user (skipped when already inside pasta's user namespace, so
// we don't shadow its network setup). Mirrors forkMountExecArgs.
func confinedExecArgs(mapRoot bool, supervisor, jailRoot, workDir, firecracker string, launchArgs []string) []string {
	args := unshareJailNamespaceFlags(mapRoot)
	args = append(args,
		supervisor, "--confined-exec",
		"--jail-root", jailRoot,
		"--work-dir", workDir,
		"--", firecracker,
	)
	return append(args, launchArgs...)
}

// confinedExecDevices are the device nodes bound into the jail. The guest reaches
// the host only through the mediated vsock + the (separately confined) network
// device; KVM/tun/urandom are the minimum the VMM itself needs.
var confinedExecDevices = []string{"/dev/kvm", "/dev/net/tun", "/dev/urandom"}

// RunConfinedExec is the re-exec entry launched via unshare inside a new
// user+mount namespace. It shares the workspace directory into the jail at /run
// (so the host supervisor and the jailed Firecracker see the same live API/vsock
// sockets), binds the device nodes Firecracker needs, pivot_roots into the jail,
// sets no-new-privs, then execs Firecracker. The user namespace itself
// deprivileges the process — root-in-userns maps to the unprivileged invoker on
// the host — so a VMM escape lands outside the jail with no host privileges.
func RunConfinedExec(args []string) error {
	jailRoot, workDir := "", ""
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--jail-root":
			if i+1 >= len(args) {
				return fmt.Errorf("--jail-root requires a value")
			}
			jailRoot = args[i+1]
			i += 2
		case "--work-dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--work-dir requires a value")
			}
			workDir = args[i+1]
			i += 2
		case "--":
			i++
			goto run
		default:
			return fmt.Errorf("unexpected confined-exec argument %q", args[i])
		}
	}
run:
	rest := args[i:]
	if jailRoot == "" || workDir == "" || len(rest) == 0 {
		return fmt.Errorf("usage: --confined-exec --jail-root <dir> --work-dir <dir> -- <firecracker> [args...]")
	}

	// Keep our mounts from propagating back to the host mount namespace.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	// Socket coherence: bind the workspace dir into the jail at /run so the live
	// API/vsock sockets Firecracker creates are the same files the host
	// supervisor connects to (the one bind that's genuinely required).
	runDir := filepath.Join(jailRoot, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return fmt.Errorf("mkdir jail /run: %w", err)
	}
	if err := unix.Mount(workDir, runDir, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind workspace dir into jail /run: %w", err)
	}
	// Bind the device nodes the VMM needs (rootless can't mknod).
	for _, dev := range confinedExecDevices {
		dst := filepath.Join(jailRoot, dev)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", dev, err)
		}
		if f, err := os.OpenFile(dst, os.O_CREATE, 0o600); err == nil {
			_ = f.Close()
		}
		if err := unix.Mount(dev, dst, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s into jail: %w", dev, err)
		}
	}
	// pivot_root requires the new root to be a mount point.
	if err := unix.Mount(jailRoot, jailRoot, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind jail root onto itself: %w", err)
	}
	oldRoot := filepath.Join(jailRoot, ".oldroot")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		return fmt.Errorf("mkdir put_old: %w", err)
	}
	if err := unix.PivotRoot(jailRoot, oldRoot); err != nil {
		return fmt.Errorf("pivot_root into jail: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir to new root: %w", err)
	}
	if err := unix.Unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	_ = os.Remove("/.oldroot")
	// No new privileges for the VMM or anything it spawns.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no-new-privs: %w", err)
	}
	if err := unix.Exec(rest[0], rest, os.Environ()); err != nil {
		return fmt.Errorf("exec firecracker in jail: %w", err)
	}
	return nil
}
