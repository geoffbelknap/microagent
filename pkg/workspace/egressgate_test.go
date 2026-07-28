package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// procModulesWith renders a minimal /proc/modules with the given modules
// loaded.
func procModulesWith(mods ...string) []byte {
	var b strings.Builder
	for _, m := range mods {
		b.WriteString(m + " 16384 0 - Live 0x0000000000000000\n")
	}
	return []byte(b.String())
}

const allTProxyModules = "nft_tproxy nf_tproxy_ipv4 xt_socket nf_socket_ipv4"

func TestValidateEgressHostPrereqs(t *testing.T) {
	noModules := func(string) ([]byte, error) { return procModulesWith(), nil }
	allModules := func(string) ([]byte, error) {
		return procModulesWith(strings.Fields(allTProxyModules)...), nil
	}
	notBuiltin := func(string) bool { return false }

	tests := []struct {
		name        string
		backend     string
		networkMode string
		egressMode  string
		readFile    func(string) ([]byte, error)
		statModule  func(string) bool
		wantErr     bool
	}{
		{
			name:    "mediated user-mode launch with missing modules refuses",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "broker",
			readFile: noModules, statModule: notBuiltin, wantErr: true,
		},
		{
			name:    "empty egress mode defaults to broker and refuses the same way",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "",
			readFile: noModules, statModule: notBuiltin, wantErr: true,
		},
		{
			name:    "egress off is the recorded override: passes untouched",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "off",
			readFile: noModules, statModule: notBuiltin, wantErr: false,
		},
		{
			name:    "isolated network has no egress to mediate: passes",
			backend: vmkit.BackendLinuxKVM, networkMode: "isolated", egressMode: "broker",
			readFile: noModules, statModule: notBuiltin, wantErr: false,
		},
		{
			name:    "apple-vf has no kernel-module prerequisite: passes",
			backend: vmkit.BackendAppleVF, networkMode: "user", egressMode: "broker",
			readFile: noModules, statModule: notBuiltin, wantErr: false,
		},
		{
			name:    "all modules loaded: passes",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "broker",
			readFile: allModules, statModule: notBuiltin, wantErr: false,
		},
		{
			name:    "modules built into the kernel count as present",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "mitm",
			readFile: noModules, statModule: func(string) bool { return true }, wantErr: false,
		},
		{
			name:    "unreadable /proc/modules never refuses: the boot path stays the fail-closed layer",
			backend: vmkit.BackendLinuxKVM, networkMode: "user", egressMode: "broker",
			readFile:   func(string) ([]byte, error) { return nil, errors.New("no procfs") },
			statModule: notBuiltin, wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEgressHostPrereqsProbed(tt.backend, tt.networkMode, tt.egressMode, tt.readFile, tt.statModule)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestValidateEgressHostPrereqsErrorNamesEverything pins the refusal text: the
// missing modules by name, the load remediation, and the explicit unmediated
// alternative, so the operator can pick either fix without a second lookup.
func TestValidateEgressHostPrereqsErrorNamesEverything(t *testing.T) {
	err := validateEgressHostPrereqsProbed(vmkit.BackendLinuxKVM, "user", "broker",
		func(string) ([]byte, error) { return procModulesWith("nft_tproxy", "nf_tproxy_ipv4"), nil },
		func(string) bool { return false })
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"xt_socket", "nf_socket_ipv4", "modprobe", "--egress off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	for _, absent := range []string{"nft_tproxy,", "nf_tproxy_ipv4,"} {
		if strings.Contains(err.Error(), absent) {
			t.Errorf("refusal names a module that is loaded: %v", err)
		}
	}
}
