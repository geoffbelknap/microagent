package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestRequestForCommandMapsHumanCommands(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCommand string
	}{
		{
			name:        "doctor",
			args:        []string{"doctor"},
			wantCommand: "host",
		},
		{
			name: "create",
			args: []string{
				"create",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "prepare",
		},
		{
			name: "create dry run",
			args: []string{
				"create",
				"--dry-run",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "check",
		},
		{
			name: "start",
			args: []string{
				"start",
				"--id", "agent-1",
				"--kernel", "/tmp/kernel",
				"--rootfs", "/tmp/rootfs.ext4",
				"--state-dir", "/tmp/state",
			},
			wantCommand: "start",
		},
		{
			name:        "status",
			args:        []string{"status", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "inspect",
		},
		{
			name:        "quarantine",
			args:        []string{"quarantine", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "quarantine",
		},
		{
			name:        "pause",
			args:        []string{"pause", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "pause",
		},
		{
			name:        "resume",
			args:        []string{"resume", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "resume",
		},
		{
			name:        "kill",
			args:        []string{"kill", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "kill",
		},
		{
			name:        "delete",
			args:        []string{"delete", "agent-1", "--state-dir", "/tmp/state"},
			wantCommand: "delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := requestForCommand(tt.args[0], newFlagSet(tt.args[0]), os.Stdout, reorderFlagArgs(tt.args[1:]))
			if err != nil {
				t.Fatalf("requestForCommand: %v", err)
			}
			if req.Command != tt.wantCommand {
				t.Fatalf("Command = %q, want %q", req.Command, tt.wantCommand)
			}
			if tt.args[0] != "doctor" && req.Identity.RuntimeID != "agent-1" {
				t.Fatalf("RuntimeID = %q, want agent-1", req.Identity.RuntimeID)
			}
		})
	}
}

// TestRequestForCommandRejectsStopVerb pins that "stop" is no longer a distinct
// request command word: it is resolved to "halt" at the registry (lookupCommand),
// so the low-level request builder must never see a raw "stop" and never emit a
// Control("stop") that would record the stopped state instead of halted.
func TestRequestForCommandRejectsStopVerb(t *testing.T) {
	_, err := requestForCommand("stop", newFlagSet("stop"), os.Stdout, reorderFlagArgs([]string{"agent-1", "--state-dir", "/tmp/state"}))
	if err == nil {
		t.Fatal("requestForCommand(stop): want unknown-command error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("requestForCommand(stop): err = %v, want unknown-command error", err)
	}
}

// TestRequestForCommandRejectsRemovedJSONAlias pins the removal of the
// -json/--json compat alias for --request-json (see MIGRATION.md): the
// low-level request flagset no longer registers "-json" at all, so it must
// surface as an ordinary unknown-flag error, never a silent parse of the
// request file.
func TestRequestForCommandRejectsRemovedJSONAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{KernelPath: "/tmp/kernel", RootfsPath: "/tmp/rootfs.ext4", StateDir: "/tmp/state"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = requestForCommand("create", newFlagSet("create"), os.Stdout, []string{"-json", path})
	if err == nil {
		t.Fatal("requestForCommand(-json): want unknown-flag error, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("requestForCommand(-json): err = %v, want unknown-flag error", err)
	}
}

func TestRequestForCommandReadsRequestJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	req := vmkit.Request{
		Identity: &vmkit.Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: vmkit.RoleWorkload, Backend: vmkit.BackendAppleVF},
		Config:   &vmkit.Config{KernelPath: "/tmp/kernel", RootfsPath: "/tmp/rootfs.ext4", StateDir: "/tmp/state"},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := requestForCommand("create", newFlagSet("create"), os.Stdout, []string{"--request-json", path})
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if got.Identity.RuntimeID != "agent-1" {
		t.Fatalf("RuntimeID = %q, want agent-1", got.Identity.RuntimeID)
	}

	// A stray -json alongside --request-json is no longer a recognized
	// flag at all (the compat alias is removed); it errors as unknown,
	// not as a "use one or the other" conflict.
	_, err = requestForCommand("create", newFlagSet("create"), os.Stdout, []string{"--request-json", path, "-json", "/tmp/other.json"})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("--request-json with stray -json: err = %v, want unknown-flag error", err)
	}
}

// TestCreateRequestJSONAliasRemovedEndToEnd exercises the full CLI path (run,
// not just requestForCommand) for the request-JSON alias removal: --request-json
// still reaches request decode on the low-level create path, while the removed
// -json alias surfaces as an ordinary unknown-flag error rather than silently
// succeeding or being swallowed by routing to the high-level create path.
func TestCreateRequestJSONAliasRemovedEndToEnd(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })

	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, []byte("not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runMainForTest(t, "create", "--request-json", path)
	if err == nil {
		t.Fatal("create --request-json <invalid>: want a decode error, got nil")
	}
	if strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("create --request-json <invalid>: got unknown-flag error, want a JSON decode error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("create --request-json <invalid>: err = %v, want a decode error naming the file content problem", err)
	}

	_, err = runMainForTest(t, "create", "-json", path)
	if err == nil {
		t.Fatal("create -json <path>: want an unknown-flag error, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("create -json <path>: err = %v, want unknown-flag error (compat alias removed)", err)
	}
}

// TestCreateJSONRequestAliasShapeFailsLoudlyEndToEnd pins the review-flagged
// hazard fix: the removed request-alias shape "create --json request.json"
// must not be silently reinterpreted as "create request.json" (which would
// create/act on a workspace literally named "request.json"). parseGlobalFlags
// must leave "--json" and "request.json" both untouched so the create
// flagset rejects "-json" as unknown, and no workspace state must appear
// under the state dir as a side effect of the failed call.
func TestCreateJSONRequestAliasShapeFailsLoudlyEndToEnd(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })

	dir := t.TempDir()
	_, err := runMainForTest(t, "create", "--json", "request.json", "--state-dir", dir)
	if err == nil {
		t.Fatal("create --json request.json: want an unknown-flag error, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), "-json") {
		t.Fatalf("create --json request.json: err = %v, want it to mention -json", err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "request.json")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace state dir %q created for %q: stat err = %v, want IsNotExist", dir, "request.json", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "request.json")); !os.IsNotExist(statErr) {
		t.Fatalf("high-level workspace dir created for %q: stat err = %v, want IsNotExist", "request.json", statErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir %q not empty after failed call: %v", dir, entries)
	}
}

// TestRequestForCommandJSONAliasHintEndToEnd pins the round-2 fix: when the
// removed request-alias shape ("create --json request.json") reaches
// requestForCommand's flag.FlagSet.Parse as an unknown "-json" flag, the
// wrapped error must point the caller at the real replacement
// (--request-json), not just report the bare "flag provided but not
// defined" message.
func TestRequestForCommandJSONAliasHintEndToEnd(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })

	dir := t.TempDir()
	_, err := runMainForTest(t, "create", "--json", "request.json", "--state-dir", dir)
	if err == nil {
		t.Fatal("create --json request.json: want an unknown-flag error, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), "use --request-json") {
		t.Fatalf("create --json request.json: err = %v, want it to hint --request-json", err)
	}
	// The hint is now owned solely by parseCommandFlags (requestForCommand's
	// own near-duplicate was removed once it threaded stdout through
	// parseCommandFlags); pin that there is exactly one occurrence, not two.
	if count := strings.Count(err.Error(), "use --request-json"); count != 1 {
		t.Fatalf("create --json request.json: hint appeared %d times, want exactly 1: %v", count, err)
	}
}

// TestLowLevelFlagParseErrorsPointAtHelp pins Plan 2 Task 3: the two
// remaining bare fs.Parse call sites (parseWorkspaceOptions, which backs
// run/dispatch and the create/start high-level paths, and requestForCommand,
// which backs create/start's low-level forms) now go through
// parseCommandFlags like every other command's flagset. An unknown flag must
// produce one error line plus a "Run 'microagent <cmd> --help' for usage"
// pointer, not a bare flag-package error or a raw usage dump.
func TestLowLevelFlagParseErrorsPointAtHelp(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })

	cases := []struct {
		name string // the command word whose --help the error must point at
		args []string
	}{
		// run and dispatch always go through parseWorkspaceOptions directly.
		{"run", []string{"run", "--nope"}},
		{"dispatch", []string{"dispatch", "--nope"}},
		// create --nope has no positional name, --file, --image, etc., so
		// shouldUseHighLevelCreate routes it to the low-level requestForCommand
		// path instead of parseWorkspaceOptions.
		{"create", []string{"create", "--nope"}},
		// start --nope has no positional workspace name, so it routes to the
		// low-level requestForCommand path (runStartWorkspace is skipped).
		{"start", []string{"start", "--nope"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runMainForTest(t, tc.args...)
			if err == nil {
				t.Fatalf("%v: want error, got nil", tc.args)
			}
			want := fmt.Sprintf("Run 'microagent %s --help' for usage", tc.name)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%v: err = %q, want it to contain %q", tc.args, err.Error(), want)
			}
			if strings.Count(err.Error(), "\n") != 1 {
				t.Fatalf("%v: err = %q, want exactly one error line plus the --help pointer line", tc.args, err.Error())
			}
		})
	}
}

// TestRunHelpStillUsesHandWrittenHelp pins that `run --help` (args[0] ==
// "--help", intercepted by runWorkspace before parseWorkspaceOptions is ever
// called) keeps printing the rich hand-written help added by plan 1, not the
// generic flag-listing generated by parseCommandFlags/printGeneratedCommandHelp.
func TestRunHelpStillUsesHandWrittenHelp(t *testing.T) {
	out, err := runMainForTest(t, "run", "--help")
	if err != nil {
		t.Fatalf("run --help: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "Run a command from an image") ||
		!strings.Contains(text, "Container-style examples") {
		t.Fatalf("run --help = %s, want the hand-written run help", text)
	}
}

// TestCreateJSONStdinAliasShapeFailsLoudlyEndToEnd pins the round-2 critical
// fix: the removed request-alias shape reached via the bare stdin marker
// ("create --json - --state-dir <dir>") must not be silently reinterpreted
// as "create - --state-dir <dir>" (which would create a workspace literally
// named "-"). parseGlobalFlags must leave "--json" and "-" both untouched so
// the create flagset rejects "-json" as unknown before ever reading stdin,
// and no workspace state must appear under the state dir as a side effect of
// the failed call.
func TestCreateJSONStdinAliasShapeFailsLoudlyEndToEnd(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })

	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("{}"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = origStdin
		r.Close()
	})

	dir := t.TempDir()
	_, err = runMainForTest(t, "create", "--json", "-", "--state-dir", dir)
	if err == nil {
		t.Fatal("create --json -: want an unknown-flag error, got nil (silent success)")
	}
	if !strings.Contains(err.Error(), "-json") {
		t.Fatalf("create --json -: err = %v, want it to mention -json", err)
	}

	if _, statErr := os.Stat(filepath.Join(dir, "-")); !os.IsNotExist(statErr) {
		t.Fatalf("workspace state dir %q created for %q: stat err = %v, want IsNotExist", dir, "-", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "workspaces", "-")); !os.IsNotExist(statErr) {
		t.Fatalf("high-level workspace dir created for %q: stat err = %v, want IsNotExist", "-", statErr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("state dir %q not empty after failed call: %v", dir, entries)
	}
}

func TestRequestForCommandParsesVsock(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--vsock", "1024=127.0.0.1:8200",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.VsockListeners) != 1 {
		t.Fatalf("VsockListeners len = %d, want 1", len(req.Config.VsockListeners))
	}
	listener := req.Config.VsockListeners[0]
	if listener.Port != 1024 || listener.Target != "127.0.0.1:8200" {
		t.Fatalf("listener = %#v", listener)
	}
}

