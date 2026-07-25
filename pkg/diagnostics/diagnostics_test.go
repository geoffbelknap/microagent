package diagnostics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/confine"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestCheckFirecrackerReportsHostSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				switch path {
				case "/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun":
					return fakeFileInfo{name: filepath.Base(path)}, nil
				default:
					return nil, os.ErrNotExist
				}
			},
			BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
			LookPath: func(name string) (string, error) {
				if name == "pasta" {
					return "/usr/bin/pasta", nil
				}
				return "", os.ErrNotExist
			},
			ReadFile: func(path string) ([]byte, error) {
				return []byte("1\n"), nil
			},
			ProbeUserNamespaces: func() error { return nil },
		},
	)
	if err != nil {
		t.Fatalf("CheckFirecracker: %v", err)
	}
	if !resp.OK || resp.Host == nil || !resp.Host.KVMAvailable || !resp.Host.VsockAvailable || !resp.Host.UserNetworkingAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if !resp.Host.UserNamespacesAvailable || !resp.Host.UserNetworkReady {
		t.Fatalf("user networking readiness = %#v", resp.Host)
	}
	if !resp.Host.SupervisorAvailable || resp.Host.SupervisorPath != "/usr/local/bin/microagent-firecracker-supervisor" {
		t.Fatalf("supervisor support = %#v", resp.Host)
	}
	if !resp.Host.GuestInitAvailable || resp.Host.GuestInitPath != "/usr/local/libexec/microagent-guestinit-amd64" {
		t.Fatalf("guest init support = %#v", resp.Host)
	}
	if !resp.Host.PauseResumeAvailable || !resp.Host.SnapshotAvailable {
		t.Fatalf("firecracker should advertise pause/resume and snapshot: %#v", resp.Host)
	}
}

func TestCheckFirecrackerReportsMissingSupport(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:       func() (string, error) { return "", fmt.Errorf("firecracker binary not found") },
			ResolveSupervisor:   func(Options) (string, error) { return "", fmt.Errorf("microagent Firecracker supervisor not found") },
			ResolveGuestInit:    func(Options) (string, error) { return "", fmt.Errorf("microagent guest init not found") },
			Stat:                func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
			LookPath:            func(string) (string, error) { return "", os.ErrNotExist },
			ReadFile:            func(string) ([]byte, error) { return []byte("0\n"), nil },
			ProbeUserNamespaces: func() error { return fmt.Errorf("clone: operation not permitted") },
		},
	)
	if err == nil {
		t.Fatal("CheckFirecracker returned nil error")
	}
	if resp.OK || resp.Host == nil || resp.Host.KVMAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Error == "" {
		t.Fatal("missing error")
	}
	if !strings.Contains(resp.Error, "microagent Firecracker supervisor not found") || !strings.Contains(resp.Error, "microagent guest init not found") {
		t.Fatalf("error = %q", resp.Error)
	}
	// Snapshot/pause-resume and console now derive from their L1 results, so with
	// no supervisor or firecracker binary they must report unavailable rather
	// than the former hardcoded true.
	if resp.Host.PauseResumeAvailable || resp.Host.SnapshotAvailable {
		t.Fatalf("snapshot family should be unavailable without a supervisor/binary: %#v", resp.Host)
	}
	if resp.Host.ConsoleAvailable {
		t.Fatalf("console should be unavailable without a supervisor: %#v", resp.Host)
	}
}

func TestCheckFirecrackerReportsMissingPasta(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				return fakeFileInfo{name: filepath.Base(path)}, nil
			},
			LookPath:            func(string) (string, error) { return "", os.ErrNotExist },
			ReadFile:            func(string) ([]byte, error) { return []byte("1\n"), nil },
			ProbeUserNamespaces: func() error { return nil },
		},
	)
	if err == nil {
		t.Fatal("CheckFirecracker returned nil error")
	}
	if resp.Host == nil || resp.Host.UserNetworkingAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if !strings.Contains(resp.Error, "apt install passt") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestCheckFirecrackerReportsAppArmorRestrictedUserNamespaces(t *testing.T) {
	resp, err := CheckFirecracker(
		Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"},
		FirecrackerProbe{
			ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
			ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
			ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
			Stat: func(path string) (os.FileInfo, error) {
				return fakeFileInfo{name: filepath.Base(path)}, nil
			},
			BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
			LookPath: func(name string) (string, error) {
				if name == "pasta" {
					return "/usr/bin/pasta", nil
				}
				return "", os.ErrNotExist
			},
			// Stock Ubuntu 24.04: the classic userns sysctls look permissive,
			// and namespace creation even succeeds — but AppArmor denies the
			// confined child's own uid_map write, so the self-map probe fails.
			ReadFile: func(path string) ([]byte, error) {
				switch path {
				case "/proc/sys/user/max_user_namespaces":
					return []byte("32768\n"), nil
				case "/proc/sys/kernel/apparmor_restrict_unprivileged_userns":
					return []byte("1\n"), nil
				}
				return nil, os.ErrNotExist
			},
			ProbeUserNamespaces: func() error {
				return fmt.Errorf("unshare --map-root-user --mount failed: unshare: write failed /proc/self/uid_map: Operation not permitted")
			},
		},
	)
	if err == nil {
		t.Fatal("CheckFirecracker returned nil error")
	}
	if resp.Host == nil || resp.Host.UserNamespacesAvailable {
		t.Fatalf("response = %#v", resp)
	}
	if !resp.Host.UserNetworkingAvailable {
		t.Fatalf("pasta presence should still be reported: %#v", resp.Host)
	}
	if resp.Host.UserNetworkReady {
		t.Fatalf("user networking must not be ready without user namespaces: %#v", resp.Host)
	}
	if !strings.Contains(resp.Error, "kernel.apparmor_restrict_unprivileged_userns=0") {
		t.Fatalf("error = %q", resp.Error)
	}
}

