package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

func TestParseWorkspaceOptionsReadsSpecFile(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "microagent.yaml")
	spec := `
name: research
image: docker.io/library/ubuntu:24.04
profile: medium
restart: on-failure
entrypoint: /app/start.sh
shell: /bin/bash
hostname: research-vm
setup:
  - mkdir -p /workspace
  - file: ./setup.sh
  - run: echo ready > /workspace/status
env:
  MICROAGENT_NAME: research
resources:
  memoryMiB: 3072
  cpuCount: 3
  sizeMiB: 12288
network:
  mode: user
  forwards:
    - host: 127.0.0.1
      hostPort: 8080
      guestPort: 80
      protocol: tcp
  dns:
    - 1.1.1.1
  routes:
    - 0.0.0.0/0
mediation:
  enabled: true
  required: true
  port: 2048
  target: 127.0.0.1:9900
  failClosed: true
disks:
  - name: workspace
    path: /tmp/workspace.ext4
    mountpoint: /workspace
    mode: rw
bundles:
  - name: seeds
    path: /tmp/seeds.tar
    mountpoint: /config
    mode: ro
outputs:
  - name: report
    path: /workspace/report.json
files:
  - src: ./agent.py
    dst: /app/agent.py
    mode: "0755"
`
	if err := os.WriteFile(filepath.Join(dir, "agent.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("#!/bin/sh\napt-get update\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{"--file", specPath, "--backend", hostBackend()})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "research" || opts.ImageRef != "docker.io/library/ubuntu:24.04" || opts.Profile != "medium" || opts.RestartPolicy != "on-failure" {
		t.Fatalf("identity/image/profile = %#v", opts)
	}
	if opts.Entrypoint != "/app/start.sh" || opts.ConsoleShell != "/bin/bash" || opts.Hostname != "research-vm" || len(opts.SetupCommands) != 3 {
		t.Fatalf("commands = entrypoint %q shell %q hostname %q setup %#v", opts.Entrypoint, opts.ConsoleShell, opts.Hostname, opts.SetupCommands)
	}
	if !strings.Contains(opts.SetupCommands[1], "apt-get update") {
		t.Fatalf("setup file command = %q", opts.SetupCommands[1])
	}
	if opts.Env["MICROAGENT_NAME"] != "research" {
		t.Fatalf("env = %#v", opts.Env)
	}
	if opts.MemoryMiB != 3072 || opts.CPUCount != 3 || opts.SizeMiB != 12288 {
		t.Fatalf("resources = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
	if opts.Network.Mode != "user" || len(opts.Network.PortForwards) != 1 || opts.Network.PortForwards[0].HostPort != 8080 || len(opts.Network.DNS) != 1 {
		t.Fatalf("network = %#v", opts.Network)
	}
	if opts.Mediation == nil || !opts.Mediation.Required || opts.Mediation.Port != 2048 || opts.Mediation.Target != "127.0.0.1:9900" {
		t.Fatalf("mediation = %#v", opts.Mediation)
	}
	if len(opts.Disks) != 2 || opts.Disks[0].Name != "workspace" || opts.Disks[1].Name != "seeds" || !opts.Disks[1].Bundle {
		t.Fatalf("disks = %#v", opts.Disks)
	}
	if len(opts.Outputs) != 1 || opts.Outputs[0].Name != "report" || opts.Outputs[0].Path != "/workspace/report.json" {
		t.Fatalf("outputs = %#v", opts.Outputs)
	}
	if len(opts.Files) != 1 || opts.Files[0].SourcePath != filepath.Join(dir, "agent.py") || opts.Files[0].Path != "/app/agent.py" || opts.Files[0].Mode != "0755" {
		t.Fatalf("files = %#v", opts.Files)
	}
}

func TestParseWorkspaceOptionsRejectsInvalidSpecFiles(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "agent.py")
	if err := os.WriteFile(srcPath, []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		spec string
		want string
	}{
		{
			name: "missing source",
			spec: "name: bad\nfiles:\n  - src: ./missing.py\n    dst: /app/agent.py\n",
			want: "file src",
		},
		{
			name: "relative dst",
			spec: "name: bad\nfiles:\n  - src: ./agent.py\n    dst: app/agent.py\n",
			want: "file dst must be absolute",
		},
		{
			name: "duplicate dst",
			spec: "name: bad\nfiles:\n  - src: ./agent.py\n    dst: /app/agent.py\n  - src: ./agent.py\n    dst: /app/agent.py\n",
			want: "duplicate file dst",
		},
		{
			name: "missing setup file",
			spec: "name: bad\nsetup:\n  - file: ./missing.sh\n",
			want: "setup file",
		},
		{
			name: "ambiguous setup entry",
			spec: "name: bad\nsetup:\n  - run: echo ok\n    file: ./agent.py\n",
			want: "cannot use both run and file",
		},
		{
			name: "misnested network",
			spec: "name: bad\nresources:\n  memoryMiB: 1024\n  network:\n    mode: user\n",
			want: `unknown field "network" under resources; move network to the top level`,
		},
		{
			name: "unknown top-level field",
			spec: "name: bad\nnetwrok:\n  mode: user\n", //nolint:misspell // deliberate typo: exercises unknown-field rejection
			want: `unknown top-level field "netwrok"`,   //nolint:misspell // deliberate typo: exercises unknown-field rejection
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specPath := filepath.Join(dir, tt.name+".yaml")
			if err := os.WriteFile(specPath, []byte(tt.spec), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := parseWorkspaceOptions("create", os.Stdout, []string{"--file", specPath})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseWorkspaceOptionsFlagsOverrideSpecFile(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "microagent.yaml")
	spec := `
name: from-spec
image: docker.io/library/busybox:1.36
profile: large
env:
  MODE: spec
resources:
  memoryMiB: 4096
`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--file", specPath,
		"--name", "from-flag",
		"--image", "docker.io/library/ubuntu:24.04",
		"--profile", "small",
		"--memory", "1536",
		"--env", "MODE=flag",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "from-flag" || opts.ImageRef != "docker.io/library/ubuntu:24.04" || opts.Profile != "small" {
		t.Fatalf("overrides = name %q image %q profile %q", opts.Name, opts.ImageRef, opts.Profile)
	}
	if opts.MemoryMiB != 1536 || opts.CPUCount != 2 || opts.SizeMiB != rootfs.DefaultSizeMiB {
		t.Fatalf("resources = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
	if opts.Env["MODE"] != "flag" {
		t.Fatalf("env = %#v", opts.Env)
	}
}

func TestParseWorkspaceOptionsMergesSpecSetupEnvAndSecretFlags(t *testing.T) {
	dir := t.TempDir()
	specPath := filepath.Join(dir, "microagent.yaml")
	setupPath := filepath.Join(dir, "flag-setup.sh")
	if err := os.WriteFile(setupPath, []byte("#!/bin/sh\necho from-file\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := `
name: from-spec
image: docker.io/library/busybox:1.36
setup:
  - run: echo from-spec
env:
  MODE: spec
  SPEC_ONLY: "1"
`
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--file", specPath,
		"--setup", "echo from-flag",
		"--setup-file", setupPath,
		"--env", "MODE=flag",
		"-e", "FLAG_ONLY=1",
		"--secret", "API=env:API_TOKEN",
		"--secrets-env-file", "/tmp/app.env",
		"--secret-on-demand", "DB=env:DB_TOKEN",
		"--secrets-audit",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.SetupCommands) != 3 || !reflect.DeepEqual(opts.SetupCommands[:2], []string{"echo from-spec", "echo from-flag"}) || !strings.Contains(opts.SetupCommands[2], "echo from-file") {
		t.Fatalf("SetupCommands = %#v", opts.SetupCommands)
	}
	if opts.Env["MODE"] != "flag" || opts.Env["SPEC_ONLY"] != "1" || opts.Env["FLAG_ONLY"] != "1" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.Secrets["API"] != "env:API_TOKEN" {
		t.Fatalf("Secrets = %#v", opts.Secrets)
	}
	if len(opts.SecretEnvFiles) != 1 || opts.SecretEnvFiles[0] != "/tmp/app.env" {
		t.Fatalf("SecretEnvFiles = %#v", opts.SecretEnvFiles)
	}
	if opts.OnDemandSecrets["DB"] != "env:DB_TOKEN" || !opts.SecretsAudit {
		t.Fatalf("OnDemandSecrets = %#v SecretsAudit = %t", opts.OnDemandSecrets, opts.SecretsAudit)
	}
}

func TestParseWorkspaceOptionsRejectsDuplicateSecretFlags(t *testing.T) {
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--secret", "API=env:API_TOKEN",
		"--secret", "API=env:OTHER_TOKEN",
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate secret name") {
		t.Fatalf("err = %v, want duplicate secret validation", err)
	}
}

func TestParseWorkspaceOptionsRunAcceptsContainerFlagsAfterImage(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"docker.io/library/busybox:1.36",
		"--env", "GREETING=hello",
		"--publish", "127.0.0.1:18080:8080/tcp",
		"printenv",
		"GREETING",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/busybox:1.36" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.Env["GREETING"] != "hello" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.ExecCommand != "exec 'printenv' 'GREETING'" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if len(opts.Network.PortForwards) != 1 {
		t.Fatalf("PortForwards = %#v", opts.Network.PortForwards)
	}
	forward := opts.Network.PortForwards[0]
	if forward.Host != "127.0.0.1" || forward.HostPort != 18080 || forward.GuestPort != 8080 || forward.Protocol != "tcp" {
		t.Fatalf("PortForward = %#v", forward)
	}
}

// A guest command's own flags must reach the guest, not be lifted out by
// reorderFlagArgs as if they were microagent's flags. Regression guard for the
// registry-login flag reordering report: `run <image> <cmd> <guest-flags>` keeps the
// guest flags in the command tail.
func TestParseWorkspaceOptionsRunKeepsGuestCommandFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "id -u",
			args: []string{"docker.io/library/alpine:3.20", "id", "-u"},
			want: "exec 'id' '-u'",
		},
		{
			name: "docker login --password-stdin",
			args: []string{"docker.io/library/alpine:3.20", "docker", "login", "--password-stdin"},
			want: "exec 'docker' 'login' '--password-stdin'",
		},
		{
			name: "short and long unknown flags",
			args: []string{"docker.io/library/alpine:3.20", "mytool", "-u", "--username", "bob"},
			want: "exec 'mytool' '-u' '--username' 'bob'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parseWorkspaceOptions("run", os.Stdout, tc.args)
			if err != nil {
				t.Fatalf("parseWorkspaceOptions: %v", err)
			}
			if opts.ExecCommand != tc.want {
				t.Fatalf("ExecCommand = %q, want %q", opts.ExecCommand, tc.want)
			}
		})
	}
}

// The registry-login reorderer hoists its OWN flags ahead of the <registry>
// positional (so a flag may come after it) but leaves any other token — including a
// flag it doesn't own — in place, so it can't disturb another command's arguments.
func TestReorderRegistryLoginArgs(t *testing.T) {
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := reorderRegistryLoginArgs([]string{"ghcr.io", "--username", "bob", "--password-stdin"}); !eq(got, []string{"-username", "bob", "-password-stdin", "ghcr.io"}) {
		t.Fatalf("login flags after the positional should hoist: %#v", got)
	}
	if got := reorderRegistryLoginArgs([]string{"--username", "bob", "ghcr.io"}); !eq(got, []string{"-username", "bob", "ghcr.io"}) {
		t.Fatalf("login flags before the positional should be preserved: %#v", got)
	}
	// A flag the registry command doesn't own is left untouched (not lifted).
	if got := reorderRegistryLoginArgs([]string{"ghcr.io", "-v", "x"}); !eq(got, []string{"ghcr.io", "-v", "x"}) {
		t.Fatalf("unowned flags must be left in place: %#v", got)
	}
}

func TestParseWorkspaceOptionsDoesNotDiscoverDefaultSpecFile(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "microagent.yaml"), []byte("name: default-spec\nentrypoint: /from-spec\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{"--name", "explicit"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "explicit" || opts.Entrypoint != "" {
		t.Fatalf("opts = %#v", opts)
	}
}

func TestRunProfilesPrintsExactConfigs(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "profiles.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "profiles"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run profiles: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"name": "medium"`) ||
		!strings.Contains(text, `"memory_mib": 2048`) ||
		!strings.Contains(text, `"size_mib": 8192`) {
		t.Fatalf("profiles output = %s", data)
	}
}
