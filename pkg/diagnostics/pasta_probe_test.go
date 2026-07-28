package diagnostics

import (
	"errors"
	"strings"
	"testing"
)

// TestPastaStartIssueNamesConfinement pins the diagnosis split: permission
// denied + confined pasta names SELinux and both fixes; everything else stays
// a plain probe failure so unrelated breakage is never misattributed.
func TestPastaStartIssueNamesConfinement(t *testing.T) {
	confined := func() (bool, string) {
		return true, "pasta labeled system_u:object_r:pasta_exec_t:s0, SELinux enforcing"
	}
	free := func() (bool, string) { return false, "" }
	denied := errors.New("Couldn't open PID file /home/u/.microagent/doctor-pasta-probe/pasta.pid: Permission denied")

	msg := pastaStartIssue(denied, confined)
	for _, want := range []string{"SELinux", "pasta_exec_t", "semanage permissive -a pasta_t", "--network isolated"} {
		if !strings.Contains(msg, want) {
			t.Errorf("confined diagnosis missing %q:\n%s", want, msg)
		}
	}

	if msg := pastaStartIssue(denied, free); strings.Contains(msg, "SELinux") {
		t.Errorf("unconfined host blamed on SELinux:\n%s", msg)
	}
	if msg := pastaStartIssue(errors.New("exec format error"), confined); strings.Contains(msg, "SELinux") {
		t.Errorf("non-permission failure blamed on SELinux:\n%s", msg)
	}
}

// TestDoctorRunsThePastaStartProbe pins the wiring: a present pasta binary
// that fails the start probe flips UserNetworkingAvailable to false and
// carries the issue — finding the binary is not the capability, and a doctor
// that only ran LookPath reported a green host that cannot boot.
func TestDoctorRunsThePastaStartProbe(t *testing.T) {
	probeRan := false
	resp, err := CheckFirecracker(
		Options{Backend: "linux-kvm", Arch: "amd64", StateDir: t.TempDir()},
		FirecrackerProbe{
			LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			ProbePastaStart: func(pastaPath, stateDir string) error {
				probeRan = true
				return errors.New("Couldn't open PID file: Permission denied")
			},
			SELinuxConfinedPasta: func() (bool, string) { return true, "pasta labeled pasta_exec_t, SELinux enforcing" },
		},
	)
	_ = err // non-nil whenever any issue exists; the response carries the diagnosis
	if !probeRan {
		t.Fatal("the pasta start probe never ran")
	}
	if resp.Host == nil || resp.Host.UserNetworkingAvailable {
		t.Error("a pasta that cannot start still reported user networking available")
	}
	if !strings.Contains(resp.Error, "semanage permissive -a pasta_t") {
		t.Errorf("issues carry no fix:\n%s", resp.Error)
	}

	// Control: a passing probe leaves the capability green with no issue.
	resp, err = CheckFirecracker(
		Options{Backend: "linux-kvm", Arch: "amd64", StateDir: t.TempDir()},
		FirecrackerProbe{
			LookPath:        func(name string) (string, error) { return "/usr/bin/" + name, nil },
			ProbePastaStart: func(string, string) error { return nil },
		},
	)
	_ = err
	if resp.Host == nil || !resp.Host.UserNetworkingAvailable {
		t.Error("a passing probe lost the capability")
	}
	if strings.Contains(resp.Error, "pasta failed") || strings.Contains(resp.Error, "pasta cannot start") {
		t.Errorf("a passing probe raised an issue:\n%s", resp.Error)
	}
}
