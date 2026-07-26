package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	firecrackersupervisor "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_FIRECRACKER_SUPERVISOR_HELPER") == "1" {
		var req vmkit.Request
		if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
			_ = json.NewEncoder(os.Stdout).Encode(vmkit.Response{OK: false, Backend: hostBackend(), Error: err.Error()})
			os.Exit(1)
		}
		resp, err := firecrackersupervisor.Supervisor{}.Do(context.Background(), req)
		_ = json.NewEncoder(os.Stdout).Encode(resp)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRunVersionAliases(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		dir := t.TempDir()
		stdoutPath := filepath.Join(dir, "stdout.txt")
		stdout, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		err = run(t.Context(), args, stdout)
		if closeErr := stdout.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "microagent ") {
			t.Fatalf("version output = %q", data)
		}
	}
}

func TestHelpIsCompactAndHelpAllListsAdvancedCommands(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"help"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run help: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	help := string(data)
	if !strings.Contains(help, "microagent help all") {
		t.Fatalf("compact help missing help all pointer:\n%s", help)
	}
	for _, command := range []string{"pause", "resume", "snapshot", "rootfs", "kernel"} {
		if strings.Contains(help, "\n  "+command+" ") {
			t.Fatalf("compact help includes secondary command %q:\n%s", command, help)
		}
	}

	allPath := filepath.Join(dir, "all.txt")
	allOut, err := os.Create(allPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"help", "all"}, allOut)
	if closeErr := allOut.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run help all: %v", err)
	}
	allData, err := os.ReadFile(allPath)
	if err != nil {
		t.Fatal(err)
	}
	allHelp := string(allData)
	for _, command := range []string{"pause", "resume", "snapshot", "rootfs", "kernel"} {
		if !strings.Contains(allHelp, "\n  "+command+" ") {
			t.Fatalf("full help missing %q command:\n%s", command, allHelp)
		}
	}
}

func TestRunTopLevelHelpFlagAliases(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		dir := t.TempDir()
		stdoutPath := filepath.Join(dir, "stdout.txt")
		stdout, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		err = run(t.Context(), args, stdout)
		if closeErr := stdout.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		data, err := os.ReadFile(stdoutPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "microagent help all") {
			t.Fatalf("help output for %v missing help all pointer:\n%s", args, data)
		}
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	err := run(context.Background(), []string{"frobnicate"}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "unknown command \"frobnicate\"") {
		t.Fatalf("want unknown-command error, got %v", err)
	}
}

func TestTopLevelAliases(t *testing.T) {
	// rm with no target behaves exactly like delete with no target
	errRM := run(context.Background(), []string{"rm"}, os.Stdout)
	errDelete := run(context.Background(), []string{"delete"}, os.Stdout)
	if (errRM == nil) != (errDelete == nil) {
		t.Fatalf("rm and delete diverge: %v vs %v", errRM, errDelete)
	}
}

