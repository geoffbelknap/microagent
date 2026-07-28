package firecracker

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// SELinux signals used to explain a pasta failure that looks like policy
// confinement. They never decide anything on their own: a host can be
// enforcing with pasta labeled pasta_exec_t and still work (a per-domain
// permissive marking, which these reads cannot see), so callers consult them
// only after pasta has actually failed with a permission error — the failure
// is the evidence, this is the explanation.
var (
	// selinuxEnforcePath is the kernel's current-mode file; overridden in
	// tests.
	selinuxEnforcePath = "/sys/fs/selinux/enforce"
	// lookupPastaLabel resolves pasta's SELinux label; overridden in tests.
	lookupPastaLabel = pastaSELinuxLabel
)

// SELinuxConfinedPastaDetail reports whether this host both enforces SELinux
// and labels the pasta binary with the confined pasta domain's entrypoint
// type, plus a human-readable detail line for error messages.
func SELinuxConfinedPastaDetail() (bool, string) {
	if !selinuxEnforcing() {
		return false, ""
	}
	label := lookupPastaLabel()
	if !strings.Contains(label, "pasta_exec_t") {
		return false, ""
	}
	return true, "pasta labeled " + label + ", SELinux enforcing"
}

func selinuxEnforcing() bool {
	data, err := os.ReadFile(selinuxEnforcePath)
	return err == nil && strings.TrimSpace(string(data)) == "1"
}

// pastaSELinuxLabel reads the security.selinux xattr off the pasta binary the
// PATH resolves, empty on any failure (no pasta, no xattr, no SELinux).
func pastaSELinuxLabel() string {
	path, err := exec.LookPath("pasta")
	if err != nil {
		return ""
	}
	buf := make([]byte, 256)
	n, err := syscall.Getxattr(path, "security.selinux", buf)
	if err != nil || n <= 0 {
		return ""
	}
	return strings.TrimRight(string(buf[:n]), "\x00")
}
