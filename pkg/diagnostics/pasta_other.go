//go:build !linux

package diagnostics

// Firecracker user-mode networking is Linux-only; on other platforms there is
// no pasta to probe and no SELinux to explain a failure with.
func defaultPastaStartProbe(pastaPath, stateDir string) error { return nil }

var defaultSELinuxConfinedPasta = func() (bool, string) { return false, "" }
