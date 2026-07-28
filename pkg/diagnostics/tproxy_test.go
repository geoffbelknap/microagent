package diagnostics

import (
	"errors"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// allModulesLoadedProc renders a /proc/modules listing all required modules.
func allModulesLoadedProc() []byte {
	var b strings.Builder
	for _, m := range egressprereq.TProxyModules {
		b.WriteString(m)
		b.WriteString(" 16384 0 - Live 0x0\n")
	}
	return []byte(b.String())
}

// The attempt-based probe outranks the module heuristic in both directions:
// a kernel that accepts the real steering rule is ready no matter what the
// module list says (autoload and parameterless built-ins are invisible to
// presence checks), and a kernel that refuses it is not ready even with every
// module apparently present.
func TestDeriveTProxyReadinessProbeOutranksModules(t *testing.T) {
	// Probe passes, zero modules visible: ready.
	host := &vmkit.HostSupport{SupervisorAvailable: true, SupervisorPath: "/opt/sup"}
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{
		probeSupport: func(string) (bool, error) { return true, nil },
		readFile:     func(string) ([]byte, error) { return nil, nil },
		statDir:      func(string) bool { return false },
	})
	if !host.EgressTProxyReady || len(host.EgressTProxyMissingModules) != 0 || host.EgressTProxyProbeError != "" {
		t.Fatalf("probe pass should be ready with nothing missing: %+v", host)
	}

	// Probe refused, every module visible: not ready, refusal recorded.
	host = &vmkit.HostSupport{SupervisorAvailable: true, SupervisorPath: "/opt/sup"}
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{
		probeSupport: func(string) (bool, error) { return true, errors.New("nft refused") },
		readFile:     func(string) ([]byte, error) { return allModulesLoadedProc(), nil },
		statDir:      func(string) bool { return false },
	})
	if host.EgressTProxyReady {
		t.Fatal("kernel refusal must not read ready")
	}
	if host.EgressTProxyProbeError != "nft refused" {
		t.Errorf("probe error = %q, want recorded refusal", host.EgressTProxyProbeError)
	}
	hint := EgressTProxyRemediation(host)
	if !strings.Contains(hint, "nft refused") {
		t.Errorf("remediation should carry the kernel's refusal: %q", hint)
	}
}

// When the probe cannot run (ran=false) or the supervisor is absent, the
// module heuristic decides, exactly as it did before the probe existed.
func TestDeriveTProxyReadinessFallsBackWithoutProbe(t *testing.T) {
	host := &vmkit.HostSupport{SupervisorAvailable: true, SupervisorPath: "/opt/sup"}
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{
		probeSupport: func(string) (bool, error) { return false, errors.New("no unshare") },
		readFile:     func(string) ([]byte, error) { return allModulesLoadedProc(), nil },
		statDir:      func(string) bool { return false },
	})
	if !host.EgressTProxyReady {
		t.Fatalf("unavailable probe should defer to the module heuristic: %+v", host)
	}

	// Supervisor absent: probe skipped entirely, heuristic decides.
	called := false
	host = &vmkit.HostSupport{}
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{
		probeSupport: func(string) (bool, error) { called = true; return true, nil },
		readFile:     func(string) ([]byte, error) { return nil, nil },
		statDir:      func(string) bool { return false },
	})
	if called {
		t.Error("probe must not run against a missing supervisor")
	}
	if host.EgressTProxyReady {
		t.Error("no probe and no modules should not read ready")
	}
}

func TestDeriveTProxyModuleReadinessAllLoaded(t *testing.T) {
	host := &vmkit.HostSupport{}
	probe := tproxyModuleProbe{
		readFile: func(string) ([]byte, error) { return allModulesLoadedProc(), nil },
		statDir:  func(string) bool { return false },
	}
	deriveTProxyModuleReadiness(host, probe)
	if !host.EgressTProxyReady {
		t.Fatalf("expected ready when all modules loaded; missing=%v", host.EgressTProxyMissingModules)
	}
	if len(host.EgressTProxyMissingModules) != 0 {
		t.Errorf("missing = %v, want none", host.EgressTProxyMissingModules)
	}
	if got := EgressTProxyRemediation(host); got != "" {
		t.Errorf("ready host remediation = %q, want empty", got)
	}
}

func TestDeriveTProxyModuleReadinessMissing(t *testing.T) {
	host := &vmkit.HostSupport{}
	// Only one module loaded, none built-in.
	probe := tproxyModuleProbe{
		readFile: func(string) ([]byte, error) { return []byte("nft_tproxy 1 0 - Live 0x0\n"), nil },
		statDir:  func(string) bool { return false },
	}
	deriveTProxyModuleReadiness(host, probe)
	if host.EgressTProxyReady {
		t.Fatal("expected not ready when modules missing")
	}
	if len(host.EgressTProxyMissingModules) != 1 {
		t.Errorf("missing = %v, want just nf_tproxy_ipv4", host.EgressTProxyMissingModules)
	}
	hint := EgressTProxyRemediation(host)
	if !strings.Contains(hint, "TPROXY") || !strings.Contains(hint, "modprobe") {
		t.Errorf("remediation = %q, want TPROXY + modprobe hint", hint)
	}
}

func TestDeriveTProxyModuleReadinessBuiltin(t *testing.T) {
	host := &vmkit.HostSupport{}
	// /proc/modules unreadable, but every module is built-in (sysfs node exists).
	probe := tproxyModuleProbe{
		readFile: func(string) ([]byte, error) { return nil, errors.New("no such file") },
		statDir:  func(string) bool { return true },
	}
	deriveTProxyModuleReadiness(host, probe)
	if !host.EgressTProxyReady {
		t.Fatalf("built-in modules should be ready; missing=%v", host.EgressTProxyMissingModules)
	}
}

func TestDeriveTProxyModuleReadinessNilProbe(t *testing.T) {
	host := &vmkit.HostSupport{}
	// No readFile and no statDir -> nothing present -> all missing, not ready.
	deriveTProxyModuleReadiness(host, tproxyModuleProbe{})
	if host.EgressTProxyReady {
		t.Fatal("with no probe inputs, modules cannot be confirmed -> not ready")
	}
	if len(host.EgressTProxyMissingModules) != len(egressprereq.TProxyModules) {
		t.Errorf("missing = %v, want all", host.EgressTProxyMissingModules)
	}
}

func TestEgressTProxyRemediationNilHost(t *testing.T) {
	if got := EgressTProxyRemediation(nil); got != "" {
		t.Errorf("nil host remediation = %q, want empty", got)
	}
}