// TestRequestForCommandLowLevelUnmediated asserts the raw low-level create/start
// primitive is NOT force-mediated. It has no --egress flag and does not build the
// request through workspace.Request(), so EgressMode stays empty and no CA-cert
// listener is allocated. Mediating it would MITM the guest's TLS with a CA the
// guest never receives (CACertPort=0 => no boot arg => guestinit installs nothing).
func TestRequestForCommandLowLevelUnmediated(t *testing.T) {
	for _, command := range []string{"create", "start"} {
		req, err := requestForCommand(command, newFlagSet(command), os.Stdout, reorderFlagArgs([]string{
			"--id", "agent-1",
			"--kernel", "/tmp/kernel",
			"--rootfs", "/tmp/rootfs.ext4",
			"--state-dir", "/tmp/state",
			"--backend", hostBackend(),
		}))
		if err != nil {
			t.Fatalf("%s: requestForCommand: %v", command, err)
		}
		if req.Config.EgressMode != "" {
			t.Errorf("%s: EgressMode = %q, want empty (raw primitive must not set a default)", command, req.Config.EgressMode)
		}
		if vmkit.EgressMediationOn(req.Config.EgressMode) {
			t.Errorf("%s: low-level request must not be mediated", command)
		}
		if req.Config.CACertPort != 0 {
			t.Errorf("%s: CACertPort = %d, want 0", command, req.Config.CACertPort)
		}
		for _, l := range req.Config.VsockListeners {
			if l.Target == "cacert://serve" {
				t.Errorf("%s: low-level request must not allocate a cacert://serve listener: %#v", command, req.Config.VsockListeners)
			}
		}
	}
}