func TestParseForkSnapshotRef(t *testing.T) {
	source, tag, err := parseForkSnapshotRef("base:pre-upgrade")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if source != "base" || tag != "pre-upgrade" {
		t.Fatalf("got source=%q tag=%q, want base/pre-upgrade", source, tag)
	}
	for _, bad := range []string{"", "base", "base:", ":tag", "   "} {
		if _, _, err := parseForkSnapshotRef(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestReorderFlagArgsKeepsTagAndFromSnapshotValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		flag string
		want string
	}{
		{"snapshot tag", []string{"agent-1", "--tag", "base", "--state-dir", "/s"}, "-tag", "base"},
		{"from-snapshot", []string{"agent-1", "--from-snapshot", "base", "--state-dir", "/s"}, "-from-snapshot", "base"},
		{"secret", []string{"app", "--secret", "API=env:TOKEN"}, "-secret", "API=env:TOKEN"},
		{"secrets-env-file", []string{"app", "--image", "img", "--secrets-env-file", "/tmp/app.env"}, "-secrets-env-file", "/tmp/app.env"},
		{"secret-on-demand", []string{"app", "--secret-on-demand", "DB=env:DB"}, "-secret-on-demand", "DB=env:DB"},
		{"runner-command", []string{"local/smoke/smoke.gguf", "--runner-command", "runner serve {model} --listen {addr}"}, "-runner-command", "runner serve {model} --listen {addr}"},
		{"runner-arg", []string{"local/smoke/smoke.gguf", "--runner-arg", "-ngl"}, "-runner-arg", "-ngl"},
		{"runner-env", []string{"local/smoke/smoke.gguf", "--runner-env", "CUDA_VISIBLE_DEVICES=0"}, "-runner-env", "CUDA_VISIBLE_DEVICES=0"},
		{"model-runner-command", []string{"demo", "--model", "org/repo/m.gguf", "--model-runner-command", "runner serve {model} --listen {addr}"}, "-model-runner-command", "runner serve {model} --listen {addr}"},
		{"model-runner-arg", []string{"demo", "--model", "org/repo/m.gguf", "--model-runner-arg", "-ngl"}, "-model-runner-arg", "-ngl"},
		{"model-policy-file", []string{"demo", "--model", "org/repo/m.gguf", "--model-policy-file", "/tmp/policy.json"}, "-model-policy-file", "/tmp/policy.json"},
		{"egress-policy", []string{"demo", "--egress", "mitm", "--egress-policy", "/tmp/egress.yaml"}, "-egress-policy", "/tmp/egress.yaml"},
		{"egress-swap-config", []string{"demo", "--egress", "mitm", "--egress-swap-config", "/tmp/swaps.yaml"}, "-egress-swap-config", "/tmp/swaps.yaml"},
		{"broker-upstream", []string{"demo", "--broker-upstream", "https://api.example.com"}, "-broker-upstream", "https://api.example.com"},
		{"broker-secret", []string{"demo", "--broker-secret", "api=env:MY_TOKEN"}, "-broker-secret", "api=env:MY_TOKEN"},
		{"broker-env", []string{"demo", "--broker-env", "EXAMPLE_BASE_URL"}, "-broker-env", "EXAMPLE_BASE_URL"},
		{"broker-ca", []string{"demo", "--broker-ca", "/etc/ssl/broker-ca.pem"}, "-broker-ca", "/etc/ssl/broker-ca.pem"},
		{"broker-endpoint", []string{"demo", "--broker-endpoint", "upstream=https://a.example.com;secret=a=env:A_TOKEN"}, "-broker-endpoint", "upstream=https://a.example.com;secret=a=env:A_TOKEN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reordered := reorderFlagArgs(tc.args)
			// The value flag and its argument must stay adjacent so flag parsing
			// does not mistake the value for a positional and the positional name
			// for an extra argument.
			found := false
			for i := 0; i+1 < len(reordered); i++ {
				if reordered[i] == tc.flag && reordered[i+1] == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("reorderFlagArgs(%v) = %v; %s value not kept adjacent", tc.args, reordered, tc.flag)
			}
		})
	}
}

func TestParseWorkspaceOptionsPositionalNameWithSwapConfig(t *testing.T) {
	// Regression: --egress-swap-config must be recognized as a value flag by
	// reorderFlagArgs, otherwise its argument is stranded as a positional after
	// the workspace name, making NArg() != 1 and rejecting the name.
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"victim",
		"--image", "docker.io/library/alpine:3.20",
		"--egress", "mitm",
		"--egress-allow", "registry.npmjs.org",
		"--egress-swap-config", "swaps.yaml",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "victim" {
		t.Fatalf("Name = %q, want victim", opts.Name)
	}
	if opts.EgressSwapConfigPath != "swaps.yaml" {
		t.Fatalf("EgressSwapConfigPath = %q, want swaps.yaml", opts.EgressSwapConfigPath)
	}
}

