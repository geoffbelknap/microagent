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
		cloneRel    = "kernel/unprivileged_userns_clone"
		maxRel      = "user/max_user_namespaces"
		apparmorRel = "kernel/apparmor_restrict_unprivileged_userns"
	)
	for _, tc := range []struct {
		name        string
		clone       *string
		max         *string
		apparmor    *string
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
			// Stock Ubuntu 24.04: the classic gates look permissive but the
			// AppArmor restriction denies the uid_map self-write.
			name:        "disabled via apparmor restriction",
			max:         strptr("32768\n"),
			apparmor:    strptr("1\n"),
			wantEnabled: false,
			reasonHas:   "kernel.apparmor_restrict_unprivileged_userns=1",
		},
		{
			name:        "enabled with apparmor restriction off",
			clone:       strptr("1\n"),
			max:         strptr("32768\n"),
			apparmor:    strptr("0\n"),
			wantEnabled: true,
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
			if tc.apparmor != nil {
				writeProcFile(t, root, apparmorRel, *tc.apparmor)
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

	t.Run("namespace signature adds isolated pointer and preserves stderr", func(t *testing.T) {
		stderr := "Failed to clone() in setup_userns(): Operation not permitted"
		err := userNetworkStartErrorWithHint(stderr)
		msg := err.Error()
		if !strings.Contains(msg, "--network isolated") {
			t.Fatalf("guiding error missing --network isolated pointer: %q", msg)
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
		if !strings.Contains(err.Error(), "--network isolated") {
			t.Fatalf("expected isolated pointer for unshare signature: %q", err.Error())
		}
		if !strings.Contains(err.Error(), stderr) {
			t.Fatalf("expected stderr preserved: %q", err.Error())
		}
	})

	t.Run("unrelated failure falls back to plain wrap preserving stderr", func(t *testing.T) {
		stderr := "pasta: Couldn't open /dev/net/tun: No such file or directory"
		err := userNetworkStartErrorWithHint(stderr)
		msg := err.Error()
		if strings.Contains(msg, "--network isolated") {
			t.Fatalf("plain failure should not advertise isolated pointer: %q", msg)
		}
		if !strings.Contains(msg, "start firecracker user networking with pasta") {
			t.Fatalf("plain failure lost the standard prefix: %q", msg)
		}
		if !strings.Contains(msg, stderr) {
			t.Fatalf("plain failure dropped stderr: %q", msg)
		}
	})
}

func TestUserNetworkStartErrorWithHintAppArmorRestriction(t *testing.T) {
	// Stock Ubuntu 24.04: classic gates permissive, AppArmor restriction on. The
	// serial-log symptom is the confined child's own uid_map write being denied;
	// the hint must name the AppArmor gate with its matching fix, not the
	// classic sysctls.
	root := t.TempDir()
	writeProcFile(t, root, "user/max_user_namespaces", "32768\n")
	writeProcFile(t, root, "kernel/apparmor_restrict_unprivileged_userns", "1\n")
	restore := procRoot
	procRoot = root
	defer func() { procRoot = restore }()

	stderr := "unshare: write failed /proc/self/uid_map: Operation not permitted"
	err := userNetworkStartErrorWithHint(stderr)
	msg := err.Error()
	if !strings.Contains(msg, "kernel.apparmor_restrict_unprivileged_userns=1") {
		t.Fatalf("hint does not name the AppArmor gate: %q", msg)
	}
	if !strings.Contains(msg, "sysctl -w kernel.apparmor_restrict_unprivileged_userns=0") {
		t.Fatalf("hint missing the AppArmor sysctl fix: %q", msg)
	}
	if strings.Contains(msg, "kernel.unprivileged_userns_clone=1") {
		t.Fatalf("hint offers the wrong (classic sysctl) fix: %q", msg)
	}
	if !strings.Contains(msg, stderr) {
		t.Fatalf("hint dropped original stderr: %q", msg)
	}
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
	if !strings.Contains(msg, "--network isolated") {
		t.Fatalf("probe-disabled host should get isolated pointer: %q", msg)
	}
	if !strings.Contains(msg, stderr) {
		t.Fatalf("probe-disabled hint dropped stderr: %q", msg)
	}
}

func strptr(s string) *string { return &s }
