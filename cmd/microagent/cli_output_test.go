package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestWriteDoctorResponseTextIncludesNetworkingSection(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	f, err := os.CreateTemp(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	resp := vmkit.Response{
		OK:      true,
		Backend: "linux-kvm",
		Host: &vmkit.HostSupport{
			Backend:              "linux-kvm",
			Architecture:         "amd64",
			IsolatedNetworkReady: true,
			UserNetworkReady:     true,
		},
	}
	if err := writeDoctorResponse(f, resp); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Name())
	out := string(data)
	if !strings.Contains(out, "Networking: isolated ready, user ready") {
		t.Errorf("expected a Networking section advertising isolated+user, got:\n%s", out)
	}
	if strings.Contains(out, "setup-networking") {
		t.Errorf("doctor must not reference the removed setup-networking command, got:\n%s", out)
	}
}

func TestWriteDoctorResponseTextAppleVFNetworkingDoesNotSuggestLinuxSetup(t *testing.T) {
	oldOutput := outputFormat
	t.Cleanup(func() { outputFormat = oldOutput })
	outputFormat = "text"
	f, err := os.CreateTemp(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	resp := vmkit.Response{
		OK:      true,
		Backend: vmkit.BackendAppleVF,
		Host: &vmkit.HostSupport{
			Backend:                 vmkit.BackendAppleVF,
			Architecture:            "arm64",
			FrameworkAvailable:      true,
			VirtualizationSupported: true,
			SupervisorAvailable:     true,
			ConsoleAvailable:        true,
			ConsoleMode:             "interactive",
		},
	}
	if err := writeDoctorResponse(f, resp); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Name())
	out := string(data)
	if !strings.Contains(out, "Networking: isolated ready, user ready") {
		t.Errorf("expected apple-vf networking readiness, got:\n%s", out)
	}
	if strings.Contains(out, "setup-networking") {
		t.Errorf("apple-vf doctor should not suggest Linux setup-networking, got:\n%s", out)
	}
}

func TestParseEgressMode(t *testing.T) {
	cases := map[string]string{
		"": "broker", "broker": "broker", "BROKER": "broker",
		"mitm": "mitm", "MITM": "mitm",
		"off": "off", "OFF": "off",
	}
	for in, want := range cases {
		got, err := parseEgressMode(in)
		if err != nil || got != want {
			t.Fatalf("parseEgressMode(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	// The retired names and any junk must error — no silent fallback (tenet 9).
	for _, bad := range []string{"guarded", "strict", "bogus", "mediated", "open", "disabled"} {
		if _, err := parseEgressMode(bad); err == nil {
			t.Fatalf("expected error for %q mode", bad)
		}
	}
}

func writeEgressPolicyFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestEgressPolicyFileMergesWithFlags asserts the policy file's allow/passthrough
// lists are unioned with --egress-allow/--egress-passthrough and deduped
// case-insensitively. Precedence is additive (default-deny means a file can only
// ADD reachability), so order-independent union with dedupe is correct.
func TestEgressPolicyFileMergesWithFlags(t *testing.T) {
	policy := writeEgressPolicyFile(t, "policy.yaml", `
allow:
  - api.github.com
  - .example.com
passthrough:
  - raw.example.com
`)
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--egress", "mitm",
		"--egress-allow", "API.GitHub.com", // dup of file entry, different case
		"--egress-allow", "extra.com",
		"--egress-passthrough", "raw.example.com", // dup of file entry
		"--egress-policy", policy,
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	// Union of flag {api.github.com, extra.com} and file {api.github.com,
	// .example.com}, deduped case-insensitively -> 3 entries.
	if len(opts.EgressAllow) != 3 {
		t.Fatalf("EgressAllow = %v, want 3 deduped entries", opts.EgressAllow)
	}
	wantAllow := map[string]bool{"api.github.com": false, "extra.com": false, ".example.com": false}
	for _, h := range opts.EgressAllow {
		if _, ok := wantAllow[strings.ToLower(h)]; !ok {
			t.Fatalf("unexpected allow entry %q in %v", h, opts.EgressAllow)
		}
		wantAllow[strings.ToLower(h)] = true
	}
	for h, seen := range wantAllow {
		if !seen {
			t.Fatalf("missing allow entry %q in %v", h, opts.EgressAllow)
		}
	}
	if len(opts.EgressPassthrough) != 1 || opts.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("EgressPassthrough = %v, want [raw.example.com] (deduped)", opts.EgressPassthrough)
	}
}

func TestEgressPolicyFileJSON(t *testing.T) {
	policy := writeEgressPolicyFile(t, "policy.json", `{"allow":["api.github.com"],"passthrough":["raw.example.com"]}`)
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--egress", "broker",
		"--egress-policy", policy,
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if len(opts.EgressAllow) != 1 || opts.EgressAllow[0] != "api.github.com" {
		t.Fatalf("EgressAllow = %v", opts.EgressAllow)
	}
	if len(opts.EgressPassthrough) != 1 || opts.EgressPassthrough[0] != "raw.example.com" {
		t.Fatalf("EgressPassthrough = %v", opts.EgressPassthrough)
	}
}

// TestEgressPolicyFileRejectedWhenOff asserts a policy file is rejected when
// mediation is off — a policy is meaningless without a mediator to enforce it.
func TestEgressPolicyFileRejectedWhenOff(t *testing.T) {
	policy := writeEgressPolicyFile(t, "policy.yaml", "allow: [api.github.com]\n")
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--egress", "off",
		"--egress-policy", policy,
	})
	if err == nil {
		t.Fatal("expected error for --egress-policy with --egress off")
	}
	if !strings.Contains(err.Error(), "broker") || !strings.Contains(err.Error(), "mitm") {
		t.Fatalf("error %q should explain broker/mitm requirement", err)
	}
}

