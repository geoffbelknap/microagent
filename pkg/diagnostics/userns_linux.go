package diagnostics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"

	firecracker "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
)

// defaultUserNamespaceProbe is the live probe CheckFirecracker uses when the
// caller does not inject one.
var defaultUserNamespaceProbe = ProbeUnprivilegedUserNamespace

// ProbeUnprivilegedUserNamespace verifies that the current user can actually
// use an unprivileged user namespace the way the Firecracker supervisor does:
// the authoritative check is the supervisor's own self-map probe, which runs
// the exact unshare invocation the rootless jail uses (and pasta mirrors),
// where the confined child writes its own /proc/self/uid_map. That is the
// write Ubuntu 24.04's kernel.apparmor_restrict_unprivileged_userns=1 default
// denies even though plain namespace creation succeeds — so a
// namespace-creation-only probe reports a green host that cannot boot
// workspaces. Only when the self-map probe cannot run at all (no unshare
// binary) does this fall back to a Go-native CLONE_NEWUSER probe with
// parent-written uid maps, which still catches hosts where user namespaces
// are disabled outright.
func ProbeUnprivilegedUserNamespace() error {
	err := firecracker.ProbeSelfMapUserNamespace()
	if errors.Is(err, firecracker.ErrUserNSProbeUnavailable) {
		return probeParentMappedUserNamespace()
	}
	return err
}

// probeParentMappedUserNamespace is the weaker fallback probe: it clones a
// child with CLONE_NEWUSER and maps the current uid/gid to root inside it via
// parent-written uid/gid maps. It detects hosts where user namespace creation
// is denied outright, but NOT policy layers that only deny the confined
// child's own uid_map write (the AppArmor restriction), because the parent
// performing the write here is not confined.
func probeParentMappedUserNamespace() error {
	helper, err := lookupNoopHelper()
	if err != nil {
		// Without a helper binary the probe cannot run; leave the verdict to
		// the sysctl checks rather than reporting a false negative.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The namespace and uid/gid maps were set up and the helper ran;
			// its exit status does not matter for the probe.
			return nil
		}
		return err
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
