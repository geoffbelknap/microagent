# Host Networking Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `nat`/`bridged`/`named` networking discoverable and one-command to enable on an installed microagent: a `microagent host setup-networking` command, a `doctor` networking-readiness section, Homebrew caveats, and e2e runner remediation messaging.

**Architecture:** Networking facts (ip_forward, supervisor file-`CAP_NET_ADMIN`, passt) are gathered in the cross-platform `pkg/diagnostics` package via injected probes and stored on `vmkit.HostSupport`; a pure derivation maps them to per-mode readiness + a remediation string that `doctor` renders. The privileged apply (`setcap` + persist `ip_forward`) lives in a Linux-only command file with a non-Linux stub. `host` is restructured to dispatch subcommands (mirroring `kernel`).

**Tech Stack:** Go (stdlib `flag`, `golang.org/x/sys/unix` for xattr/caps), bash (e2e runner), Ruby (Homebrew formula — separate repo/PR).

Spec: `docs/specs/2026-06-02-host-networking-setup-design.md`.

**Conventions for every task:** build/test with the shared cache —
`export GOCACHE="$PWD/.cache/microagent-e2e/go-build" GOMODCACHE="$PWD/.cache/microagent-e2e/gomodcache" GOFLAGS="-modcacherw"`. Work on branch `host-networking-setup` (already created; the design doc is already committed there).

---

### Task 1: Add networking fields to `HostSupport`

**Files:**
- Modify: `pkg/vmkit/types.go:152-173`

- [ ] **Step 1: Add fields to the struct**

In `pkg/vmkit/types.go`, append these fields inside the `HostSupport` struct, after `TunAvailable` (line 173):

```go
	// Privileged-networking readiness (Linux/Firecracker). nat/bridged/named
	// require IPv4 forwarding and the supervisor binary holding CAP_NET_ADMIN.
	IPForwardEnabled          bool `json:"ipForwardEnabled,omitempty"`
	SupervisorNetAdminCapable bool `json:"supervisorNetAdminCapable,omitempty"`
	IsolatedNetworkReady      bool `json:"isolatedNetworkReady,omitempty"`
	UserNetworkReady          bool `json:"userNetworkReady,omitempty"`
	PrivilegedNetworkReady    bool `json:"privilegedNetworkReady,omitempty"`
```

`PrivilegedNetworkReady` covers nat/bridged/named together (they share the same prerequisites). `UserNetworkReady` mirrors `UserNetworkingAvailable` but is named for the mode; keep both so the derivation has one clear output surface.

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./pkg/vmkit/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add pkg/vmkit/types.go
git commit -m "vmkit: add privileged-networking readiness fields to HostSupport"
```

---

### Task 2: FILE-capability reader for the supervisor binary

A binary's capabilities live in the `security.capability` xattr (set by `setcap`), distinct from the running process's capabilities. New linux reader + non-linux stub.

**Files:**
- Create: `pkg/diagnostics/capabilities_linux.go`
- Create: `pkg/diagnostics/capabilities_other.go`
- Test: `pkg/diagnostics/capabilities_linux_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/diagnostics/capabilities_linux_test.go`:

```go
//go:build linux

package diagnostics

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBinaryHasNetAdmin(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("setting file capabilities requires root")
	}
	if _, err := exec.LookPath("setcap"); err != nil {
		t.Skip("setcap not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-supervisor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// No caps yet.
	ok, err := BinaryHasNetAdmin(bin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fresh binary should not report CAP_NET_ADMIN")
	}

	// Grant the cap and re-check.
	if out, err := exec.Command("setcap", "cap_net_admin+eip", bin).CombinedOutput(); err != nil {
		t.Fatalf("setcap failed: %v: %s", err, out)
	}
	ok, err = BinaryHasNetAdmin(bin)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("binary with cap_net_admin+eip should report CAP_NET_ADMIN")
	}
}

