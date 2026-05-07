package vmkit

import "testing"

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
