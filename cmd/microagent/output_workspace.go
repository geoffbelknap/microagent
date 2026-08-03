package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func writeDispatchResult(stdout, stderr *os.File, result workspace.DispatchResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	// A dry run has no guest streams and no audit; the plan is the output.
	if result.Plan != nil {
		return writeJSON(stdout, result)
	}
	if result.Result != nil {
		writeGuestStream(stdout, result.Result.Stdout)
		writeGuestStream(stderr, result.Result.Stderr)
	}
	// The "what did it do on the network" receipt — mediator-written, so the
	// guest cannot forge it. It goes to stderr so stdout carries only the task
	// output.
	a := result.Audit
	fmt.Fprintf(stderr, "Egress: %d decision(s)\n", a.DecisionCount)
	for _, host := range sortedHosts(a.AllowByHost) {
		fmt.Fprintf(stderr, "  allow %s (%d)\n", host, a.AllowByHost[host])
	}
	for _, host := range sortedHosts(a.DenyByHost) {
		fmt.Fprintf(stderr, "  deny  %s (%d)\n", host, a.DenyByHost[host])
	}
	return nil
}

// quarantineEnvelope keeps quarantine's structured output SHAPE-COMPATIBLE:
// vmkit.Response is embedded, so its fields stay at the top level exactly where
// every existing consumer reads them, and the capture fields are added
// alongside. Nesting the response under a key instead would silently break
// every parser of `quarantine --json`.
type quarantineEnvelope struct {
	vmkit.Response
	Captured     bool                      `json:"captured"`
	CaptureTag   string                    `json:"captureTag,omitempty"`
	CaptureError string                    `json:"captureError,omitempty"`
	Incident     workspace.IncidentReceipt `json:"incident"`
}

// writeQuarantineResult reports containment AND what happened to the evidence.
// A failed capture must be loud: the workspace is contained either way, so a
// quiet failure would look identical to a successful capture while the volatile
// state it was meant to preserve is already gone.
func writeQuarantineResult(stdout *os.File, result workspace.QuarantineResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, quarantineEnvelope{
			Response:     result.Response,
			Captured:     result.Captured,
			CaptureTag:   result.CaptureTag,
			CaptureError: result.CaptureError,
			Incident:     result.Incident,
		})
	}
	if err := writeResponse(stdout, result.Response); err != nil {
		return err
	}
	switch {
	case result.Captured:
		fmt.Fprintf(stdout, "  evidence captured as %s (guest secrets RETAINED, not restorable)\n", result.CaptureTag)
	case result.CaptureError != "":
		fmt.Fprintf(stdout, "  WARNING: evidence capture failed, contained anyway: %s\n", result.CaptureError)
	}
	fmt.Fprintf(stdout, "  incident: session=%s egress=%d broker=%d secrets=%d complete=%t\n",
		result.Incident.SessionID, result.Incident.Egress.DecisionCount,
		result.Incident.Broker.RequestCount, result.Incident.Secrets.AccessCount,
		result.Incident.Complete)
	return nil
}

func writeWorkspaceResult(stdout *os.File, result workspaceResult) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{})
}

// writeRunResult prints `run` output docker-style: guest stdout/stderr land on
// the matching host streams and stdout carries nothing else, so pipes see only
// the task output. Workspace metadata stays available via --json and
// `workspace show`. The workspace name is printed to stderr only when its state
// outlives the run: --keep, or a failure (Run preserves state for debugging).
func writeRunResult(stdout, stderr *os.File, result workspaceResult, keep bool, runErr error) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	if result.Result != nil {
		writeGuestStream(stdout, result.Result.Stdout)
		writeGuestStream(stderr, result.Result.Stderr)
	}
	if runErr != nil {
		if result.Workspace != "" {
			fmt.Fprintf(stderr, "Workspace: %s\n", result.Workspace)
		}
		if result.SerialPath != "" {
			fmt.Fprintf(stderr, "Console log: %s\n", result.SerialPath)
		}
		return nil
	}
	if keep && result.Workspace != "" {
		fmt.Fprintf(stderr, "Workspace: %s\n", result.Workspace)
	}
	if result.Response.Error != "" {
		fmt.Fprintf(stderr, "Error: %s\n", result.Response.Error)
	}
	return nil
}