func TestRequestForCommandParsesNetwork(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--backend", hostBackend(),
		"--network", "user",
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if req.Config.Network == nil {
		t.Fatal("network config is nil")
	}
	if req.Config.Network.Mode != "user" {
		t.Fatalf("network mode = %q, want user", req.Config.Network.Mode)
	}
	if len(req.Config.Network.PortForwards) != 1 {
		t.Fatalf("port forwards len = %d, want 1", len(req.Config.Network.PortForwards))
	}
	forward := req.Config.Network.PortForwards[0]
	if forward.Host != "127.0.0.1" || forward.HostPort != 8080 || forward.GuestPort != 80 || forward.Protocol != "tcp" {
		t.Fatalf("forward = %#v", forward)
	}
}

func TestRequestForCommandRejectsRemovedNetworkModes(t *testing.T) {
	for _, mode := range []string{"bridged", "nat", "named"} {
		_, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
			"--id", "agent-1",
			"--kernel", "/tmp/kernel",
			"--rootfs", "/tmp/rootfs.ext4",
			"--state-dir", "/tmp/state",
			"--backend", hostBackend(),
			"--network", mode,
		}))
		if err == nil {
			t.Fatalf("requestForCommand accepted removed network mode %q", mode)
		}
	}
}

// captureHelp runs a help printer that writes to an *os.File and returns what
// it printed, so the heredoc help text can be asserted against the --network
// flag help constants.
func captureHelp(t *testing.T, print func(*os.File)) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	print(w)
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestNetworkFlagHelpAgreesOnModeSet guards against drift across the several
// --network help strings: every workspace-facing --network help must advertise
// exactly {user, isolated} and never re-advertise a removed mode.
func TestNetworkFlagHelpAgreesOnModeSet(t *testing.T) {
	workspaceModes := []string{"user", "isolated"}

	// Flag help constants used by requestForCommand and parseWorkspaceOptions.
	for _, c := range []struct {
		name string
		help string
	}{
		{"networkModeFlagHelp", networkModeFlagHelp},
	} {
		assertAdvertisesModes(t, c.name, c.help, workspaceModes)
		assertExcludesRemovedModes(t, c.name, c.help)
	}

	// Heredoc help text printed by the workspace-facing help commands.
	for _, c := range []struct {
		name  string
		print func(*os.File)
	}{
		{"printFullHelp", printFullHelp},
		{"printRunHelp", printRunHelp},
		{"printCreateHelp", printCreateHelp},
	} {
		help := networkHelpBlock(captureHelp(t, c.print))
		if help == "" {
			t.Fatalf("%s: no -network help block found", c.name)
		}
		assertAdvertisesModes(t, c.name, help, workspaceModes)
		assertExcludesRemovedModes(t, c.name, help)
	}

	assertAdvertisesModes(t, "networkModePerfFlagHelp", networkModePerfFlagHelp, workspaceModes)
	assertExcludesRemovedModes(t, "networkModePerfFlagHelp", networkModePerfFlagHelp)
	perfHelp := networkHelpBlock(captureHelp(t, printPerfHelp))
	if perfHelp == "" {
		t.Fatal("printPerfHelp: no -network help block found")
	}
	assertAdvertisesModes(t, "printPerfHelp", perfHelp, workspaceModes)
	assertExcludesRemovedModes(t, "printPerfHelp", perfHelp)
}

