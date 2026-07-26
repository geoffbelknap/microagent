//go:build linux

package firecracker

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestValidateFirecrackerConfigRejectsUnsupportedNetworkMode(t *testing.T) {
	err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "open"}})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("validateFirecrackerConfig err = %v", err)
	}
}

func TestValidateFirecrackerConfigAcceptsIsolatedNetworkMode(t *testing.T) {
	if err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "isolated"}}); err != nil {
		t.Fatalf("validateFirecrackerConfig isolated: %v", err)
	}
}

func TestValidateFirecrackerConfigAcceptsUserNetworkMode(t *testing.T) {
	if err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: "user"}}); err != nil {
		t.Fatalf("validateFirecrackerConfig user: %v", err)
	}
}

func TestValidateFirecrackerConfigRejectsRemovedNetworkModes(t *testing.T) {
	for _, mode := range []string{"bridged", "nat", "named"} {
		err := validateFirecrackerConfig(&vmkit.Config{Network: &vmkit.NetworkConfig{Mode: mode}})
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("validateFirecrackerConfig(%q) err = %v", mode, err)
		}
	}
}

func TestSupervisorCheckAcceptsIsolatedFirecrackerNetworkMode(t *testing.T) {
	req := vmkit.Request{
		Command: "check",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   t.TempDir(),
			Network:    &vmkit.NetworkConfig{Mode: "isolated"},
		},
	}
	resp, err := Supervisor{}.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("Supervisor.Do rejected isolated network mode: resp=%+v err=%v", resp, err)
	}
	if !resp.OK || resp.Backend != vmkit.BackendLinuxKVM {
		t.Fatalf("response = %+v err = %v", resp, err)
	}
}

func TestWriteConfigAddsUserNetworkInterfaceAndBootArgs(t *testing.T) {
	opts := Options{Name: "agent-1", StateDir: t.TempDir()}
	req := vmkit.Request{
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendLinuxKVM,
		},
		Config: &vmkit.Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   opts.StateDir,
			MemoryMiB:  512,
			CPUCount:   2,
			ShellPort:  24279,
			ExecPort:   25279,
			Network: &vmkit.NetworkConfig{
				Mode:    "user",
				IP:      "10.43.12.2/29",
				Gateway: "10.43.12.1",
				DNS:     []string{"1.1.1.1", "8.8.8.8"},
			},
		},
	}
	if err := writeConfig(opts, req); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	var cfg config
	data, err := os.ReadFile(configPath(opts))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.NetworkInterfaces) != 1 {
		t.Fatalf("network interfaces = %#v", cfg.NetworkInterfaces)
	}
	bootArgs := cfg.BootSource.BootArgs
	if !strings.Contains(bootArgs, "microagent_net_if=eth0") ||
		!strings.Contains(bootArgs, "microagent_net_ip=10.43.12.2/29") ||
		!strings.Contains(bootArgs, "microagent_net_gw=10.43.12.1") ||
		!strings.Contains(bootArgs, "microagent_shell_port=24279") ||
		!strings.Contains(bootArgs, "microagent_exec_port=25279") ||
		!strings.Contains(bootArgs, "microagent_net_dns=1.1.1.1,8.8.8.8") {
		t.Fatalf("boot args = %q", bootArgs)
	}
}