// TestParseWorkspaceOptionsPositionalNameWithBrokerEndpoint is a regression
// guard for the same class of bug TestParseWorkspaceOptionsPositionalNameWithSwapConfig
// covers: callers may place the workspace name first (positional), followed by
// flags, so every broker-* flag — including the new
// --broker-endpoint/--broker-ca and the bool --broker-proxy/--broker-capture —
// must be recognized by reorderFlagArgs or the name is rejected as an
// unexpected trailing argument.
func TestParseWorkspaceOptionsPositionalNameWithBrokerEndpoint(t *testing.T) {
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"victim",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-endpoint", "upstream=https://a.example.com;secret=a=env:A_TOKEN;proxy;capture",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "victim" {
		t.Fatalf("Name = %q, want victim", opts.Name)
	}
	if len(opts.Brokers) != 1 || !opts.Brokers[0].Proxy || !opts.Brokers[0].Capture {
		t.Fatalf("Brokers = %+v", opts.Brokers)
	}

	opts, err = parseWorkspaceOptions("create", os.Stdout, []string{
		"victim2",
		"--image", "docker.io/library/alpine:3.20",
		"--broker-upstream", "https://api.example.com",
		"--broker-secret", "api=env:MY_TOKEN",
		"--broker-ca", "/etc/ssl/broker-ca.pem",
		"--broker-proxy",
		"--broker-capture",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.Name != "victim2" {
		t.Fatalf("Name = %q, want victim2", opts.Name)
	}
	if opts.Broker == nil || !opts.Broker.Proxy || !opts.Broker.Capture || opts.Broker.UpstreamCAFile != "/etc/ssl/broker-ca.pem" {
		t.Fatalf("Broker = %+v", opts.Broker)
	}
}

func TestParseWorkspaceOptionsEgressAllowCommaSplits(t *testing.T) {
	// A comma-separated allowlist must split into distinct hosts, not be stored
	// as one literal host (which silently denies every real host).
	opts, err := parseWorkspaceOptions("create", os.Stdout, []string{
		"victim",
		"--image", "docker.io/library/alpine:3.20",
		"--egress", "mitm",
		"--egress-allow", "registry.npmjs.org,postman-echo.com",
	})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	got := map[string]bool{}
	for _, h := range opts.EgressAllow {
		got[h] = true
	}
	if !got["registry.npmjs.org"] || !got["postman-echo.com"] {
		t.Fatalf("EgressAllow = %v, want both registry.npmjs.org and postman-echo.com as separate hosts", opts.EgressAllow)
	}
	if got["registry.npmjs.org,postman-echo.com"] {
		t.Fatalf("EgressAllow kept the comma-joined value as one literal host: %v", opts.EgressAllow)
	}
}

func TestRunSnapshotCreateParsesTagAndName(t *testing.T) {
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	// With a missing supervisor the create fails at dispatch, but it must get
	// past argument parsing — proving --tag is recognized as a value flag and
	// the positional name is accepted rather than mistaken for extra arguments.
	rerr := run(t.Context(), []string{"snapshot", "create", "agent-1", "--tag", "base", "--state-dir", dir, "--supervisor", filepath.Join(dir, "no-supervisor")}, out)
	_ = out.Close()
	if rerr == nil {
		t.Fatal("expected dispatch error with a missing supervisor")
	}
	if strings.Contains(rerr.Error(), "usage:") {
		t.Fatalf("snapshot create mis-parsed --tag/name: %v", rerr)
	}
}

func TestRunSnapshotCreateAppleVFUsesBackendSnapshotPath(t *testing.T) {
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rerr := run(t.Context(), []string{"snapshot", "create", "agent-1", "--tag", "base", "--state-dir", dir, "--backend", "apple-vf"}, out)
	_ = out.Close()
	if rerr == nil {
		t.Fatal("expected Apple VF snapshot create to require runtime state")
	}
	if strings.Contains(rerr.Error(), "not supported on the apple-vf backend") {
		t.Fatalf("snapshot create error = %q, did not expect unsupported feature gap", rerr.Error())
	}
	if runtime.GOOS != "darwin" {
		if !strings.Contains(rerr.Error(), "is not available in this") {
			t.Fatalf("snapshot create error = %q, want host backend rejection on %s", rerr.Error(), runtime.GOOS)
		}
		return
	}
	if !strings.Contains(rerr.Error(), "runtime.json") {
		t.Fatalf("snapshot create error = %q, want missing runtime state", rerr.Error())
	}
}

func TestRunSnapshotListAndRemove(t *testing.T) {
	dir := t.TempDir()
	name := "agent-1"
	for _, tag := range []string{"snap-a", "snap-b"} {
		sdir := vmkit.SnapshotDir(dir, name, tag)
		if err := vmkit.WriteSnapshotManifest(sdir, vmkit.SnapshotManifest{Tag: tag, MemoryMiB: 512, CreatedAt: "2026-06-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}

	listPath := filepath.Join(dir, "list.json")
	stdout, err := os.Create(listPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"--json", "snapshot", "list", name, "--state-dir", dir}, stdout)
	_ = stdout.Close()
	if err != nil {
		t.Fatalf("snapshot list: %v", err)
	}
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "snap-a") || !strings.Contains(string(data), "snap-b") {
		t.Fatalf("snapshot list output missing tags:\n%s", data)
	}

	rmOut, err := os.Create(filepath.Join(dir, "rm.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"snapshot", "delete", name, "snap-a", "--state-dir", dir}, rmOut)
	_ = rmOut.Close()
	if err != nil {
		t.Fatalf("snapshot delete: %v", err)
	}
	if _, err := os.Stat(vmkit.SnapshotDir(dir, name, "snap-a")); !os.IsNotExist(err) {
		t.Fatalf("snap-a should be removed, stat err = %v", err)
	}
}

func TestCreateHelpUsesWorkspaceHelp(t *testing.T) {
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	err = run(t.Context(), []string{"create", "--help"}, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("run create --help: %v", err)
	}
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "microagent create") ||
		!strings.Contains(text, "-entrypoint <command>") ||
		!strings.Contains(text, "-v SRC:DST[:ro|rw]") ||
		!strings.Contains(text, "-p host:guest[/tcp]") ||
		!strings.Contains(text, "-dry-run") {
		t.Fatalf("create help = %s", text)
	}
	if strings.Contains(text, "Rootfs image path") {
		t.Fatalf("create help exposed low-level supervisor flags: %s", text)
	}
}

func TestHighLevelCommandHelpDoesNotFallThroughToSupervisorFlags(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "start", want: "microagent start"},
		{command: "delete", want: "Confirm workspace deletion without prompting"},
		{command: "status", want: "Workspace name"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			dir := t.TempDir()
			stdoutPath := filepath.Join(dir, "stdout.txt")
			stdout, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			err = run(t.Context(), []string{tt.command, "--help"}, stdout)
			if closeErr := stdout.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatalf("run %s --help: %v", tt.command, err)
			}
			data, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if !strings.Contains(text, tt.want) {
				t.Fatalf("%s help = %s, want %q", tt.command, text, tt.want)
			}
			if strings.Contains(text, "Rootfs image path") || strings.Contains(text, "Read request JSON") {
				t.Fatalf("%s help exposed low-level supervisor flags: %s", tt.command, text)
			}
		})
	}
}

func TestGlobalJSONOutputSwitch(t *testing.T) {
	outputFormat = ""
	t.Cleanup(func() {
		outputFormat = ""
	})
	args := parseGlobalFlags([]string{"--json", "doctor"})
	if outputFormat != "json" {
		t.Fatalf("outputFormat = %q, want json", outputFormat)
	}
	if len(args) != 1 || args[0] != "doctor" {
		t.Fatalf("args = %#v", args)
	}
}

func TestRemovedOutputProfilesFail(t *testing.T) {
	stdout, stderr, code := runMainCapture(t, "--mode=ax", "version")
	if code == 0 {
		t.Fatalf("removed --mode profile succeeded: stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(string(stderr), "unknown command") {
		t.Fatalf("stderr = %q, want unknown command", stderr)
	}

	t.Setenv("MICROAGENT_MODE", "ax")
	stdout, stderr, code = runMainCapture(t, "--json", "version")
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("MICROAGENT_MODE affected CLI: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal(stdout, &result); err != nil {
		t.Fatalf("decode --json output %q: %v", stdout, err)
	}
	if result["name"] != "microagent" || result["ok"] != nil {
		t.Fatalf("version JSON = %#v, want bare result", result)
	}
}
