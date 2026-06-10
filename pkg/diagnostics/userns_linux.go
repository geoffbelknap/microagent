package diagnostics

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// defaultUserNamespaceProbe is the live probe CheckFirecracker uses when the
// caller does not inject one.
var defaultUserNamespaceProbe = ProbeUnprivilegedUserNamespace

// ProbeUnprivilegedUserNamespace verifies that the current user can actually
// create a user namespace by cloning a child with CLONE_NEWUSER and mapping
// the current uid/gid to root inside it — the same setup pasta performs for
// Firecracker user-mode networking. Reading sysctls is not enough: policy
// layers such as AppArmor (kernel.apparmor_restrict_unprivileged_userns) deny
// the clone at runtime while the classic userns sysctls report it enabled.
func ProbeUnprivilegedUserNamespace() error {
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