// writeGuestStream forwards one captured guest stream to a host stream,
// stripping control characters that could reprogram the operator's terminal.
func writeGuestStream(out *os.File, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	fmt.Fprint(out, sanitizeHumanOutput(content))
	if !strings.HasSuffix(content, "\n") {
		fmt.Fprintln(out)
	}
}

func sortedHosts(counts map[string]int) []string {
	hosts := make([]string, 0, len(counts))
	for host := range counts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

type workspaceResultOptions struct {
	SuppressSuccessfulResult bool
	CreatedSummary           bool
	StartedSummary           bool
}

func writeWorkspaceResultWithOptions(stdout *os.File, result workspaceResult, opts workspaceResultOptions) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	switch {
	case opts.CreatedSummary:
		fmt.Fprintf(stdout, "Created workspace: %s\n", result.Workspace)
	case opts.StartedSummary:
		fmt.Fprintf(stdout, "Started workspace: %s\n", result.Workspace)
	default:
		fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	}
	if result.Response.Event != nil {
		fmt.Fprintf(stdout, "State: %s\n", humanWorkspaceState(result.Response.Event.State, opts))
	} else if result.FinalState != "" {
		fmt.Fprintf(stdout, "State: %s\n", humanWorkspaceState(vmkit.VMState(result.FinalState), opts))
	}
	if result.RootfsPath != "" {
		fmt.Fprintf(stdout, "Rootfs: %s\n", result.RootfsPath)
	}
	if result.Profile != "" {
		fmt.Fprintf(stdout, "Profile: %s\n", result.Profile)
	}
	if result.Restart != "" {
		fmt.Fprintf(stdout, "Restart: %s\n", result.Restart)
	}
	if result.Network.Mode != "" {
		fmt.Fprintf(stdout, "Network: %s\n", result.Network.Mode)
	}
	if strings.TrimSpace(result.ConsoleShell) != "" {
		fmt.Fprintf(stdout, "Shell: %s\n", strings.TrimSpace(result.ConsoleShell))
	}
	if strings.TrimSpace(result.Hostname) != "" {
		fmt.Fprintf(stdout, "Hostname: %s\n", strings.TrimSpace(result.Hostname))
	}
	if len(result.Artifacts.Ingress) != 0 || len(result.Artifacts.Egress) != 0 {
		fmt.Fprintf(stdout, "Artifacts: ingress=%d egress=%d\n", len(result.Artifacts.Ingress), len(result.Artifacts.Egress))
	}
	if result.Resources.MemoryMiB != 0 || result.Resources.CPUCount != 0 || result.Resources.SizeMiB != 0 {
		fmt.Fprintf(stdout, "Resources: memory=%dMiB cpus=%d", result.Resources.MemoryMiB, result.Resources.CPUCount)
		if result.Resources.SizeMiB != 0 {
			fmt.Fprintf(stdout, " disk=%dMiB", result.Resources.SizeMiB)
		}
		if usage := result.Response.RootfsUsage; usage != nil {
			fmt.Fprintf(stdout, " used=%dMiB(%d%%) host=%dMiB", usage.FSUsedMiB, usage.UsedPercent, usage.HostAllocatedMiB)
			if usage.Assessment != "" {
				fmt.Fprintf(stdout, " [%s]", usage.Assessment)
			}
		}
		fmt.Fprintln(stdout)
	}
	if result.KernelPath != "" {
		fmt.Fprintf(stdout, "Kernel: %s\n", result.KernelPath)
	}
	if result.SerialPath != "" {
		fmt.Fprintf(stdout, "Console log: %s\n", result.SerialPath)
	}
	if result.Result != nil && !(opts.SuppressSuccessfulResult && result.Result.ExitCode == 0 && strings.TrimSpace(result.Result.Error) == "") {
		fmt.Fprintf(stdout, "Exit code: %d\n", result.Result.ExitCode)
		if strings.TrimSpace(result.Result.Stdout) != "" {
			fmt.Fprintf(stdout, "\n%s", sanitizeHumanOutput(result.Result.Stdout))
			if !strings.HasSuffix(result.Result.Stdout, "\n") {
				fmt.Fprintln(stdout)
			}
		}
		if strings.TrimSpace(result.Result.Stderr) != "" {
			fmt.Fprintf(stdout, "\nStderr:\n%s", sanitizeHumanOutput(result.Result.Stderr))
			if !strings.HasSuffix(result.Result.Stderr, "\n") {
				fmt.Fprintln(stdout)
			}
		}
	}
	if result.Response.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", result.Response.Error)
	}
	return nil
}