func TestCheckUserNamespaces(t *testing.T) {
	readFiles := func(contents map[string]string) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if value, ok := contents[path]; ok {
				return []byte(value), nil
			}
			return nil, os.ErrNotExist
		}
	}
	probeFail := func() error { return fmt.Errorf("clone: operation not permitted") }
	cases := []struct {
		name         string
		files        map[string]string
		probe        func() error
		wantOK       bool
		issueExcerpt string
	}{
		{
			name:   "probe success is authoritative",
			files:  map[string]string{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n"},
			probe:  func() error { return nil },
			wantOK: true,
		},
		{
			name: "probe failure with apparmor restriction",
			files: map[string]string{
				"/proc/sys/user/max_user_namespaces":                     "32768\n",
				"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n",
			},
			probe:        probeFail,
			wantOK:       false,
			issueExcerpt: "kernel.apparmor_restrict_unprivileged_userns=0",
		},
		{
			name:         "probe failure prefers the disabled-clone sysctl hint",
			files:        map[string]string{"/proc/sys/kernel/unprivileged_userns_clone": "0\n"},
			probe:        probeFail,
			wantOK:       false,
			issueExcerpt: "kernel.unprivileged_userns_clone=1",
		},
		{
			name:         "probe failure prefers the max-namespaces sysctl hint",
			files:        map[string]string{"/proc/sys/user/max_user_namespaces": "0\n"},
			probe:        probeFail,
			wantOK:       false,
			issueExcerpt: "user.max_user_namespaces",
		},
		{
			name:         "probe failure without any sysctl evidence",
			files:        map[string]string{},
			probe:        probeFail,
			wantOK:       false,
			issueExcerpt: "clone: operation not permitted",
		},
		{
			name:         "no probe falls back to apparmor sysctl",
			files:        map[string]string{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1\n"},
			wantOK:       false,
			issueExcerpt: "restricted by AppArmor",
		},
		{
			name:   "no probe and no sysctl evidence",
			files:  map[string]string{},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, issue := checkUserNamespaces(readFiles(tc.files), tc.probe)
			if ok != tc.wantOK {
				t.Fatalf("ok = %t, want %t (issue %q)", ok, tc.wantOK, issue)
			}
			if tc.wantOK && issue != "" {
				t.Fatalf("issue = %q, want empty", issue)
			}
			if tc.issueExcerpt != "" && !strings.Contains(issue, tc.issueExcerpt) {
				t.Fatalf("issue = %q, want to contain %q", issue, tc.issueExcerpt)
			}
		})
	}
}

// The apple-vf supervisor's `host` response now carries the real confinement
// posture (Spec B). AugmentHostSupport must pass it through unchanged — it only
// defaults the mode to "off" when the supervisor reported nothing.
func TestAugmentHostSupportPreservesAppleVFConfinement(t *testing.T) {
	resp := vmkit.Response{
		Backend: vmkit.BackendAppleVF,
		Host: &vmkit.HostSupport{
			Backend:           vmkit.BackendAppleVF,
			ConfinementMode:   "seatbelt",
			ConfinementActive: true,
		},
	}
	AugmentHostSupport(&resp, Options{Backend: vmkit.BackendAppleVF, Arch: "arm64"})
	if resp.Host.ConfinementMode != "seatbelt" {
		t.Errorf("ConfinementMode = %q, want \"seatbelt\"", resp.Host.ConfinementMode)
	}
	if !resp.Host.ConfinementActive {
		t.Error("ConfinementActive = false, want true (supervisor reported it active)")
	}
}

