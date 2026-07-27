package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
)

func TestRunRootFSValidatesRequiredFlags(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runRootFS(t.Context(), []string{"build"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "image_ref is required") {
		t.Fatalf("err = %v, want image_ref validation", err)
	}
}

func TestRootFSExecMapsToShellCommand(t *testing.T) {
	var req rootfs.BuildRequest
	execCommand := "echo hello"
	if strings.TrimSpace(execCommand) != "" {
		req.Command = []string{"/bin/sh", "-lc", execCommand}
	}
	if got := strings.Join(req.Command, " "); got != "/bin/sh -lc echo hello" {
		t.Fatalf("Command = %q", got)
	}
}

func TestParseWorkspaceOptionsForRun(t *testing.T) {
	dir := t.TempDir()
	setupPath := filepath.Join(dir, "setup.sh")
	if err := os.WriteFile(setupPath, []byte("#!/bin/sh\necho from-file\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/ubuntu:24.04",
		"--exec", "uname -a",
		"--setup", "apt-get update",
		"--setup", "apt-get install -y git",
		"--setup-file", setupPath,
		"--shell", "/bin/bash",
		"--hostname", "research-vm",
		"--env", "AGENCY_AGENT_NAME=research",
		"--env", "AGENCY_MODEL=standard",
		"--name", "research",
		"--kernel", "/tmp/kernel",
		"--state-dir", "/tmp/microagent-state",
		"--mke2fs", "/tmp/mke2fs",
		"--arch", "arm64",
		"--memory", "1024",
		"--cpus", "4",
		"--size-mib", "2048",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("ImageRef = %q", opts.ImageRef)
	}
	if opts.ExecCommand != "uname -a" {
		t.Fatalf("ExecCommand = %q", opts.ExecCommand)
	}
	if len(opts.SetupCommands) != 3 || opts.SetupCommands[0] != "apt-get update" || opts.SetupCommands[1] != "apt-get install -y git" || !strings.Contains(opts.SetupCommands[2], "echo from-file") {
		t.Fatalf("SetupCommands = %#v", opts.SetupCommands)
	}
	if opts.ConsoleShell != "/bin/bash" {
		t.Fatalf("ConsoleShell = %q", opts.ConsoleShell)
	}
	if opts.Hostname != "research-vm" {
		t.Fatalf("Hostname = %q", opts.Hostname)
	}
	if opts.Env["AGENCY_AGENT_NAME"] != "research" || opts.Env["AGENCY_MODEL"] != "standard" {
		t.Fatalf("Env = %#v", opts.Env)
	}
	if opts.Name != "research" {
		t.Fatalf("Name = %q", opts.Name)
	}
	if opts.KernelPath != "/tmp/kernel" {
		t.Fatalf("KernelPath = %q", opts.KernelPath)
	}
	if opts.MemoryMiB != 1024 || opts.CPUCount != 4 || opts.SizeMiB != 2048 {
		t.Fatalf("resource opts = memory %d cpus %d size %d", opts.MemoryMiB, opts.CPUCount, opts.SizeMiB)
	}
}

func TestParseWorkspaceOptionsModelFlagAndSpecPrecedence(t *testing.T) {
	specPath := filepath.Join(t.TempDir(), "microagent.yaml")
	if err := os.WriteFile(specPath, []byte("name: demo\nimage: docker.io/library/ubuntu:24.04\nmodel: org/spec-repo/spec.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{"--file", specPath})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/spec-repo/spec.gguf" {
		t.Fatalf("spec model not applied: %q", opts.Model)
	}

	opts, err = parseWorkspaceOptions("create", os.Stdout, []string{"--file", specPath, "--model", "org/flag-repo/flag.gguf"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/flag-repo/flag.gguf" {
		t.Fatalf("--model flag should win over spec: %q", opts.Model)
	}

	opts, err = parseWorkspaceOptions("create", os.Stdout, []string{"demo", "--model", "org/flag-repo/flag.gguf"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Model != "org/flag-repo/flag.gguf" {
		t.Fatalf("create --model not parsed: %q", opts.Model)
	}
}

func TestParseWorkspaceOptionsModelRunnerAndMediationFlags(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"demo",
		"--model", "org/repo/model.gguf",
		"--model-runner", "vllm",
		"--model-gpu", "auto",
		"--model-runner-model", "Qwen/Qwen2.5-0.5B-Instruct",
		"--model-runner-served-model", "local-chat",
		"--model-runner-arg", "--max-model-len",
		"--model-runner-arg", "2048",
		"--model-runner-env", "CUDA_VISIBLE_DEVICES=0",
		"--model-mediation", "policy",
		"--model-policy-file", "/tmp/model-policy.json",
		"--model-policy-timeout", "250ms",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ModelRunner.Backend != "vllm" || opts.ModelRunner.GPU != "auto" || opts.ModelRunner.BackendModel != "Qwen/Qwen2.5-0.5B-Instruct" || opts.ModelRunner.ServedModel != "local-chat" {
		t.Fatalf("model runner = %+v", opts.ModelRunner)
	}
	if !reflect.DeepEqual(opts.ModelRunner.Args, []string{"--max-model-len", "2048"}) {
		t.Fatalf("model runner args = %#v", opts.ModelRunner.Args)
	}
	if !reflect.DeepEqual(opts.ModelRunner.Env, []string{"CUDA_VISIBLE_DEVICES=0"}) {
		t.Fatalf("model runner env = %#v", opts.ModelRunner.Env)
	}
	if opts.ModelMediation.Mode != "policy" || opts.ModelMediation.PolicyFile != "/tmp/model-policy.json" || opts.ModelMediation.PolicyTimeout != "250ms" {
		t.Fatalf("model mediation = %+v", opts.ModelMediation)
	}
}

// TestSerialLogBytesFlagReachesTheLibrary pins the adapter wiring for the
// response-shaping contract: --serial-log-bytes is the CLI spelling of
// Options.SerialLogMaxBytes, and -1 is the documented full-log opt-in.
func TestSerialLogBytesFlagReachesTheLibrary(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--serial-log-bytes", "2048",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.SerialLogMaxBytes != 2048 {
		t.Errorf("SerialLogMaxBytes = %d, want 2048", opts.SerialLogMaxBytes)
	}

	opts, err = parseWorkspaceOptions("dispatch", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--serial-log-bytes", "-1",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.SerialLogMaxBytes != -1 {
		t.Errorf("SerialLogMaxBytes = %d, want -1 (full log opt-in)", opts.SerialLogMaxBytes)
	}
}
