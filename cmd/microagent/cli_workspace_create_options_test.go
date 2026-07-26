package main

import (
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestParseWorkspaceOptionsForCreateDefaultsImageAndPositionalName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--arch", "amd64",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.ImageRef != defaultWorkspaceImageAMD64 {
		t.Fatalf("ImageRef = %q, want %q", opts.ImageRef, defaultWorkspaceImageAMD64)
	}
	if opts.Hostname != "research" {
		t.Fatalf("Hostname = %q", opts.Hostname)
	}
	if opts.MemoryMiB != defaultWorkspaceMemoryMiB || opts.CPUCount != 2 || opts.SizeMiB != rootfs.DefaultSizeMiB {
		t.Fatalf("defaults = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsAppliesResourceProfile(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--profile", "medium",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Profile != "medium" || opts.MemoryMiB != 2048 || opts.CPUCount != 2 || opts.SizeMiB != 8192 {
		t.Fatalf("profile resources = profile %q memory %d cpus %d size %d", opts.Profile, opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidConsoleShell(t *testing.T) {
	for _, shellPath := range []string{"bash", "/bin/../bin/bash"} {
		_, err := parseWorkspaceOptions("create", os.Stdout, []string{
			"research",
			"--image", "docker.io/library/ubuntu:24.04",
			"--shell", shellPath,
		})
		if err == nil {
			t.Fatalf("parseWorkspaceOptions accepted shell %q", shellPath)
		}
	}
}

func TestParseWorkspaceOptionsRejectsInvalidHostname(t *testing.T) {
	for _, hostname := range []string{"bad_name", "-bad", strings.Repeat("a", 64)} {
		_, err := parseWorkspaceOptions("create", os.Stdout, []string{
			"research",
			"--image", "docker.io/library/ubuntu:24.04",
			"--hostname", hostname,
		})
		if err == nil {
			t.Fatalf("parseWorkspaceOptions accepted hostname %q", hostname)
		}
	}
}

func TestParseWorkspaceOptionsLetsExplicitResourcesOverrideProfile(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--profile", "large",
		"--memory", "3072",
		"--cpus", "3",
		"--size-mib", "12288",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Profile != "large" || opts.MemoryMiB != 3072 || opts.CPUCount != 3 || opts.SizeMiB != 12288 {
		t.Fatalf("resources = profile %q memory %d cpus %d size %d", opts.Profile, opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsAcceptsRestartPolicy(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--restart", "on-failure",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.RestartPolicy != "on-failure" {
		t.Fatalf("RestartPolicy = %q", opts.RestartPolicy)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidRestartPolicy(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{"research", "--restart", "sometimes"})
	if err == nil || !strings.Contains(err.Error(), "restart policy") {
		t.Fatalf("err = %v, want restart validation", err)
	}
}

func TestShouldRestartWorkspace(t *testing.T) {
	tests := []struct {
		policy string
		state  vmkit.VMState
		want   bool
	}{
		{policy: "never", state: vmkit.StateFailed, want: false},
		{policy: "on-failure", state: vmkit.StateFailed, want: true},
		{policy: "on-failure", state: vmkit.StateStopped, want: false},
		{policy: "always", state: vmkit.StateStopped, want: true},
		{policy: "always", state: vmkit.StateFailed, want: true},
		{policy: "always", state: vmkit.StateRunning, want: false},
	}
	for _, tt := range tests {
		if got := shouldRestartWorkspace(tt.policy, tt.state); got != tt.want {
			t.Fatalf("shouldRestartWorkspace(%q, %q) = %v, want %v", tt.policy, tt.state, got, tt.want)
		}
	}
}

func TestParseWorkspaceOptionsRejectsUnknownProfile(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{"research", "--profile", "huge"})
	if err == nil || !strings.Contains(err.Error(), "unknown resource profile") {
		t.Fatalf("err = %v, want unknown profile", err)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidResources(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{"research", "--memory", "0"})
	if err == nil || !strings.Contains(err.Error(), "memory must be positive") {
		t.Fatalf("err = %v, want memory validation", err)
	}
	_, err = parseWorkspaceOptions("create", os.Stdout, []string{"research", "--size-mib", "0"})
	if err == nil || !strings.Contains(err.Error(), "size-mib must be positive") {
		t.Fatalf("err = %v, want size validation", err)
	}
}

func TestParseWorkspaceOptionsAcceptsDiskAndBundle(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--disk", "workspace=/tmp/workspace.ext4:/workspace:rw",
		"--bundle", "constraints=/tmp/constraints.tar:/config:ro",
		"--output", "report=/workspace/report.json",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.Disks) != 2 {
		t.Fatalf("Disks len = %d, want 2", len(opts.Disks))
	}
	if opts.Disks[0].Name != "workspace" || opts.Disks[0].Bundle {
		t.Fatalf("disk = %#v", opts.Disks[0])
	}
	if opts.Disks[1].Name != "constraints" || !opts.Disks[1].Bundle || opts.Disks[1].Mode != "ro" {
		t.Fatalf("bundle = %#v", opts.Disks[1])
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "report" || opts.Outputs[0].Path != "/workspace/report.json" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
}

func TestParseWorkspaceOptionsAcceptsSafeContainerStyleVolumes(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"-v", "/tmp/config.tar:/config:ro",
		"--volume", "/tmp/workspace.ext4:/workspace",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.Disks) != 2 {
		t.Fatalf("Disks len = %d, want 2", len(opts.Disks))
	}
	if opts.Disks[0].Name != "config" || opts.Disks[0].Path != "/tmp/config.tar" || opts.Disks[0].Mountpoint != "/config" || opts.Disks[0].Mode != "ro" || !opts.Disks[0].Bundle {
		t.Fatalf("bundle volume = %#v", opts.Disks[0])
	}
	if opts.Disks[1].Name != "workspace" || opts.Disks[1].Path != "/tmp/workspace.ext4" || opts.Disks[1].Mountpoint != "/workspace" || opts.Disks[1].Mode != "rw" || opts.Disks[1].Bundle {
		t.Fatalf("disk volume = %#v", opts.Disks[1])
	}
}

func TestParseWorkspaceOptionsRejectsHostBindMountVolume(t *testing.T) {
	dir := t.TempDir()
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"-v", dir + ":/workspace:rw",
	})
	if err == nil || !strings.Contains(err.Error(), "does not expose host bind mounts") {
		t.Fatalf("err = %v, want host bind mount rejection", err)
	}
}

func TestParseWorkspaceOptionsAcceptsManagedVolumeByName(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--volume", "cache:/cache:rw",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(opts.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(opts.Disks))
	}
	d := opts.Disks[0]
	if !d.ManagedVolume || d.Name != "cache" || d.Mountpoint != "/cache" || d.Mode != "rw" {
		t.Fatalf("unexpected managed volume disk: %+v", d)
	}
}

func TestParseWorkspaceOptionsRejectsUnsupportedVolumeSource(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--volume", "./data.bin:/data:rw",
	})
	if err == nil || !strings.Contains(err.Error(), "not host bind mounts") {
		t.Fatalf("err = %v, want unsupported volume rejection", err)
	}
}

func TestParseWorkspaceOptionsRejectsRemovedNetworkModes(t *testing.T) {
	for _, mode := range []string{"bridged", "nat", "named"} {
		_, err := parseWorkspaceOptions("create", os.Stdout, []string{"research", "--network", mode})
		if err == nil {
			t.Fatalf("parseWorkspaceOptions accepted removed network mode %q", mode)
		}
	}
}

func TestParseWorkspaceOptionsRejectsUnsupportedContainerCompatibilityFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "privileged",
			args: []string{"--privileged", "docker.io/library/busybox:1.36", "true"},
			want: "--privileged is not supported",
		},
		{
			name: "pod",
			args: []string{"--pod", "new:demo", "docker.io/library/busybox:1.36", "true"},
			want: "does not implement pods",
		},
		{
			name: "bind mount",
			args: []string{"--mount", "type=bind,source=/tmp,target=/workspace", "docker.io/library/busybox:1.36", "true"},
			want: "does not expose host bind mounts",
		},
		{
			name: "capability",
			args: []string{"--cap-add", "NET_ADMIN", "docker.io/library/busybox:1.36", "true"},
			want: "microVM boundary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseWorkspaceOptions("run", os.Stdout, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseWorkspaceOptionsAcceptsMediation(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--mediation", "2048=127.0.0.1:9900",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Mediation == nil || !opts.Mediation.Enabled || !opts.Mediation.Required || !opts.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
	if opts.Mediation.Port != 2048 || opts.Mediation.Target != "127.0.0.1:9900" {
		t.Fatalf("mediation endpoint = %#v", opts.Mediation)
	}
}

func TestParseWorkspaceOptionsAcceptsOptionalMediation(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--mediation", "2048=127.0.0.1:9900",
		"--mediation-optional",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Mediation == nil || !opts.Mediation.Enabled || opts.Mediation.Required || opts.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
}

func TestParseWorkspaceOptionsRejectsOptionalMediationWithoutMapping(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{"research", "--mediation-optional"})
	if err == nil || !strings.Contains(err.Error(), "requires --mediation") {
		t.Fatalf("err = %v, want mediation mapping error", err)
	}
}
