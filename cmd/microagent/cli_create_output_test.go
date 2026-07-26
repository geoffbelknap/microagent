package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestRunHighLevelCreateDoesNotRenderEmptyResultOnPreflightFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runHighLevelCreate(t.Context(), []string{
		"port-check",
		"--state-dir", dir,
		"--publish", portText + ":80",
		"--size-mib", "512",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "host port 127.0.0.1:"+portText+" is unavailable") {
		t.Fatalf("runHighLevelCreate err = %v", err)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(out), "Workspace:") {
		t.Fatalf("stdout = %q", string(out))
	}
}

func TestRunStartWorkspaceDoesNotRenderEmptyResultOnMissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = runStartWorkspace(t.Context(), []string{"missing", "--state-dir", dir}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "workspace.json") {
		t.Fatalf("runStartWorkspace err = %v", err)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(out), "Workspace:") {
		t.Fatalf("stdout = %q", string(out))
	}
}

func TestFormatProgressEventSupportsIndeterminateGuestSetup(t *testing.T) {
	got := formatProgressEvent(rootfs.ProgressEvent{
		Phase:         "guest-setup",
		Message:       "running guest setup",
		Current:       65,
		Indeterminate: true,
	})
	if !strings.Contains(got, "running guest setup") || !strings.Contains(got, "1m05s") {
		t.Fatalf("progress = %q", got)
	}
}

func TestWriteCreateResultSuppressesSuccessfulSetupLogs(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	result := workspaceResult{
		Workspace:  "homebridge",
		FinalState: string(vmkit.StateStopped),
		Network:    networkSpec{Mode: "user"},
		Result: &guestResult{
			ExitCode: 0,
			Stdout:   "Homebridge Installation Complete!\n",
			Stderr:   "debconf: delaying package configuration\n",
		},
	}
	if err := writeCreateResult(stdout, result, nil); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "Homebridge Installation Complete") || strings.Contains(text, "debconf") || strings.Contains(text, "Exit code") {
		t.Fatalf("create output included setup logs: %q", text)
	}
	if !strings.Contains(text, "Created workspace: homebridge") || !strings.Contains(text, "State: ready (stopped)") || !strings.Contains(text, "Network: user") {
		t.Fatalf("create output missing summary: %q", text)
	}
}

func TestWriteCreateResultKeepsFailedSetupLogs(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	result := workspaceResult{
		Workspace: "homebridge",
		Result: &guestResult{
			ExitCode: 127,
			Stderr:   "setup failed\n",
			Error:    "exit status 127",
		},
	}
	if err := writeCreateResult(stdout, result, errors.New("exit status 127")); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "Exit code: 127") || !strings.Contains(text, "setup failed") {
		t.Fatalf("create output omitted failure logs: %q", text)
	}
}

// runResultStreams runs writeRunResult against temp stdout/stderr files in
// text mode and returns what landed on each stream.
func runResultStreams(t *testing.T, result workspaceResult, keep bool, runErr error) (string, string) {
	t.Helper()
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRunResult(stdout, stderr, result, keep, runErr); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	outData, err := os.ReadFile(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	errData, err := os.ReadFile(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(outData), string(errData)
}

func TestWriteRunResultStdoutCarriesOnlyCommandOutput(t *testing.T) {
	result := workspaceResult{
		Workspace:  "run-brave-otter-4f9c",
		Profile:    "small",
		FinalState: string(vmkit.StateStopped),
		RootfsPath: "/tmp/rootfs.ext4",
		KernelPath: "/tmp/Image",
		Result: &guestResult{
			ExitCode: 0,
			Stdout:   "Linux run-brave-otter 6.1.0\n",
			Stderr:   "a guest warning\n",
		},
	}
	out, errText := runResultStreams(t, result, false, nil)
	if out != "Linux run-brave-otter 6.1.0\n" {
		t.Fatalf("stdout carried more than the command output: %q", out)
	}
	if errText != "a guest warning\n" {
		t.Fatalf("stderr = %q, want guest stderr only", errText)
	}
}

func TestWriteRunResultKeepPrintsWorkspaceOnStderr(t *testing.T) {
	result := workspaceResult{
		Workspace: "run-kept-1",
		Result:    &guestResult{ExitCode: 0, Stdout: "ok\n"},
	}
	out, errText := runResultStreams(t, result, true, nil)
	if out != "ok\n" {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(errText, "Workspace: run-kept-1") {
		t.Fatalf("stderr missing kept workspace name: %q", errText)
	}
}

func TestWriteRunResultFailurePointsAtPreservedState(t *testing.T) {
	result := workspaceResult{
		Workspace:  "run-broken-1",
		SerialPath: "/tmp/serial.log",
		Result:     &guestResult{ExitCode: 1, Stderr: "boom\n"},
	}
	out, errText := runResultStreams(t, result, false, errors.New("run failed"))
	if out != "" {
		t.Fatalf("stdout should be empty on failure without guest stdout: %q", out)
	}
	if !strings.Contains(errText, "boom") ||
		!strings.Contains(errText, "Workspace: run-broken-1") ||
		!strings.Contains(errText, "Console log: /tmp/serial.log") {
		t.Fatalf("stderr missing failure breadcrumbs: %q", errText)
	}
}

func TestWriteDispatchResultSplitsStreamsAndSortsReceipt(t *testing.T) {
	outputFormat = "text"
	t.Cleanup(func() { outputFormat = "" })
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	result := workspace.DispatchResult{
		Workspace:  "dispatch-swift-falcon-9k4t",
		FinalState: string(vmkit.StateStopped),
		Result:     &guestResult{ExitCode: 0, Stdout: "4\n"},
		Audit: workspace.EgressAuditSummary{
			DecisionCount: 3,
			AllowByHost:   map[string]int{"b.example.com": 1, "a.example.com": 2},
		},
	}
	if err := writeDispatchResult(stdout, stderr, result); err != nil {
		t.Fatal(err)
	}
	if err := stdout.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	outData, err := os.ReadFile(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	errData, err := os.ReadFile(filepath.Join(dir, "stderr.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(outData) != "4\n" {
		t.Fatalf("stdout carried more than the task output: %q", string(outData))
	}
	errText := string(errData)
	if !strings.Contains(errText, "Egress: 3 decision(s)") {
		t.Fatalf("stderr missing egress receipt: %q", errText)
	}
	aIdx := strings.Index(errText, "allow a.example.com (2)")
	bIdx := strings.Index(errText, "allow b.example.com (1)")
	if aIdx == -1 || bIdx == -1 || aIdx > bIdx {
		t.Fatalf("receipt hosts missing or unsorted: %q", errText)
	}
}

func TestGuestExitError(t *testing.T) {
	if err := guestExitError(nil); err != nil {
		t.Fatalf("nil result: %v", err)
	}
	if err := guestExitError(&guestResult{ExitCode: 0}); err != nil {
		t.Fatalf("exit 0: %v", err)
	}
	err := guestExitError(&guestResult{ExitCode: 7})
	var exitErr cliExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || !exitErr.Silent {
		t.Fatalf("exit 7 = %#v, want silent cliExitError code 7", err)
	}
}
