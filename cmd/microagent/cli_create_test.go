package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestCreateDispatchKeepsLowLevelSupervisorCreate(t *testing.T) {
	if !shouldUseHighLevelCreate([]string{"research"}) {
		t.Fatal("positional create should use high-level workspace create")
	}
	if !shouldUseHighLevelCreate([]string{"--name", "research"}) {
		t.Fatal("--name create should use high-level workspace create")
	}
	if shouldUseHighLevelCreate([]string{"--id", "agent", "--rootfs", "/tmp/rootfs.ext4", "--kernel", "/tmp/Image"}) {
		t.Fatal("low-level rootfs create should stay on supervisor create path")
	}
}

func TestWorkspaceSupervisorSelectsHostBackendOnly(t *testing.T) {
	// The linux-kvm default resolves an installed supervisor next to the
	// `microagent` binary on PATH (and honors MICROAGENT_FIRECRACKER_SUPERVISOR),
	// so a host with microagent installed would leak an absolute path into the
	// bare-name assertion below. Point both at hermetic values.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("MICROAGENT_FIRECRACKER_SUPERVISOR", "")

	opts := workspaceOptions{Backend: hostBackend()}
	if hostBackend() == vmkit.BackendAppleVF {
		opts.SupervisorPath = "/tmp/applevf"
	}
	supervisor, err := workspace.Supervisor(opts)
	if err != nil {
		t.Fatalf("host supervisor: %v", err)
	}
	executable, ok := supervisor.(vmkit.ExecutableSupervisor)
	if !ok {
		t.Fatalf("host supervisor = %T, want vmkit.ExecutableSupervisor", supervisor)
	}
	if hostBackend() == vmkit.BackendLinuxKVM && executable.Path != "microagent-firecracker-supervisor" {
		t.Fatalf("firecracker supervisor path = %q", executable.Path)
	}
	if hostBackend() == vmkit.BackendAppleVF && executable.Path != "/tmp/applevf" {
		t.Fatalf("apple vf supervisor path = %q", executable.Path)
	}

	otherBackend := vmkit.BackendLinuxKVM
	if hostBackend() == vmkit.BackendLinuxKVM {
		otherBackend = vmkit.BackendAppleVF
	}
	if _, err := workspace.Supervisor(workspaceOptions{Backend: otherBackend}); err == nil {
		t.Fatalf("Supervisor(%q) err = nil, want host-only rejection", otherBackend)
	}
}

func TestParseWorkspaceOptionsUsesHostSupervisorDefault(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	want := defaultSupervisorPath(hostBackend())
	if opts.SupervisorPath != want {
		t.Fatalf("SupervisorPath = %q, want %q", opts.SupervisorPath, want)
	}
}

func TestParseWorkspaceOptionsAcceptsContainerStyleRunCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"docker.io/library/busybox:1.36",
		"echo",
		"hello world",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/busybox:1.36" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "exec 'echo' 'hello world'" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if opts.UseImageCommand {
		t.Fatal("UseImageCommand = true")
	}
}

func TestParseWorkspaceOptionsRunImageDefaultsToImageCommand(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"docker.io/library/busybox:1.36",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/busybox:1.36" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if !opts.UseImageCommand {
		t.Fatal("UseImageCommand = false")
	}
}

func TestParseWorkspaceOptionsRunPositionalCommandConflictsWithExec(t *testing.T) {
	_, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
		"echo",
	})
	if err == nil || !strings.Contains(err.Error(), "both --exec and positional command") {
		t.Fatalf("err = %v, want positional command conflict", err)
	}
}

