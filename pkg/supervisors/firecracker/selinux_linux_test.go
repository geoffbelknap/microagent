package firecracker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSELinux points the SELinux signals at controlled values for one test.
func fakeSELinux(t *testing.T, enforce string, label string) {
	t.Helper()
	dir := t.TempDir()
	enforcePath := filepath.Join(dir, "enforce")
	if enforce != "" {
		if err := os.WriteFile(enforcePath, []byte(enforce), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldPath, oldLookup := selinuxEnforcePath, lookupPastaLabel
	selinuxEnforcePath = enforcePath
	lookupPastaLabel = func() string { return label }
	t.Cleanup(func() { selinuxEnforcePath, lookupPastaLabel = oldPath, oldLookup })
}

// TestConfinedPastaFailureNamesThePolicy pins the diagnosis: a
// permission-denied pasta failure on an enforcing host with a confined pasta
// names SELinux and the fix, instead of a bare EACCES that reads like a
// microagent bug — the denial itself is only visible in the audit log the
// user has no reason to check.
func TestConfinedPastaFailureNamesThePolicy(t *testing.T) {
	fakeSELinux(t, "1", "system_u:object_r:pasta_exec_t:s0")

	err := userNetworkStartErrorWithHint("Couldn't open PID file /home/u/.microagent/ws/pasta.pid: Permission denied")

	for _, want := range []string{"SELinux", "pasta_exec_t", "semanage permissive -a pasta_t", "--network isolated", "Permission denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("diagnosis missing %q:\n%s", want, err)
		}
	}
}

// TestUnconfinedHostsKeepThePlainError is the misattribution guard, in both
// directions: no SELinux signal means a permission error stays a permission
// error, and a confined host with a NON-permission failure is not blamed on
// SELinux.
func TestUnconfinedHostsKeepThePlainError(t *testing.T) {
	fakeSELinux(t, "0", "system_u:object_r:pasta_exec_t:s0")
	err := userNetworkStartErrorWithHint("Couldn't open PID file /x/pasta.pid: Permission denied")
	if strings.Contains(err.Error(), "SELinux") {
		t.Errorf("permissive host blamed on SELinux:\n%s", err)
	}

	fakeSELinux(t, "1", "system_u:object_r:bin_t:s0")
	err = userNetworkStartErrorWithHint("Couldn't open PID file /x/pasta.pid: Permission denied")
	if strings.Contains(err.Error(), "SELinux") {
		t.Errorf("unconfined pasta blamed on SELinux:\n%s", err)
	}

	fakeSELinux(t, "1", "system_u:object_r:pasta_exec_t:s0")
	err = userNetworkStartErrorWithHint("something else entirely broke")
	if strings.Contains(err.Error(), "SELinux") {
		t.Errorf("a non-permission failure blamed on SELinux:\n%s", err)
	}
}

// TestSELinuxConfinedPastaDetail pins the signal reader itself.
func TestSELinuxConfinedPastaDetail(t *testing.T) {
	fakeSELinux(t, "1", "system_u:object_r:pasta_exec_t:s0")
	confined, detail := SELinuxConfinedPastaDetail()
	if !confined || !strings.Contains(detail, "pasta_exec_t") || !strings.Contains(detail, "enforcing") {
		t.Errorf("confined host not reported: %v %q", confined, detail)
	}

	fakeSELinux(t, "", "system_u:object_r:pasta_exec_t:s0") // no enforce file: no SELinux
	if confined, _ := SELinuxConfinedPastaDetail(); confined {
		t.Error("a host without SELinux reported as confined")
	}
}
