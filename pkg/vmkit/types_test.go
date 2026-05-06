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
