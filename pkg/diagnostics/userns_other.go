//go:build !linux

package diagnostics

// defaultUserNamespaceProbe is nil on non-Linux platforms: user namespaces are
// a Linux concept and Firecracker user-mode networking is Linux-only, so the
// sysctl checks (which find no proc files and pass) decide the verdict.
var defaultUserNamespaceProbe func() error
