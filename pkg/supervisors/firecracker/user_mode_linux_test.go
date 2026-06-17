//go:build linux

package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProcFile creates <root>/proc/sys/<rel> with the given contents so the
// userns probe can be exercised against synthetic sysctl files without root or
// touching the real /proc. The layout matches how procRoot is concatenated with
// the absolute /proc/sys/... gate paths.
func writeProcFile(t *testing.T, root, rel, contents string) {
	t.Helper()
	path := filepath.Join(root, "proc", "sys", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestUnprivilegedUserNSEnabled(t *testing.T) {
	const (
		cloneRel = "kernel/unprivileged_userns_clone"
		maxRel   = "user/max_user_namespaces"
	)
	for _, tc := range []struct {
		name        string
		clone       *string
		max         *string
		wantEnabled bool
		reasonHas   string
	}{
		{
			name:        "disabled via clone=0",
			clone:       strptr("0\n"),
			max:         strptr("32768\n"),
			wantEnabled: false,
			reasonHas:   "kernel.unprivileged_userns_clone=0",
		},
		{
			name:        "disabled via max=0",
			clone:       strptr("1\n"),
			max:         strptr("0\n"),
			wantEnabled: false,
			reasonHas:   "user.max_user_namespaces=0",
		},
		{
			name:        "enabled",
			clone:       strptr("1\n"),
			max:         strptr("32768\n"),
			wantEnabled: true,
		},
		{
			name:        "enabled when sysctls absent",
			clone:       nil,
			max:         nil,
			wantEnabled: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.clone != nil {
				writeProcFile(t, root, cloneRel, *tc.clone)
			}
			if tc.max != nil {
				writeProcFile(t, root, maxRel, *tc.max)
			}
			restore := procRoot
			procRoot = root
			defer func() { procRoot = restore }()

			enabled, reason := unprivilegedUserNSEnabled()
			if enabled != tc.wantEnabled {
				t.Fatalf("unprivilegedUserNSEnabled enabled=%v reason=%q, want enabled=%v", enabled, reason, tc.wantEnabled)
			}
			if tc.wantEnabled {
				if reason != "" {
					t.Fatalf("expected empty reason when enabled, got %q", reason)
				}
				return
			}
			if !strings.Contains(reason, tc.reasonHas) {
				t.Fatalf("reason %q does not contain %q", reason, tc.reasonHas)
			}
		})
	}
}

func TestUserNetworkStartErrorWithHint(t *testing.T) {
	// Point the probe at a tempdir with no sysctl files so the probe alone
	// reports "enabled"; this isolates the stderr-signature branch.
	root := t.TempDir()
	restore := procRoot
	procRoot = root
	defer func() { procRoot = restore }()

	t.Run("namespace signature adds nat pointer and preserves stderr", func(t *testing.T) {
		stderr := "Failed to clone() in setup_userns(): Operation not permitted"
		err := userNetworkStartErrorWithHint(stderr)
		msg := err.Error()
		if !strings.Contains(msg, "--network nat") {
			t.Fatalf("guiding error missing --network nat pointer: %q", msg)
		}
		if !strings.Contains(msg, "unprivileged user namespaces") {
			t.Fatalf("guiding error missing userns explanation: %q", msg)
		}
		if !strings.Contains(msg, stderr) {
			t.Fatalf("guiding error dropped original stderr: %q", msg)
		}
	})

	t.Run("unshare signature triggers hint", func(t *testing.T) {
		stderr := "unshare(CLONE_NEWUSER): operation not permitted"
		err := userNetworkStartErrorWithHint(stderr)
		if !strings.Contains(err.Error(), "--network nat") {
			t.Fatalf("expected nat pointer for unshare signature: %q", err.Error())
		}
		if !strings.Contains(err.Error(), stderr) {
			t.Fatalf("expected stderr preserved: %q", err.Error())
		}
	})

	t.Run("unrelated failure falls back to plain wrap preserving stderr", func(t *testing.T) {
		stderr := "pasta: Couldn't open /dev/net/tun: No such file or directory"
		err := userNetworkStartErrorWithHint(stderr)
		msg := err.Error()
		if strings.Contains(msg, "--network nat") {
			t.Fatalf("plain failure should not advertise nat pointer: %q", msg)
		}
		if !strings.Contains(msg, "start firecracker user networking with pasta") {
			t.Fatalf("plain failure lost the standard prefix: %q", msg)
		}
		if !strings.Contains(msg, stderr) {
			t.Fatalf("plain failure dropped stderr: %q", msg)
		}
	})
}

func TestUserNetworkStartErrorWithHintProbeDisabled(t *testing.T) {
	// Even with an unrelated stderr, a probe-disabled host must surface the
	// guiding error so hardened hosts get a fix pointer.
	root := t.TempDir()
	writeProcFile(t, root, "user/max_user_namespaces", "0\n")
	restore := procRoot
	procRoot = root
	defer func() { procRoot = restore }()

	stderr := "pasta: some opaque failure"
	err := userNetworkStartErrorWithHint(stderr)
	msg := err.Error()
	if !strings.Contains(msg, "--network nat") {
		t.Fatalf("probe-disabled host should get nat pointer: %q", msg)
	}
	if !strings.Contains(msg, stderr) {
		t.Fatalf("probe-disabled hint dropped stderr: %q", msg)
	}
}

func strptr(s string) *string { return &s }