func TestEgressPolicyFileLoadErrorPropagates(t *testing.T) {
	policy := writeEgressPolicyFile(t, "policy.yaml", "allowed: [api.github.com]\n") // typo -> unknown key
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--egress", "mitm",
		"--egress-policy", policy,
	})
	if err == nil {
		t.Fatal("expected error for policy file with unknown key")
	}
}

// TestEgressPolicyFileUnionWithManifest confirms the file/flag union (resolved
// CLI-side into opts) further unions with a manifest's egress lists through
// OptionsFromManifest, and the manifest's entries survive. This is the
// create-then-start path: flags+file produce a manifest, which on start unions
// into Config. Here we assert the manifest-side union directly.
func TestEgressPolicyFileUnionWithManifest(t *testing.T) {
	policy := writeEgressPolicyFile(t, "policy.yaml", "allow: [file.example.com]\n")
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"research",
		"--egress", "mitm",
		"--egress-allow", "flag.example.com",
		"--egress-policy", policy,
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	// The CLI-resolved union (flag + file) must contain both.
	got := map[string]bool{}
	for _, h := range opts.EgressAllow {
		got[strings.ToLower(h)] = true
	}
	if !got["flag.example.com"] || !got["file.example.com"] {
		t.Fatalf("CLI union missing entries: %v", opts.EgressAllow)
	}
}

func TestParseEgressModeDefaults(t *testing.T) {
	for in, want := range map[string]string{"": "broker", "broker": "broker", "mitm": "mitm"} {
		got, err := parseEgressMode(in)
		if err != nil || got != want {
			t.Errorf("parseEgressMode(%q)=%q,%v want %q", in, got, err, want)
		}
	}
	if _, err := parseEgressMode("nonsense"); err == nil {
		t.Error("expected error for unknown egress mode")
	}
}

func TestRunRmForcesDiscard(t *testing.T) {
	// run --rm must set opts.Keep = false (--rm is the explicit discard; currently
	// it is a no-op that never touches opts.Keep, so this test should FAIL until
	// the wiring is added).
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--rm",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions run --rm: %v", err)
	}
	if opts.Keep {
		t.Error("run --rm: opts.Keep = true, want false (--rm must force discard)")
	}
}

func TestRunKeepSetsKeep(t *testing.T) {
	// run --keep must retain the workspace (opts.Keep = true).
	opts, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--keep",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions run --keep: %v", err)
	}
	if !opts.Keep {
		t.Error("run --keep: opts.Keep = false, want true")
	}
}

func TestRunRmAndKeepErrors(t *testing.T) {
	// run --rm --keep must be rejected as a mutual exclusion error.
	_, err := parseWorkspaceOptions("run", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--rm",
		"--keep",
	})
	if err == nil {
		t.Error("run --rm --keep: expected error, got nil")
	}
}

