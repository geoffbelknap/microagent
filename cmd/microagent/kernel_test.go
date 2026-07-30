package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/kernel"
	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestKernelCommandsRejectUnsafeArchitectureBeforeIO(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "Image")
	if err := os.WriteFile(source, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdout.Close() })

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "install",
			run: func() error {
				return runKernelInstall(context.Background(), []string{"--from", source, "--arch", "../../../escaped"}, stdout)
			},
		},
		{name: "verify", run: func() error {
			return runKernelVerify([]string{"--path", source, "--arch", "../../../escaped"}, stdout)
		}},
		{name: "list", run: func() error {
			return runKernelList([]string{"--arch", "../../../escaped"}, stdout)
		}},
		{name: "check", run: func() error {
			return runKernelCheck([]string{"--arch", "../../../escaped"}, stdout)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !operation.IsKind(err, operation.ErrorValidation) {
				t.Fatalf("error = %#v, want typed validation error", err)
			}
		})
	}
}

func TestKernelVerifyDerivesDefaultPathAfterArchitectureParsing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	arch := "arm64"
	alias := "aarch64"
	if defaultGuestArch() == arch {
		arch = "amd64"
		alias = "x86_64"
	}
	path := workspace.WritableKernelPath(hostBackend(), arch)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("kernel bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, err := os.Create(filepath.Join(home, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdout.Close() })
	if err := runKernelVerify([]string{"--arch", alias}, stdout); err != nil {
		t.Fatalf("runKernelVerify: %v", err)
	}
	if _, err := stdout.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var result kernel.VerifyResult
	if err := json.NewDecoder(stdout).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Path != path {
		t.Fatalf("verify path = %q, want %q", result.Path, path)
	}
}