// networkHelpBlock extracts the -network option help (its line plus any
// indented continuation lines) from a heredoc help body.
func networkHelpBlock(help string) string {
	lines := strings.Split(help, "\n")
	var block []string
	collecting := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-network ") {
			collecting = true
			block = append(block, trimmed)
			continue
		}
		if collecting {
			// Continuation lines are indented and do not start a new -flag.
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "-") || trimmed == "" {
				break
			}
			block = append(block, trimmed)
		}
	}
	return strings.Join(block, " ")
}

func assertAdvertisesModes(t *testing.T, name, help string, modes []string) {
	t.Helper()
	for _, mode := range modes {
		if !strings.Contains(help, mode) {
			t.Fatalf("%s does not advertise %q mode: %q", name, mode, help)
		}
	}
}

func assertExcludesRemovedModes(t *testing.T, name, help string) {
	t.Helper()
	for _, mode := range []string{"bridged", "nat", "named"} {
		if strings.Contains(help, mode) {
			t.Fatalf("%s re-advertises the removed %q mode: %q", name, mode, help)
		}
	}
}

func TestRequestForCommandRejectsIsolatedPublish(t *testing.T) {
	_, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--network", "isolated",
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err == nil {
		t.Fatal("requestForCommand accepted isolated publish")
	}
}