// When the apple-vf supervisor reports no confinement (e.g. an older binary or
// an error response with no Host), the honesty invariant requires "off"/false.
func TestAugmentHostSupportDefaultsAppleVFConfinementOff(t *testing.T) {
	resp := vmkit.Response{
		Backend: vmkit.BackendAppleVF,
		Host:    &vmkit.HostSupport{Backend: vmkit.BackendAppleVF},
	}
	AugmentHostSupport(&resp, Options{Backend: vmkit.BackendAppleVF, Arch: "arm64"})
	if resp.Host.ConfinementMode != "off" {
		t.Errorf("ConfinementMode = %q, want \"off\"", resp.Host.ConfinementMode)
	}
	if resp.Host.ConfinementActive {
		t.Error("ConfinementActive = true, want false (nothing reported)")
	}
}

// confinementProbe is a fully-satisfied Firecracker probe with an injectable
// effective uid and user-namespace outcome, so confinement resolution is
// deterministic regardless of the test runner's real uid or host policy.
func confinementProbe(euid int, usernsOK bool) FirecrackerProbe {
	usernsData := "0\n"
	if usernsOK {
		usernsData = "1\n"
	}
	return FirecrackerProbe{
		ResolveBinary:     func() (string, error) { return "/usr/local/bin/firecracker", nil },
		ResolveSupervisor: func(Options) (string, error) { return "/usr/local/bin/microagent-firecracker-supervisor", nil },
		ResolveGuestInit:  func(Options) (string, error) { return "/usr/local/libexec/microagent-guestinit-amd64", nil },
		Stat: func(path string) (os.FileInfo, error) {
			switch path {
			case "/dev/kvm", "/dev/vhost-vsock", "/dev/net/tun":
				return fakeFileInfo{name: filepath.Base(path)}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		BinaryVersion: func(string) string { return "Firecracker v1.15.1" },
		LookPath: func(name string) (string, error) {
			if name == "pasta" {
				return "/usr/bin/pasta", nil
			}
			return "", os.ErrNotExist
		},
		ReadFile: func(string) ([]byte, error) { return []byte(usernsData), nil },
		ProbeUserNamespaces: func() error {
			if usernsOK {
				return nil
			}
			return fmt.Errorf("clone: operation not permitted")
		},
		Geteuid: func() int { return euid },
	}
}

// TestCheckFirecrackerResolvesConfinement proves doctor reports the confinement
// posture it actually resolves (from euid + user-namespace availability) rather
// than a hardcoded default. The prior behavior always reported "off".
func TestCheckFirecrackerResolvesConfinement(t *testing.T) {
	opts := Options{Backend: vmkit.BackendLinuxKVM, Arch: "amd64"}
	cases := []struct {
		name       string
		knob       string // MICROAGENT_CONFINEMENT ("" = unset/auto)
		euid       int
		usernsOK   bool
		wantMode   string
		wantActive bool
	}{
		{"auto non-root with userns -> rootless", "", 1000, true, "rootless", true},
		{"auto root -> jailer", "", 0, false, "jailer", true},
		{"auto non-root without userns -> off", "", 1000, false, "off", false},
		{"explicit off -> off", "off", 1000, true, "off", false},
		{"explicit rootless with userns -> rootless", "rootless", 1000, true, "rootless", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(confine.EnvVar, tc.knob)
			// Ignore err: the no-userns case reports a host issue, but resp.Host
			// is still populated and confinement resolution still runs.
			resp, _ := CheckFirecracker(opts, confinementProbe(tc.euid, tc.usernsOK))
			// AugmentHostSupport is the defaulting funnel Check runs afterward.
			AugmentHostSupport(&resp, opts)
			if resp.Host == nil {
				t.Fatal("resp.Host is nil")
			}
			if resp.Host.ConfinementMode != tc.wantMode {
				t.Errorf("ConfinementMode = %q, want %q", resp.Host.ConfinementMode, tc.wantMode)
			}
			if resp.Host.ConfinementActive != tc.wantActive {
				t.Errorf("ConfinementActive = %v, want %v", resp.Host.ConfinementActive, tc.wantActive)
			}
		})
	}
}

func TestCheckFirecrackerGathersNetworkingFacts(t *testing.T) {
	probe := FirecrackerProbe{
		ResolveBinary:       func() (string, error) { return "/fc", nil },
		ResolveSupervisor:   func(Options) (string, error) { return "/sup", nil },
		ResolveGuestInit:    func(Options) (string, error) { return "/init", nil },
		Stat:                func(string) (os.FileInfo, error) { return nil, nil },
		BinaryVersion:       func(string) string { return "Firecracker v1.0.0" },
		LookPath:            func(string) (string, error) { return "/usr/bin/pasta", nil },
		ReadFile:            func(path string) ([]byte, error) { return nil, os.ErrNotExist },
		ProbeUserNamespaces: func() error { return nil },
	}
	resp, err := CheckFirecracker(Options{Backend: "linux-kvm", Arch: "amd64"}, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Host.IsolatedNetworkReady {
		t.Error("expected IsolatedNetworkReady")
	}
	if !resp.Host.UserNetworkReady {
		t.Error("expected UserNetworkReady when passt + user namespaces present")
	}
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