func TestParseWorkspaceOptionsAcceptsContainerStyleRunAliases(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"-e", "GREETING=hello",
		"-p", "127.0.0.1:18080:8080/tcp",
		"--rm",
		"docker.io/library/busybox:1.36",
		"printenv",
		"GREETING",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Env["GREETING"] != "hello" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.Keep {
		t.Fatal("Keep = true")
	}
	if len(opts.Network.PortForwards) != 1 {
		t.Fatalf("PortForwards = %#v", opts.Network.PortForwards)
	}
	forward := opts.Network.PortForwards[0]
	if forward.Host != "127.0.0.1" || forward.HostPort != 18080 || forward.GuestPort != 8080 || forward.Protocol != "tcp" {
		t.Fatalf("PortForward = %#v", forward)
	}
	if opts.ExecCommand != "exec 'printenv' 'GREETING'" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
}

func TestParseWorkspaceOptionsRejectsRunRmKeepConflict(t *testing.T) {
	_, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--rm",
		"--keep",
		"docker.io/library/busybox:1.36",
		"true",
	})
	if err == nil || !strings.Contains(err.Error(), "both --rm and --keep") {
		t.Fatalf("err = %v, want --rm --keep conflict", err)
	}
}

func TestParseWorkspaceOptionsPreservesExplicitSupervisor(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--supervisor", "/tmp/microagent-supervisor",
		"--image", "docker.io/library/busybox:1.36",
		"--exec", "true",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.SupervisorPath != "/tmp/microagent-supervisor" {
		t.Fatalf("SupervisorPath = %q", opts.SupervisorPath)
	}
}

func TestDefaultPerfBootOptionsUsesHostSupervisorDefault(t *testing.T) {
	opts := defaultPerfBootOptions()
	want := defaultSupervisorPath(hostBackend())
	if opts.SupervisorPath != want {
		t.Fatalf("SupervisorPath = %q, want %q", opts.SupervisorPath, want)
	}
}

func firecrackerSupervisorHelper(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "microagent-firecracker-supervisor")
	script := fmt.Sprintf("#!/usr/bin/env bash\nGO_WANT_FIRECRACKER_SUPERVISOR_HELPER=1 %q\n", executable)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func processStillActive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if !platformProcessStillActive(pid) {
		return false
	}
	if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 && fields[2] == "Z" {
			return false
		}
	}
	return true
}

func TestWorkspaceCommandRunsSetupBeforeExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"apt-get update", "apt-get install -y git"},
		ExecCommand:   "uname -a",
	})
	want := "set -eu\napt-get update\napt-get install -y git\nuname -a"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestWorkspaceCommandAllowsMultiCommandExec(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		SetupCommands: []string{"echo setup"},
		ExecCommand:   "echo one; echo two",
	})
	want := "set -eu\necho setup\necho one; echo two"
	if command != want {
		t.Fatalf("workspaceCommand = %q, want %q", command, want)
	}
}

func TestExecWantsHelpIgnoresGuestArgvAfterSeparator(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"guest -h after separator", []string{"ws", "--", "psql", "-h", "x"}, false},
		{"guest --help after separator", []string{"ws", "--", "wget", "--help"}, false},
		{"guest literal help after separator", []string{"ws", "--", "help"}, false},
		{"cli --help", []string{"--help"}, true},
		{"cli -h before separator", []string{"ws", "-h"}, true},
		{"cli help word", []string{"help"}, true},
		{"cli --help with guest argv", []string{"ws", "--help", "--", "ls"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := execWantsHelp(tc.args); got != tc.want {
				t.Fatalf("execWantsHelp(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestWorkspaceCommandCarriesNoGuestConfigRewrite: the setup script is a
// plain script — the setup-to-final transition is a host-side manifest
// decision now, so no boot command may embed a guest config rewrite.
func TestWorkspaceCommandCarriesNoGuestConfigRewrite(t *testing.T) {
	command := workspaceCommand(workspaceOptions{
		Entrypoint:      "/app/entrypoint.sh",
		SetupCommands:   []string{"echo setup"},
		ResultPort:      1024,
		PrepareForStart: true,
	})
	if !strings.Contains(command, "echo setup") {
		t.Fatalf("workspaceCommand missing setup: %q", command)
	}
	if strings.Contains(command, "/etc/microagent/run.json") {
		t.Fatalf("workspaceCommand embeds a guest config rewrite: %q", command)
	}
}