func TestCreateRmErrors(t *testing.T) {
	// --rm is run-only; create --rm must be rejected.
	_, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"--image", "docker.io/library/alpine:3.20",
		"--rm",
	})
	if err == nil {
		t.Error("create --rm: expected error (run-only flag), got nil")
	}
}

func TestStartRmErrors(t *testing.T) {
	// --rm is run-only; start --rm must be rejected.
	_, err := parseWorkspaceOptions("start", os.Stdout, []string{
		"--name", "test-ws",
		"--rm",
	})
	if err == nil {
		t.Error("start --rm: expected error (run-only flag), got nil")
	}
}

func writeWaitCommandState(t *testing.T, name string, state vmkit.VMState) string {
	t.Helper()
	dir := t.TempDir()
	opts := workspace.Options{Name: name, StateDir: dir, Backend: workspace.HostBackend()}
	req, err := workspace.Request(opts, "run", filepath.Join(dir, "rootfs.ext4"), "req-1")
	if err != nil {
		t.Fatalf("workspace.Request: %v", err)
	}
	if err := workspace.WriteProcessState(opts, req, state, 0, ""); err != nil {
		t.Fatalf("WriteProcessState: %v", err)
	}
	return dir
}

func runWaitCommandForTest(t *testing.T, args []string) (string, error) {
	t.Helper()
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	runErr := run(t.Context(), args, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

func TestRunWaitReportsCleanTerminalState(t *testing.T) {
	stateDir := writeWaitCommandState(t, "research", vmkit.StateStopped)
	output, err := runWaitCommandForTest(t, []string{"--json", "wait", "research", "--state-dir", stateDir})
	if err != nil {
		t.Fatalf("run wait: %v", err)
	}
	var result waitResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("wait output = %q: %v", output, err)
	}
	if result.Workspace != "research" || result.State != string(vmkit.StateStopped) || !result.OK {
		t.Fatalf("wait result = %#v", result)
	}
}

func TestRunWaitFailedStateExitsNonzeroAfterWritingResult(t *testing.T) {
	stateDir := writeWaitCommandState(t, "research", vmkit.StateFailed)
	output, err := runWaitCommandForTest(t, []string{"--json", "wait", "research", "--state-dir", stateDir})
	var exitErr cliExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 || !exitErr.Silent {
		t.Fatalf("run wait err = %#v, want silent exit 1", err)
	}
	var result waitResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("wait output = %q: %v", output, err)
	}
	if result.State != string(vmkit.StateFailed) || result.OK {
		t.Fatalf("wait result = %#v", result)
	}
}

func TestRunWaitTimeoutSurfacesRetryableError(t *testing.T) {
	stateDir := writeWaitCommandState(t, "research", vmkit.StateStopped)
	// A negative timeout is rejected before any wait begins.
	if _, err := runWaitCommandForTest(t, []string{"wait", "research", "--state-dir", stateDir, "--timeout", "-1s"}); err == nil {
		t.Fatal("run wait --timeout -1s: expected error, got nil")
	}
}

func TestRunWaitRequiresWorkspaceName(t *testing.T) {
	if _, err := runWaitCommandForTest(t, []string{"wait"}); err == nil || !strings.Contains(err.Error(), "usage: microagent wait") {
		t.Fatalf("run wait err = %v, want usage error", err)
	}
}

func TestRunWaitMissingWorkspaceReturnsNotFound(t *testing.T) {
	if _, err := runWaitCommandForTest(t, []string{"wait", "missing", "--state-dir", t.TempDir()}); !errors.Is(err, workspace.WorkspaceNotFoundError{}) {
		t.Fatalf("run wait err = %v, want WorkspaceNotFoundError", err)
	}
}

func TestStartWaitFlagsParse(t *testing.T) {
	// start --wait is gated on the same positional-name detection as plain
	// start; a trailing --wait must not break it.
	if !hasPositionalWorkspaceName([]string{"research", "--wait"}) {
		t.Fatal("hasPositionalWorkspaceName(research --wait) = false")
	}
	if !hasPositionalWorkspaceName([]string{"--wait", "research"}) {
		t.Fatal("hasPositionalWorkspaceName(--wait research) = false")
	}
	reordered := reorderFlagArgs([]string{"research", "--wait", "--wait-timeout", "5m"})
	want := []string{"-wait", "-wait-timeout", "5m", "research"}
	if strings.Join(reordered, " ") != strings.Join(want, " ") {
		t.Fatalf("reorderFlagArgs = %v, want %v", reordered, want)
	}
}
