//go:build linux

package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func TestRunConnectsSerialInputToFirecrackerStdin(t *testing.T) {
	dir := t.TempDir()
	fakeFirecracker := filepath.Join(dir, "firecracker")
	script := `#!/bin/sh
printf 'ready\n'
IFS= read -r line
printf 'got:%s\n' "$line"
`
	if err := os.WriteFile(fakeFirecracker, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	req := vmkit.Request{
		Command: "run",
		Identity: &vmkit.Identity{
			RequestID: "req-1",
			RuntimeID: "research",
			Role:      vmkit.RoleWorkload,
			Backend:   vmkit.BackendFirecracker,
		},
		Config: &vmkit.Config{
			KernelPath:  filepath.Join(dir, "Image"),
			RootfsPath:  filepath.Join(dir, "rootfs.ext4"),
			StateDir:    dir,
			MemoryMiB:   128,
			CPUCount:    1,
			SerialInput: true,
		},
	}
	done := make(chan error, 1)
	go func() {
		resp, err := (Supervisor{Options: Options{
			Name:            "research",
			StateDir:        dir,
			FirecrackerPath: fakeFirecracker,
			Timeout:         2 * time.Second,
		}}).Do(context.Background(), req)
		if err != nil {
			done <- err
			return
		}
		if !resp.OK || resp.Event == nil || resp.Event.State != vmkit.StateStopped {
			done <- &unexpectedResponseError{response: resp}
			return
		}
		done <- nil
	}()
	inputPath := filepath.Join(dir, "research", "serial.in")
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(inputPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not appear", inputPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	input, err := os.OpenFile(inputPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.WriteString("hello\n"); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("firecracker supervisor did not exit")
	}
	serial, err := os.ReadFile(filepath.Join(dir, "research", "serial.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serial), "ready\n") || !strings.Contains(string(serial), "got:hello\n") {
		t.Fatalf("serial log = %q", serial)
	}
}

type unexpectedResponseError struct {
	response vmkit.Response
}

func (e *unexpectedResponseError) Error() string {
	return "unexpected response"
}