// writeDeleteOutcomes reports what delete did, honestly: a removed workspace
// and an already-absent one both succeed, but they read differently, because
// a caller who typo'd a name (or whose glob never expanded) must not be told
// a deletion happened. One name keeps the historical bare-object JSON shape;
// several wrap in a results array so the stream stays one JSON document.
func writeDeleteOutcomes(stdout *os.File, outcomes []deleteOutcome) error {
	if outputJSON(stdout) {
		if len(outcomes) == 1 {
			return writeJSON(stdout, outcomes[0].DeleteResult)
		}
		return writeJSON(stdout, map[string]any{"results": outcomes})
	}
	for _, outcome := range outcomes {
		switch {
		case outcome.OK && outcome.Deleted:
			fmt.Fprintf(stdout, "Deleted workspace: %s\n", outcome.Workspace)
		case outcome.OK:
			fmt.Fprintf(stdout, "Workspace %s did not exist; nothing deleted.\n", outcome.Workspace)
		default:
			if err := writeResponse(stdout, outcome.Response); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeCreateResult(stdout *os.File, result workspaceResult, err error) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{
		SuppressSuccessfulResult: err == nil,
		CreatedSummary:           err == nil,
	})
}

func writeStartResult(stdout *os.File, result workspaceResult, err error) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{
		SuppressSuccessfulResult: err == nil,
		StartedSummary:           err == nil,
	})
}

func humanWorkspaceState(state vmkit.VMState, opts workspaceResultOptions) string {
	if opts.CreatedSummary && state == vmkit.StateStopped {
		return "ready (stopped)"
	}
	return string(state)
}

func writeApplyResult(stdout *os.File, result applyResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.State != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.State)
	}
	if len(result.Applied) != 0 {
		fmt.Fprintf(stdout, "Applied: %s\n", strings.Join(result.Applied, ", "))
	}
	if result.Network.Mode != "" {
		fmt.Fprintf(stdout, "Network: %s\n", result.Network.Mode)
	}
	for _, forward := range result.Network.PortForwards {
		host := strings.TrimSpace(forward.Host)
		if host == "" {
			host = "127.0.0.1"
		}
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		fmt.Fprintf(stdout, "Forward: %s:%d -> %d/%s\n", host, forward.HostPort, forward.GuestPort, protocol)
	}
	if result.Reloaded {
		fmt.Fprintln(stdout, "Reloaded: port forwards")
	}
	if result.Response != nil && result.Response.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", result.Response.Error)
	}
	return nil
}

func sanitizeHumanOutput(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

func writeCopyResult(stdout *os.File, result copyResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.Artifact != "" {
		fmt.Fprintf(stdout, "Artifact: %s\n", result.Artifact)
	}
	fmt.Fprintf(stdout, "Disk: %s\n", result.Disk)
	fmt.Fprintf(stdout, "Direction: %s\n", result.Direction)
	fmt.Fprintf(stdout, "Source: %s\n", result.Source)
	fmt.Fprintf(stdout, "Target: %s\n", result.Target)
	if result.Bytes != 0 {
		fmt.Fprintf(stdout, "Bytes: %d\n", result.Bytes)
	}
	return nil
}

func writeArtifactsResult(stdout *os.File, result artifactsResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "Ingress: %d\n", len(result.Artifacts.Ingress))
	for _, artifact := range result.Artifacts.Ingress {
		fmt.Fprintf(stdout, "  %s %s %s\n", artifact.Name, artifact.Kind, artifact.Mountpoint)
	}
	fmt.Fprintf(stdout, "Egress: %d\n", len(result.Artifacts.Egress))
	for _, artifact := range result.Artifacts.Egress {
		fmt.Fprintf(stdout, "  %s %s\n", artifact.Name, artifact.Path)
	}
	return nil
}

