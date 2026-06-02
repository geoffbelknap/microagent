package vmkit

import (
	"encoding/json"
	"testing"
)

func TestConfigSecretsJSONRoundTrip(t *testing.T) {
	in := Config{
		SecretsPort:    1026,
		Secrets:        []SecretRef{{Name: "API_KEY", Ref: "vault:secret/data/app#api_key"}},
		SecretEnvFiles: []string{"/etc/app.env"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.SecretsPort != 1026 || len(out.Secrets) != 1 || out.Secrets[0].Name != "API_KEY" {
		t.Fatalf("secrets did not round-trip: %+v", out)
	}
	if out.Secrets[0].Ref != "vault:secret/data/app#api_key" || len(out.SecretEnvFiles) != 1 {
		t.Fatalf("secret ref/env-files did not round-trip: %+v", out)
	}
}

func TestValidateConfigAppliesDefaults(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryMiB != 512 || cfg.CPUCount != 2 {
		t.Fatalf("defaults = memory %d cpu %d", cfg.MemoryMiB, cfg.CPUCount)
	}
}

func TestValidateRequestAcceptsHaltWithStateDir(t *testing.T) {
	req := Request{
		Command: "halt",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      RoleWorkload,
			Backend:   BackendFirecracker,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err != nil {
		t.Fatalf("ValidateRequest rejected halt: %v", err)
	}
}

func TestValidateRequestAcceptsQuarantineWithStateDir(t *testing.T) {
	req := Request{
		Command: "quarantine",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      RoleWorkload,
			Backend:   BackendFirecracker,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err != nil {
		t.Fatalf("ValidateRequest rejected quarantine: %v", err)
	}
}

func TestValidateRequestAcceptsPauseAndResumeWithStateDir(t *testing.T) {
	for _, command := range []string{"pause", "resume"} {
		req := Request{
			Command: command,
			Identity: &Identity{
				RequestID: "req-1",
				RuntimeID: "agent-1",
				Role:      RoleWorkload,
				Backend:   BackendFirecracker,
			},
			Config: &Config{StateDir: "/tmp/state"},
		}
		if err := ValidateRequest(req); err != nil {
			t.Fatalf("ValidateRequest rejected %s: %v", command, err)
		}
	}
}

func TestValidateRequestRejectsRuntimeIDTraversal(t *testing.T) {
	req := Request{
		Command: "delete",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "../victim",
			Role:      RoleWorkload,
			Backend:   BackendAppleVF,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("ValidateRequest accepted runtimeID traversal")
	}
}

func TestValidateRequestRejectsRuntimeIDPathSeparator(t *testing.T) {
	req := Request{
		Command: "delete",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "parent/child",
			Role:      RoleWorkload,
			Backend:   BackendAppleVF,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("ValidateRequest accepted runtimeID path separator")
	}
}

func TestValidateConfigRejectsDuplicateVsockPorts(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		MemoryMiB:  512,
		CPUCount:   2,
		VsockListeners: []VsockListener{
			{Port: 1024, Target: "127.0.0.1:8200"},
			{Port: 1024, Target: "127.0.0.1:8300"},
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected duplicate port error")
	}
}

func TestValidateConfigAcceptsMediation(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		Mediation: &MediationConfig{
			Enabled:    true,
			Required:   true,
			Port:       2048,
			Target:     "127.0.0.1:9900",
			FailClosed: true,
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig rejected mediation: %v", err)
	}
}

func TestValidateMediationConfigRejectsIncompleteRequiredChannel(t *testing.T) {
	tests := []MediationConfig{
		{Required: true, Port: 2048, Target: "127.0.0.1:9900", FailClosed: true},
		{Enabled: true, Required: true, Target: "127.0.0.1:9900", FailClosed: true},
		{Enabled: true, Required: true, Port: 2048, FailClosed: true},
		{Enabled: true, Required: true, Port: 2048, Target: "127.0.0.1:9900"},
	}
	for _, mediation := range tests {
		if err := ValidateMediationConfig(mediation); err == nil {
			t.Fatalf("ValidateMediationConfig accepted %#v", mediation)
		}
	}
}

func TestValidateConfigRejectsBadDiskMode(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		Disks: []Disk{{
			Name:       "constraints",
			Path:       "/tmp/constraints.ext4",
			Mountpoint: "/config",
			Mode:       "writeable",
		}},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig accepted bad disk mode")
	}
}

func TestValidateNetworkConfigRejectsInvalidMode(t *testing.T) {
	if err := ValidateNetworkConfig(NetworkConfig{Mode: "open"}); err == nil {
		t.Fatal("ValidateNetworkConfig accepted invalid mode")
	}
}

func TestValidateNetworkConfigAcceptsUserMode(t *testing.T) {
	if err := ValidateNetworkConfig(NetworkConfig{Mode: "user"}); err != nil {
		t.Fatalf("ValidateNetworkConfig user: %v", err)
	}
}

func TestValidateNetworkConfigRejectsIsolatedPortForwards(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "isolated",
		PortForwards: []PortForward{
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted isolated port forward")
	}
}

func TestValidateNetworkConfigRejectsUnsupportedPortForwardProtocol(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "nat",
		PortForwards: []PortForward{
			{Protocol: "udp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted udp port forward")
	}
}

func TestValidateNetworkConfigRejectsDuplicateHostPorts(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "nat",
		PortForwards: []PortForward{
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 8080},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted duplicate host ports")
	}
}