func TestBinaryHasNetAdminMissingFileIsNotError(t *testing.T) {
	ok, err := BinaryHasNetAdmin(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing binary should not error, got %v", err)
	}
	if ok {
		t.Fatal("missing binary should report no capability")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diagnostics/ -run TestBinaryHasNetAdmin -v`
Expected: FAIL — `undefined: BinaryHasNetAdmin`.

- [ ] **Step 3: Implement the linux reader**

`pkg/diagnostics/capabilities_linux.go`:

```go
//go:build linux

package diagnostics

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

// capNetAdmin is the capability number for CAP_NET_ADMIN (caps 0-31 live in the
// first 32-bit word of the VFS capability data).
const capNetAdmin = 12

// BinaryHasNetAdmin reports whether the on-disk binary at path carries
// CAP_NET_ADMIN in its permitted file-capability set (as written by
// `setcap cap_net_admin+...`). A missing binary or missing capability xattr is
// reported as false with no error; only unexpected I/O errors are returned.
func BinaryHasNetAdmin(path string) (bool, error) {
	// vfs_cap_data is at most 24 bytes (revision 3 with rootid); 64 is ample.
	buf := make([]byte, 64)
	n, err := unix.Getxattr(path, "security.capability", buf)
	if err != nil {
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTSUP) {
			return false, nil
		}
		return false, err
	}
	if n < 8 {
		return false, nil
	}
	// Layout (little-endian): magic_etc(u32), then data[]: {permitted(u32), inheritable(u32)} per 32-cap word.
	// CAP_NET_ADMIN is in the first word, so data[0].permitted is bytes [4,8).
	permitted := binary.LittleEndian.Uint32(buf[4:8])
	return permitted&(1<<uint(capNetAdmin)) != 0, nil
}
```

- [ ] **Step 4: Implement the non-linux stub**

`pkg/diagnostics/capabilities_other.go`:

```go
//go:build !linux

package diagnostics

// BinaryHasNetAdmin is a no-op on non-Linux platforms: file capabilities are a
// Linux concept and privileged Firecracker networking is Linux-only.
func BinaryHasNetAdmin(path string) (bool, error) {
	return false, nil
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/diagnostics/ -run TestBinaryHasNetAdmin -v`
Expected: PASS (the cap-setting test SKIPs unless run as root with `setcap`; the missing-file test PASSes).

Run: `GOOS=darwin go build ./pkg/diagnostics/`
Expected: success (stub compiles).

- [ ] **Step 6: Commit**

```bash
git add pkg/diagnostics/capabilities_linux.go pkg/diagnostics/capabilities_other.go pkg/diagnostics/capabilities_linux_test.go
git commit -m "diagnostics: read CAP_NET_ADMIN from a binary's file capabilities"
```

---

### Task 3: Per-mode readiness derivation (pure function)

**Files:**
- Create: `pkg/diagnostics/networking.go`
- Test: `pkg/diagnostics/networking_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/diagnostics/networking_test.go`:

```go
package diagnostics

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestDeriveNetworkReadiness(t *testing.T) {
	cases := []struct {
		name                                       string
		ipForward, cap, passt                      bool
		wantUser, wantPrivileged                   bool
	}{
		{"nothing", false, false, false, false, false},
		{"passt only", false, false, true, true, false},
		{"forward only", true, false, false, false, false},
		{"cap only", false, true, false, false, false},
		{"forward+cap", true, true, false, false, true},
		{"all", true, true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &vmkit.HostSupport{
				IPForwardEnabled:          c.ipForward,
				SupervisorNetAdminCapable: c.cap,
				UserNetworkingAvailable:   c.passt,
			}
			DeriveNetworkReadiness(h)
			if !h.IsolatedNetworkReady {
				t.Error("isolated must always be ready")
			}
			if h.UserNetworkReady != c.wantUser {
				t.Errorf("user ready = %v, want %v", h.UserNetworkReady, c.wantUser)
			}
			if h.PrivilegedNetworkReady != c.wantPrivileged {
				t.Errorf("privileged ready = %v, want %v", h.PrivilegedNetworkReady, c.wantPrivileged)
			}
		})
	}
}

func TestNetworkRemediation(t *testing.T) {
	// Ready: no remediation.
	ready := &vmkit.HostSupport{PrivilegedNetworkReady: true}
	if got := NetworkRemediation(ready); got != "" {
		t.Errorf("ready host should have no remediation, got %q", got)
	}
	// Forwarding on but cap missing -> post-upgrade phrasing.
	upgraded := &vmkit.HostSupport{IPForwardEnabled: true, SupervisorNetAdminCapable: false}
	if got := NetworkRemediation(upgraded); !strings.Contains(got, "CAP_NET_ADMIN") || !strings.Contains(got, "setup-networking") {
		t.Errorf("missing-cap remediation = %q, want CAP_NET_ADMIN + setup-networking hint", got)
	}
	// Nothing set -> generic remediation.
	none := &vmkit.HostSupport{}
	if got := NetworkRemediation(none); !strings.Contains(got, "sudo microagent host setup-networking") {
		t.Errorf("generic remediation = %q, want setup-networking command", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diagnostics/ -run 'TestDeriveNetworkReadiness|TestNetworkRemediation' -v`
Expected: FAIL — `undefined: DeriveNetworkReadiness` / `undefined: NetworkRemediation`.

- [ ] **Step 3: Implement the derivation**

`pkg/diagnostics/networking.go`:

```go
package diagnostics

import "github.com/geoffbelknap/microagent/pkg/vmkit"

// DeriveNetworkReadiness fills the per-mode readiness fields on host from the
// gathered facts. isolated always works; user works with passt; nat/bridged/named
// need IPv4 forwarding and the supervisor holding CAP_NET_ADMIN.
func DeriveNetworkReadiness(host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	host.IsolatedNetworkReady = true
	host.UserNetworkReady = host.UserNetworkingAvailable
	host.PrivilegedNetworkReady = host.IPForwardEnabled && host.SupervisorNetAdminCapable
}

// NetworkRemediation returns a one-line hint for enabling privileged networking,
// or "" when nat/bridged/named are already usable.
func NetworkRemediation(host *vmkit.HostSupport) string {
	if host == nil || host.PrivilegedNetworkReady {
		return ""
	}
	if host.IPForwardEnabled && !host.SupervisorNetAdminCapable {
		return "nat/bridged/named networking unavailable: the supervisor lacks CAP_NET_ADMIN (a 'brew upgrade' resets it). Re-run: sudo microagent host setup-networking"
	}
	return "nat/bridged/named networking unavailable. Run: sudo microagent host setup-networking"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/diagnostics/ -run 'TestDeriveNetworkReadiness|TestNetworkRemediation' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/diagnostics/networking.go pkg/diagnostics/networking_test.go
git commit -m "diagnostics: derive per-mode network readiness + remediation"
```

---

### Task 4: Gather networking facts in `CheckFirecracker`

Wire ip_forward (via existing `ReadFile` probe), supervisor file-cap (via a new probe field defaulting to `BinaryHasNetAdmin`), and call `DeriveNetworkReadiness`. passt presence is already in `UserNetworkingAvailable`.

**Files:**
- Modify: `pkg/diagnostics/diagnostics.go` (add `ReadBinaryCapabilities` to `FirecrackerProbe`; populate fields in `CheckFirecracker` after `host.SupervisorPath` is set ~line 216-259; call `DeriveNetworkReadiness` before returning)
- Test: `pkg/diagnostics/diagnostics_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/diagnostics/diagnostics_test.go`:

```go
func TestCheckFirecrackerGathersNetworkingFacts(t *testing.T) {
	probe := FirecrackerProbe{
		ResolveBinary:     func() (string, error) { return "/fc", nil },
		ResolveSupervisor: func(Options) (string, error) { return "/sup", nil },
		ResolveGuestInit:  func(Options) (string, error) { return "/init", nil },
		Stat:              func(string) (os.FileInfo, error) { return nil, nil },
		BinaryVersion:     func(string) string { return "Firecracker v1.0.0" },
		LookPath:          func(string) (string, error) { return "/usr/bin/pasta", nil },
		ReadFile: func(path string) ([]byte, error) {
			if path == "/proc/sys/net/ipv4/ip_forward" {
				return []byte("1\n"), nil
			}
			return nil, os.ErrNotExist
		},
		ReadBinaryCapabilities: func(path string) (bool, error) { return true, nil },
	}
	resp, err := CheckFirecracker(Options{Backend: "firecracker", Arch: "amd64"}, probe)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Host.IPForwardEnabled {
		t.Error("expected IPForwardEnabled")
	}
	if !resp.Host.SupervisorNetAdminCapable {
		t.Error("expected SupervisorNetAdminCapable")
	}
	if !resp.Host.PrivilegedNetworkReady {
		t.Error("expected PrivilegedNetworkReady when forward+cap present")
	}
}
```

(Confirm the `os` import exists in the test file; the explorer reported `diagnostics_test.go` already injects `Stat`/`ReadFile`, so it does.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/diagnostics/ -run TestCheckFirecrackerGathersNetworkingFacts -v`
Expected: FAIL — `unknown field 'ReadBinaryCapabilities' in struct literal`.

- [ ] **Step 3: Add the probe field and default**

In `pkg/diagnostics/diagnostics.go`, add to the `FirecrackerProbe` struct (after `ReadFile`):

```go
	ReadBinaryCapabilities func(path string) (bool, error)
```

In `Check` (where the default `FirecrackerProbe` is constructed for the real run — same place `ReadFile: os.ReadFile` is set), add:

```go
		ReadBinaryCapabilities: BinaryHasNetAdmin,
```

- [ ] **Step 4: Populate the fields in `CheckFirecracker`**

In `CheckFirecracker`, after `host.SupervisorPath` is resolved (~line 220) and before the final return, add:

```go
	if probe.ReadFile != nil {
		if data, err := probe.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
			host.IPForwardEnabled = strings.TrimSpace(string(data)) == "1"
		}
	}
	if probe.ReadBinaryCapabilities != nil && host.SupervisorPath != "" {
		if ok, err := probe.ReadBinaryCapabilities(host.SupervisorPath); err == nil {
			host.SupervisorNetAdminCapable = ok
		}
	}
	DeriveNetworkReadiness(host)
```

(Confirm `strings` is imported in `diagnostics.go`; if not, add it.)

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/diagnostics/ -v`
Expected: PASS (new test + existing tests).

- [ ] **Step 6: Commit**

```bash
git add pkg/diagnostics/diagnostics.go pkg/diagnostics/diagnostics_test.go
git commit -m "diagnostics: gather ip_forward + supervisor cap into host networking facts"
```

---

### Task 5: Render the networking section in `doctor` text output

**Files:**
- Modify: `cmd/microagent/main.go` `writeDoctorResponse` (~lines 4175-4218)
- Test: `cmd/microagent/main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/microagent/main_test.go`:

```go
func TestWriteDoctorResponseTextIncludesNetworkingSection(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "doctor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	resp := vmkit.Response{
		OK:      true,
		Backend: "firecracker",
		Host: &vmkit.HostSupport{
			Backend:                "firecracker",
			Architecture:           "amd64",
			IPForwardEnabled:       true,
			IsolatedNetworkReady:   true,
			UserNetworkReady:       true,
			PrivilegedNetworkReady: false, // cap missing
		},
	}
	if err := writeDoctorResponse(f, resp); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(f.Name())
	out := string(data)
	if !strings.Contains(out, "Networking:") {
		t.Errorf("expected a Networking section, got:\n%s", out)
	}
	if !strings.Contains(out, "setup-networking") {
		t.Errorf("expected remediation hint, got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/microagent/ -run TestWriteDoctorResponseTextIncludesNetworkingSection -v`
Expected: FAIL — no "Networking:" in output.

- [ ] **Step 3: Add the networking section to the text renderer**

Add a shared helper (used by both `writeDoctorResponse` and Task 7's `--check`), e.g. near `writeDoctorResponse` in `cmd/microagent/main.go`:

```go
func printNetworkingSection(stdout *os.File, host *vmkit.HostSupport) {
	if host == nil {
		return
	}
	ready := func(b bool) string {
		if b {
			return "ready"
		}
		return "unavailable"
	}
	fmt.Fprintf(stdout, "Networking: isolated %s, user %s, nat/bridged/named %s\n",
		ready(host.IsolatedNetworkReady),
		ready(host.UserNetworkReady),
		ready(host.PrivilegedNetworkReady))
	if hint := diagnostics.NetworkRemediation(host); hint != "" {
		fmt.Fprintf(stdout, "  %s\n", hint)
	}
}
```

Then, in `writeDoctorResponse`'s text branch, after the existing `Host:` line is printed (the explorer noted the Host line ends ~line 4201), call it:

```go
	printNetworkingSection(stdout, resp.Host)
```

(Confirm `diagnostics` is imported in `main.go` — it is, per the explorer, e.g. `diagnostics.Check`.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/microagent/ -run TestWriteDoctorResponse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/microagent/main.go cmd/microagent/main_test.go
git commit -m "doctor: render networking readiness section + remediation"
```

---

### Task 6: Restructure `host` to dispatch subcommands

Make `runHost` mirror `runKernel`: dispatch a leading subcommand, else fall through to the diagnostics report (preserving today's behavior exactly).

**Files:**
- Modify: `cmd/microagent/main.go` `runHost` (lines 316-342)
- Test: `cmd/microagent/main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/microagent/main_test.go`:

```go
func TestHostUnknownSubcommandErrors(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := run(t.Context(), []string{"host", "bogus-subcommand"}, f)
	if err == nil || !strings.Contains(err.Error(), "bogus-subcommand") {
		t.Fatalf("expected unknown host subcommand error, got %v", err)
	}
}

func TestHostNoSubcommandStillReportsDiagnostics(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	// Existing behavior must be preserved: `host` with only flags reports.
	if err := run(t.Context(), []string{"--json", "host", "--backend", hostBackend(), "--arch", defaultGuestArch()}, f); err != nil {
		t.Fatalf("host report should not error: %v", err)
	}
	data, _ := os.ReadFile(f.Name())
	if !strings.Contains(string(data), "\"backend\"") {
		t.Fatalf("expected diagnostics JSON, got: %s", data)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/microagent/ -run 'TestHostUnknownSubcommand|TestHostNoSubcommandStill' -v`
Expected: `TestHostUnknownSubcommandErrors` FAILs with `unexpected host argument: bogus-subcommand` (wrong message, but more importantly the dispatch doesn't exist). It currently errors but not via subcommand routing — we want explicit routing.

- [ ] **Step 3: Add subcommand dispatch at the top of `runHost`**

At the very start of `runHost(ctx, args, stdout)`, before building `doctorOptions`/flag parsing, insert:

```go
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "setup-networking":
			return runHostSetupNetworking(args[1:], stdout)
		default:
			return fmt.Errorf("unknown host command: %s", args[0])
		}
	}
```

The rest of `runHost` (flag parsing + diagnostics report) stays as the no-subcommand default. `runHostSetupNetworking` is implemented in Task 7.

- [ ] **Step 4: Stub `runHostSetupNetworking` so it compiles**

Add (temporary, replaced in Task 7):

```go
func runHostSetupNetworking(args []string, stdout *os.File) error {
	return fmt.Errorf("not implemented")
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/microagent/ -run 'TestHostUnknownSubcommand|TestHostNoSubcommandStill|TestHostCommand' -v`
Expected: PASS (unknown subcommand routed; existing report path preserved).

- [ ] **Step 6: Commit**

```bash
git add cmd/microagent/main.go cmd/microagent/main_test.go
git commit -m "host: dispatch subcommands (mirror kernel), keep report as default"
```

---

### Task 7: Implement `host setup-networking` (apply/--check/--revert)

Privileged logic is Linux-only; flag parsing + the not-root guard + the report shape are shared. Apply: persist `ip_forward` to `/etc/sysctl.d/99-microagent.conf` + live apply; `setcap cap_net_admin+eip` on the resolved supervisor. `--check` mutates nothing. `--revert` removes the drop-in and drops the cap.

**Files:**
- Create: `cmd/microagent/host_setup_networking.go` (cross-platform: flag parsing, `--check` via diagnostics, dispatch to `applyHostNetworking`/`revertHostNetworking`)
- Create: `cmd/microagent/host_setup_networking_linux.go` (`applyHostNetworking`, `revertHostNetworking`)
- Create: `cmd/microagent/host_setup_networking_other.go` (non-linux stubs returning a clear error)
- Test: `cmd/microagent/host_setup_networking_test.go`

- [ ] **Step 1: Write the failing test (parsing + not-root guard; no host mutation)**

`cmd/microagent/host_setup_networking_test.go`:

```go
package main

import (
	"os"
	"strings"
	"testing"
)

func TestSetupNetworkingCheckReportsWithoutMutating(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	// --check must never require root and never mutate the host.
	err := runHostSetupNetworking([]string{"--check"}, f)
	// On a host that isn't set up this returns a non-nil "not ready" error;
	// either way it must not panic and must print a readiness line.
	data, _ := os.ReadFile(f.Name())
	if !strings.Contains(string(data), "networking") && err == nil {
		t.Fatalf("expected a readiness report, got empty output")
	}
}

func TestSetupNetworkingApplyRequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test asserts the non-root guard")
	}
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := runHostSetupNetworking(nil, f) // apply
	if err == nil || !strings.Contains(err.Error(), "sudo microagent host setup-networking") {
		t.Fatalf("apply as non-root must instruct sudo, got %v", err)
	}
}

func TestSetupNetworkingRejectsUnknownFlag(t *testing.T) {
	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	if err := runHostSetupNetworking([]string{"--bogus"}, f); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/microagent/ -run TestSetupNetworking -v`
Expected: FAIL — current stub returns `not implemented` (guard/parse messages absent).

- [ ] **Step 3: Implement the cross-platform command body (replaces the Task 6 stub)**

Replace the stub `runHostSetupNetworking` with this (in `cmd/microagent/host_setup_networking.go`; delete the stub from `main.go`):

```go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
)

func runHostSetupNetworking(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("host setup-networking", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	check := fs.Bool("check", false, "report readiness without changing the host")
	revert := fs.Bool("revert", false, "undo a previous setup-networking")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *check && *revert {
		return fmt.Errorf("--check and --revert are mutually exclusive")
	}

	opts := doctorOptions{Backend: hostBackend(), Arch: defaultGuestArch()}
	opts.SupervisorPath = defaultSupervisorPath(opts.Backend)

	if *check {
		resp, _ := doctorResponse(context.Background(), opts)
		printNetworkingSection(stdout, resp.Host)
		if resp.Host == nil || !resp.Host.PrivilegedNetworkReady {
			return fmt.Errorf("privileged networking is not ready")
		}
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("setup requires root: run `sudo microagent host setup-networking`")
	}

	if *revert {
		if err := revertHostNetworking(opts.SupervisorPath); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "reverted microagent host networking setup")
		return nil
	}

	if err := applyHostNetworking(opts.SupervisorPath); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "host networking enabled: ip_forward persisted and CAP_NET_ADMIN granted to the supervisor")
	fmt.Fprintln(stdout, "run `microagent doctor` to confirm")
	return nil
}
```

Notes for the implementer:
- This file imports `context`, `flag`, `fmt`, `os`, and the `diagnostics` package.
- Reuse the existing helpers the explorer identified: `hostBackend()`, `defaultGuestArch()`, `defaultSupervisorPath(backend)`, `doctorResponse(ctx, opts)`, and `printNetworkingSection(stdout, host)` from Task 5.

- [ ] **Step 4: Implement the linux apply/revert**

`cmd/microagent/host_setup_networking_linux.go`:

```go
//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
)

const sysctlDropIn = "/etc/sysctl.d/99-microagent.conf"

func applyHostNetworking(supervisorPath string) error {
	if err := os.WriteFile(sysctlDropIn, []byte("net.ipv4.ip_forward=1\n"), 0o644); err != nil {
		return fmt.Errorf("persist ip_forward: %w", err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("enable ip_forward: %w", err)
	}
	if _, err := exec.LookPath("setcap"); err != nil {
		return fmt.Errorf("setcap not found (install libcap2-bin / libcap): %w", err)
	}
	if out, err := exec.Command("setcap", "cap_net_admin+eip", supervisorPath).CombinedOutput(); err != nil {
		return fmt.Errorf("grant CAP_NET_ADMIN to %s: %w: %s", supervisorPath, err, out)
	}
	return nil
}

func revertHostNetworking(supervisorPath string) error {
	if err := os.Remove(sysctlDropIn); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", sysctlDropIn, err)
	}
	if _, err := exec.LookPath("setcap"); err == nil {
		_ = exec.Command("setcap", "-r", supervisorPath).Run() // best-effort
	}
	return nil
}
```

- [ ] **Step 5: Implement the non-linux stub**

`cmd/microagent/host_setup_networking_other.go`:

```go
//go:build !linux

package main

import "fmt"

func applyHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}

func revertHostNetworking(supervisorPath string) error {
	return fmt.Errorf("host setup-networking is only supported on Linux")
}
```

- [ ] **Step 6: Run tests + cross-compile check**

Run: `go test ./cmd/microagent/ -run 'TestSetupNetworking|TestHost' -v`
Expected: PASS (apply guard fires as non-root; `--check` reports; unknown flag rejected).

Run: `GOOS=darwin go build ./cmd/microagent/`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add cmd/microagent/host_setup_networking*.go cmd/microagent/main.go cmd/microagent/host_setup_networking_test.go
git commit -m "host setup-networking: persist ip_forward + setcap supervisor (linux), stub elsewhere"
```

---

### Task 8: Add `setup-networking` to host help text

**Files:**
- Modify: `cmd/microagent/main.go` host/help text (the explorer noted help printers near lines 6739-6798; find where `host` usage is described).

- [ ] **Step 1: Update help**

Add a line documenting `microagent host setup-networking [--check|--revert]` and a one-sentence description (enables nat/bridged/named via ip_forward + CAP_NET_ADMIN; requires root) wherever `host` is described in the help output.

- [ ] **Step 2: Build**

Run: `go build ./cmd/microagent/`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add cmd/microagent/main.go
git commit -m "host: document setup-networking in help"
```

---

### Task 9: e2e runner remediation messaging

**Files:**
- Modify: `scripts/dev/microagent-e2e.sh` (final summary ~lines 377-385; named-network skip line ~lines 344-345)

- [ ] **Step 1: Improve the named-network skip reason**

Change the netpriv skip line so it points at remediation. Replace:

```bash
    printf '\n==> %s\n.. SKIP (no privileged networking: need root/CAP_NET_ADMIN + ip_forward)\n' "$name"
```

with:

```bash
    printf '\n==> %s\n.. SKIP (privileged networking not enabled)\n   To enable: sudo microagent host setup-networking  (then re-run as root or with MICROAGENT_E2E_ALLOW_NETPRIV=1)\n' "$name"
```

- [ ] **Step 2: Add an actionable footer to the summary**

After the `FAILED:` line in the summary block, before the exit logic, add:

```bash
if [ "${#skipped[@]}" -gt 0 ]; then
  printf '\nTo unlock skipped scenarios:\n'
  printf '  networking (nat/bridged): scripts/dev/microagent-e2e-linux-network-setup.sh, then re-run\n'
  printf '  named-network (netpriv):  sudo microagent host setup-networking, then run as root\n'
fi
```

- [ ] **Step 3: Verify the runner still parses and lists**

Run: `bash -n scripts/dev/microagent-e2e.sh && scripts/dev/microagent-e2e.sh --list`
Expected: no syntax error; scenario list prints.

- [ ] **Step 4: Commit**

```bash
git add scripts/dev/microagent-e2e.sh
git commit -m "e2e: surface networking setup remediation in skip messages and summary"
```

---

### Task 10: Full verification

- [ ] **Step 1: Whole-suite unit tests + vet + fmt**

Run:
```bash
gofmt -l cmd/microagent pkg/diagnostics pkg/vmkit
go vet ./cmd/microagent/ ./pkg/diagnostics/ ./pkg/vmkit/
go test ./cmd/microagent/ ./pkg/diagnostics/ ./pkg/vmkit/ -count=1
```
Expected: gofmt prints nothing; vet clean; tests PASS.

- [ ] **Step 2: Cross-compile guard**

Run: `GOOS=darwin go build ./... && GOOS=linux go build ./...`
Expected: both succeed.

- [ ] **Step 3: Manual smoke (Linux host)**

Run: `go run ./cmd/microagent doctor`
Expected: output includes a `Networking:` line; if privileged networking isn't set up, a `setup-networking` remediation hint appears.

Run (as root, on a disposable host or accepting the host mutation): `sudo go run ./cmd/microagent host setup-networking` then `go run ./cmd/microagent doctor` and confirm `nat/bridged/named ready`. Optionally run `scripts/dev/microagent-e2e.sh networking` to verify the privileged lane now passes. Then `sudo go run ./cmd/microagent host setup-networking --revert` to clean up.

- [ ] **Step 4: Open the PR** (auto-merge enabled per repo preference)

```bash
git push -u origin host-networking-setup
gh pr create --title "host setup-networking + doctor networking readiness" --body "<summary + link to docs/specs/2026-06-02-host-networking-setup-design.md>"
gh pr merge --auto --merge
```

---

### Task 11: Homebrew caveats (separate repo / separate PR)

**Files:**
- Modify: `homebrew-tap/microagent.rb` (`caveats` method)

- [ ] **Step 1: Append to the caveats string**

In the existing `def caveats` block, append:

```
On Linux, `isolated` and `user` (passt) networking work out of the box.
`nat`, `bridged`, and `named` network modes need a one-time privileged step:

  sudo microagent host setup-networking

This is reset by `brew upgrade` (file capabilities don't survive a reinstall),
so re-run it after upgrading. Check readiness any time with:

  microagent doctor
```

- [ ] **Step 2: Validate the formula**

Run: `cd homebrew-tap && brew style microagent.rb` (or `ruby -c microagent.rb` if brew unavailable)
Expected: no style/syntax errors.

- [ ] **Step 3: Commit + PR (in homebrew-tap)**

```bash
cd homebrew-tap
git checkout -b microagent-networking-caveats
git add microagent.rb
git commit -m "microagent: caveats for one-time host networking setup"
git push -u origin microagent-networking-caveats
gh pr create --title "microagent: networking setup caveats" --body "Tell users nat/bridged/named need 'sudo microagent host setup-networking' (and re-run after upgrade)."
gh pr merge --auto --merge
```

---

## Self-review notes

- **Spec coverage:** setup command (Tasks 6-8) · doctor section (Tasks 1-5) · Homebrew caveats (Task 11) · e2e messaging (Task 9) · explicit non-persistence handling (Task 3 remediation phrasing + Task 11 "re-run after upgrade") — all covered.
- **Type consistency:** `BinaryHasNetAdmin`, `DeriveNetworkReadiness`, `NetworkRemediation`, `applyHostNetworking`/`revertHostNetworking`, `runHostSetupNetworking` are used with identical signatures across the tasks that define and call them. `HostSupport` field names (`IPForwardEnabled`, `SupervisorNetAdminCapable`, `IsolatedNetworkReady`, `UserNetworkReady`, `PrivilegedNetworkReady`) are consistent from Task 1 onward.
- **Shared helper:** Task 5 introduces `printNetworkingSection(stdout, host)` and Task 7's `--check` reuses it (DRY, no placeholders).
- **Import checks flagged inline:** confirm `strings` in `diagnostics.go`, and `diagnostics`/`context` in the new command file.