func TestRequestForCommandAcceptsAppleVFPublish(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--backend", vmkit.BackendAppleVF,
		"--publish", "127.0.0.1:8080:80/tcp",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if req.Config == nil || req.Config.Network == nil || len(req.Config.Network.PortForwards) != 1 {
		t.Fatalf("network = %#v", req.Config.Network)
	}
}

func TestRequestForCommandRejectsUnsupportedPortForwardProtocol(t *testing.T) {
	_, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--publish", "127.0.0.1:8080:80/udp",
	}))
	if err == nil {
		t.Fatal("requestForCommand accepted udp port forward")
	}
}

func TestRequestForCommandParsesDisk(t *testing.T) {
	req, err := requestForCommand("create", newFlagSet("create"), os.Stdout, reorderFlagArgs([]string{
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
		"--disk", "constraints=/tmp/constraints.ext4:/config:ro",
	}))
	if err != nil {
		t.Fatalf("requestForCommand: %v", err)
	}
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	disk := req.Config.Disks[0]
	if disk.Name != "constraints" || disk.Path != "/tmp/constraints.ext4" || disk.Mountpoint != "/config" || disk.Mode != "ro" {
		t.Fatalf("disk = %#v", disk)
	}
}

func TestWorkspaceRequestIncludesVsockMappings(t *testing.T) {
	mediation := vmkit.MediationConfig{
		Enabled:    true,
		Required:   true,
		Port:       2048,
		Target:     "127.0.0.1:9900",
		FailClosed: true,
	}
	req, err := workspace.Request(workspaceOptions{
		Name:    "agent-1",
		Backend: vmkit.BackendLinuxKVM,
		// linux-kvm has a host-datapath capture provider, so the secure-default
		// (unspecified -> broker) egress mode; broker forges no certs, so no CA-cert listener.
		KernelPath:     "/tmp/kernel",
		MemoryMiB:      512,
		CPUCount:       2,
		ResultPort:     1024,
		VsockListeners: []vmkit.VsockListener{{Port: 3128, Target: "127.0.0.1:19000"}},
		Mediation:      &mediation,
	}, "run", "/tmp/rootfs.ext4", workspace.NewRequestID())
	if err != nil {
		t.Fatalf("workspaceRequest: %v", err)
	}
	// result + enforcer + mediation listeners. The default mode is broker,
	// which mediates but forges no certificates, so NO CA-cert listener is
	// allocated (unlike the retired guarded default).
	if len(req.Config.VsockListeners) != 3 {
		t.Fatalf("VsockListeners len = %d, want 3: %#v", len(req.Config.VsockListeners), req.Config.VsockListeners)
	}
	if req.Config.VsockListeners[1].Port != 3128 || req.Config.VsockListeners[1].Target != "127.0.0.1:19000" {
		t.Fatalf("enforcer listener = %#v", req.Config.VsockListeners[1])
	}
	if req.Config.VsockListeners[2].Port != 2048 || req.Config.VsockListeners[2].Target != "127.0.0.1:9900" {
		t.Fatalf("mediation listener = %#v", req.Config.VsockListeners[2])
	}
	for _, l := range req.Config.VsockListeners {
		if l.Target == "cacert://serve" {
			t.Fatalf("broker default must not allocate a cacert://serve listener: %#v", req.Config.VsockListeners)
		}
	}
	if req.Config.Mediation == nil || !req.Config.Mediation.Required || !req.Config.Mediation.FailClosed {
		t.Fatalf("mediation = %#v", req.Config.Mediation)
	}
}

