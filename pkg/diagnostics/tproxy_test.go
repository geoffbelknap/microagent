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
	if len(host.EgressTProxyMissingModules) != 3 {
		t.Errorf("missing = %v, want 3 modules", host.EgressTProxyMissingModules)
	}
	hint := EgressTProxyRemediation(host)
	if !strings.Contains(hint, "TPROXY") || !strings.Contains(hint, "microagent host setup-networking") {
		t.Errorf("remediation = %q, want TPROXY + setup-networking hint", hint)
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