func writeNetworkResult(stdout *os.File, result workspaceNetworkResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	if result.State != "" {
		fmt.Fprintf(stdout, "State: %s\n", result.State)
	}
	if result.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", result.Backend)
	}
	writeNetworkConfig(stdout, "Network", result.Network)
	if result.Runtime != nil {
		writeNetworkConfig(stdout, "Runtime network", *result.Runtime)
	}
	return nil
}

func writeNetworkConfig(stdout *os.File, label string, network vmkit.NetworkConfig) {
	fmt.Fprintf(stdout, "%s: %s\n", label, network.Mode)
	if network.IP != "" {
		fmt.Fprintf(stdout, "IP: %s\n", network.IP)
	}
	if len(network.DNS) != 0 {
		fmt.Fprintf(stdout, "DNS: %s\n", strings.Join(network.DNS, ", "))
	}
	if len(network.Routes) != 0 {
		fmt.Fprintf(stdout, "Routes: %s\n", strings.Join(network.Routes, ", "))
	}
	for _, forward := range network.PortForwards {
		host := forward.Host
		if host == "" {
			host = "*"
		}
		protocol := strings.TrimSpace(forward.Protocol)
		if protocol == "" {
			protocol = "tcp"
		}
		fmt.Fprintf(stdout, "Forward: %s %s:%d -> guest:%d\n", protocol, host, forward.HostPort, forward.GuestPort)
	}
}

func writeSuperviseResult(stdout *os.File, result superviseResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "Policy: %s\n", result.Policy)
	fmt.Fprintf(stdout, "Restarts: %d\n", result.Restarts)
	if result.FinalState != "" {
		fmt.Fprintf(stdout, "Final state: %s\n", colorizeState(stdout, result.FinalState))
	}
	return nil
}

func writeWaitResult(stdout *os.File, result waitResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "State: %s\n", colorizeState(stdout, result.State))
	return nil
}

// writeRunningWorkspaceList renders ps: the live view over the saved
// inventory. An empty live view says what was filtered out, so "nothing
// running" cannot be misread as "no workspaces at all".
func writeRunningWorkspaceList(stdout *os.File, running []workspaceListEntry, total int) error {
	if !outputJSON(stdout) && len(running) == 0 && total > 0 {
		fmt.Fprintf(stdout, "No running workspaces (%d saved). Run 'microagent list' to see them.\n", total)
		return nil
	}
	return writeWorkspaceList(stdout, running)
}

func writeWorkspaceList(stdout *os.File, entries []workspaceListEntry) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspaces": entries})
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No workspaces.")
		return nil
	}
	cols := []tableColumn{
		{Header: "NAME", Legacy: 24, Min: 12, Max: 32, Flex: true},
		{Header: "STATE", Legacy: 12, Min: 5, Max: 12},
		{Header: "BACKEND", Legacy: 12, Min: 7, Max: 12},
		{Header: "PROFILE", Legacy: 12, Min: 7, Max: 16},
		{Header: "NETWORK", Legacy: 10, Min: 7, Max: 10},
		{Header: "RESTART", Legacy: 0, Min: 7},
	}
	rows := make([][]tableCell, len(entries))
	for i, entry := range entries {
		rows[i] = []tableCell{
			cell(entry.Name),
			{Text: entry.State, Colorize: func(s string) string { return colorizeState(stdout, s) }},
			cell(entry.Backend),
			cell(entry.Profile),
			cell(entry.Network),
			cell(entry.Restart),
		}
	}
	renderTable(stdout, cols, rows)
	return nil
}
