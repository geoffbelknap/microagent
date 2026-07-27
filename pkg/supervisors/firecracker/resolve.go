package firecracker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveBinaryFrom locates the firecracker binary exactly the way the
// supervisor launches it: MICROAGENT_FIRECRACKER first, then PATH, then
// ../libexec/firecracker relative to anchor — the supervisor executable.
//
// This is THE resolution, shared by the supervisor's boot path and the
// doctor's probe, because the two drifting is how doctor lied twice. First
// doctor anchored on the CLI binary while boots anchored on the supervisor,
// so split layouts got a false "failed". The repair kept the CLI tree as a
// last resort, which mirrored the bug: firecracker present only in the CLI's
// tree made doctor report a boot path the supervisor cannot see — a false
// "healthy", worse than the false "failed" it replaced. One function, one
// anchor parameter, and the question "could these disagree" stops being a
// matter of reading two files side by side.
//
// An env override that does not resolve, or names a directory, is a
// configuration mistake and fails here with the fix attached — for the boot
// path too, which used to pass the bad value through and fail later at exec
// with a bare errno.
func ResolveBinaryFrom(anchor string) (string, error) {
	if path := strings.TrimSpace(os.Getenv("MICROAGENT_FIRECRACKER")); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("MICROAGENT_FIRECRACKER is not usable: %s; point it at the firecracker binary or unset it to search PATH and the installed supervisor tree", err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("MICROAGENT_FIRECRACKER is not usable: %s is a directory; point it at the firecracker binary or unset it to search PATH and the installed supervisor tree", path)
		}
		return path, nil
	}
	if path, err := exec.LookPath("firecracker"); err == nil {
		return path, nil
	}
	if strings.TrimSpace(anchor) != "" {
		// Resolve the anchor's symlinks first: a Homebrew bin/ entry is a
		// symlink into the Cellar, and ../libexec exists beside the real
		// binary, not beside the link.
		if resolved, err := filepath.EvalSymlinks(anchor); err == nil {
			anchor = resolved
		}
		packaged := filepath.Clean(filepath.Join(filepath.Dir(anchor), "..", "libexec", "firecracker"))
		if info, err := os.Stat(packaged); err == nil && !info.IsDir() {
			return packaged, nil
		}
	}
	return "", fmt.Errorf("%s", BinaryNotFoundError)
}