func TestWorkspaceRequestIncludesDisks(t *testing.T) {
	req, err := workspace.Request(workspaceOptions{
		Name:       "agent-1",
		Backend:    "apple-vf",
		KernelPath: "/tmp/kernel",
		MemoryMiB:  512,
		CPUCount:   2,
		Disks: []workspaceDisk{{
			Name:       "workspace",
			Path:       "/tmp/workspace.ext4",
			Mountpoint: "/workspace",
			Mode:       "rw",
		}},
	}, "run", "/tmp/rootfs.ext4", workspace.NewRequestID())
	if err != nil {
		t.Fatalf("workspaceRequest: %v", err)
	}
	if len(req.Config.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(req.Config.Disks))
	}
	if req.Config.Disks[0].Mountpoint != "/workspace" || req.Config.Disks[0].Mode != "rw" {
		t.Fatalf("disk = %#v", req.Config.Disks[0])
	}
}

func TestRunUsesSupervisorOverride(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	supervisor := filepath.Join(dir, "supervisor")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
python3 -c 'import json,sys; req=json.load(sys.stdin); open(%q, "w").write(json.dumps(req)); print(json.dumps({"ok": True, "backend": %q, "event": {"identity": req["identity"], "state": "prepared", "observedAt": "2026-05-02T00:00:00Z"}}))'
`, requestPath, hostBackend())
	if err := os.WriteFile(supervisor, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(dir, "stdout.json")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{
		"create",
		"--supervisor", supervisor,
		"--backend", hostBackend(),
		"--id", "agent-1",
		"--kernel", "/tmp/kernel",
		"--rootfs", "/tmp/rootfs.ext4",
		"--state-dir", "/tmp/state",
	}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"state": "prepared"`) {
		t.Fatalf("stdout missing prepared state: %s", data)
	}
	var req vmkit.Request
	requestData, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(requestData, &req); err != nil {
		t.Fatalf("decode supervisor request %q: %v", requestData, err)
	}
	if req.Command != "prepare" {
		t.Fatalf("supervisor request command = %q, want prepare", req.Command)
	}
	if req.Identity == nil || req.Identity.RuntimeID != "agent-1" || req.Identity.Backend != hostBackend() {
		t.Fatalf("supervisor request identity = %#v", req.Identity)
	}
	if req.Config == nil || req.Config.RootfsPath != "/tmp/rootfs.ext4" || req.Config.KernelPath != "/tmp/kernel" {
		t.Fatalf("supervisor request config = %#v", req.Config)
	}
}

// startWorkspaceReferencingProcess spawns a long-lived process whose argv carries
// the workspace's state path, so the status reconcile (which checks firecracker
// liveness) sees the VM as alive and reports it running instead of reaping it to a
// terminal state. Returns its pid.
func startWorkspaceReferencingProcess(t *testing.T, stateDir, name string) int {
	t.Helper()
	vm := exec.Command("sleep", "300")
	vm.Args = []string{filepath.Join(stateDir, name), "300"}
	if err := vm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vm.Process.Kill(); _, _ = vm.Process.Wait() })
	return vm.Process.Pid
}
