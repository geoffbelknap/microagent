package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func writeDoctorResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	fmt.Fprintf(stdout, "Backend: %s\n", nonEmpty(resp.Backend, "unknown"))
	fmt.Fprintf(stdout, "Status: %s\n", colorizeState(stdout, humanOK(resp.OK)))
	if resp.Host != nil {
		fmt.Fprintf(stdout, "Host: %s", nonEmpty(resp.Host.Architecture, "unknown"))
		if resp.Host.SupervisorPath != "" {
			fmt.Fprintf(stdout, ", supervisor=%s", resp.Host.SupervisorPath)
		}
		if resp.Host.SupervisorAvailable {
			fmt.Fprint(stdout, ", supervisor available")
		}
		if resp.Host.FrameworkAvailable {
			fmt.Fprint(stdout, ", framework available")
		}
		if resp.Host.VirtualizationSupported {
			fmt.Fprint(stdout, ", virtualization supported")
		}
		if resp.Host.KVMAvailable {
			fmt.Fprint(stdout, ", KVM available")
		}
		if resp.Host.VsockAvailable {
			fmt.Fprint(stdout, ", vsock available")
		}
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Console: %s", availability(resp.Host.ConsoleAvailable))
		if resp.Host.ConsoleMode != "" {
			fmt.Fprintf(stdout, " (%s)", resp.Host.ConsoleMode)
		}
		fmt.Fprintln(stdout)
		confinementState := "inactive"
		if resp.Host.ConfinementActive {
			confinementState = "active"
		}
		fmt.Fprintf(stdout, "Confinement: %s (%s)\n", nonEmpty(resp.Host.ConfinementMode, "off"), confinementState)
		printNetworkingSection(stdout, resp.Host)
		printCapabilitiesSection(stdout, resp.Host)
	}
	if resp.Kernel != nil {
		fmt.Fprintf(stdout, "Kernel: %s", nonEmpty(resp.Kernel.Status, "unknown"))
		if resp.Kernel.Path != "" {
			fmt.Fprintf(stdout, " (%s)", resp.Kernel.Path)
		}
		fmt.Fprintln(stdout)
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

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
	colorReady := func(b bool) string { return colorizeState(stdout, ready(b)) }
	if host.Backend == vmkit.BackendAppleVF {
		networkReady := host.FrameworkAvailable && host.VirtualizationSupported && host.SupervisorAvailable
		fmt.Fprintf(stdout, "Networking: isolated %s, user %s\n", colorReady(networkReady), colorReady(networkReady))
		return
	}
	fmt.Fprintf(stdout, "Networking: isolated %s, user %s\n",
		colorReady(host.IsolatedNetworkReady),
		colorReady(host.UserNetworkReady))
	if host.Backend == vmkit.BackendLinuxKVM {
		status := "PASS"
		if !host.EgressTProxyReady {
			status = "WARN"
		}
		fmt.Fprintf(stdout, "Egress TPROXY modules: %s", colorizeState(stdout, status))
		if len(host.EgressTProxyMissingModules) > 0 {
			fmt.Fprintf(stdout, " (missing: %s)", strings.Join(host.EgressTProxyMissingModules, ", "))
		}
		fmt.Fprintln(stdout)
		if hint := diagnostics.EgressTProxyRemediation(host); hint != "" {
			fmt.Fprintf(stdout, "  %s\n", hint)
		}
	}
}

// printCapabilitiesSection renders the L1 (prerequisites-verified) status of the
// backend's declared capabilities: a ready/total rollup plus a line naming the
// missing prerequisites for any capability that is not ready. Capabilities the
// backend does not populate (e.g. backends without a wired registry) render
// nothing.
func printCapabilitiesSection(stdout *os.File, host *vmkit.HostSupport) {
	if host == nil || len(host.Capabilities) == 0 {
		return
	}
	ready := 0
	var degraded []vmkit.CapabilityDiagnostic
	for _, c := range host.Capabilities {
		if c.Ready {
			ready++
		} else {
			degraded = append(degraded, c)
		}
	}
	status := "PASS"
	if len(degraded) > 0 {
		status = "WARN"
	}
	fmt.Fprintf(stdout, "Capabilities: %s (%d/%d ready)\n", colorizeState(stdout, status), ready, len(host.Capabilities))
	for _, c := range degraded {
		if len(c.Missing) > 0 {
			fmt.Fprintf(stdout, "  %s: missing %s\n", c.Capability, strings.Join(c.Missing, ", "))
		} else {
			fmt.Fprintf(stdout, "  %s: not ready\n", c.Capability)
		}
	}
}

func writeRuntimeContract(stdout *os.File, contract vmkit.RuntimeContract) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, contract)
	}
	fmt.Fprintf(stdout, "Contract: %s\n", contract.Version)
	fmt.Fprintf(stdout, "Backends: %s\n", strings.Join(contract.Backends, ", "))
	fmt.Fprintf(stdout, "Commands: %s\n", strings.Join(contractItemNames(contract.Commands), ", "))
	fmt.Fprintf(stdout, "States: %s\n", strings.Join(contractStateNames(contract.States), ", "))
	fmt.Fprintf(stdout, "Readiness: %s\n", strings.Join(contractItemNames(contract.ReadinessSignals), ", "))
	fmt.Fprintf(stdout, "Result: %s\n", strings.Join(contractItemNames(contract.ResultFields), ", "))
	fmt.Fprintf(stdout, "Artifacts: %s\n", strings.Join(contractItemNames(contract.ArtifactChannels), ", "))
	fmt.Fprintf(stdout, "Mediation: %s\n", contract.Mediation.Primitive)
	return nil
}

func writeResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	fmt.Fprintf(stdout, "Status: %s\n", colorizeState(stdout, humanOK(resp.OK)))
	if resp.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", resp.Backend)
	}
	if resp.Event != nil {
		fmt.Fprintf(stdout, "Workspace: %s\n", resp.Event.Identity.RuntimeID)
		fmt.Fprintf(stdout, "State: %s\n", colorizeState(stdout, string(resp.Event.State)))
		if resp.RestartPolicy != "" {
			fmt.Fprintf(stdout, "Restart: %s\n", resp.RestartPolicy)
		}
		if resp.Network != nil && resp.Network.Mode != "" {
			fmt.Fprintf(stdout, "Network: %s\n", resp.Network.Mode)
		}
		if resp.Mediation != nil && resp.Mediation.Enabled {
			fmt.Fprintf(stdout, "Mediation: required=%t failClosed=%t port=%d target=%s\n", resp.Mediation.Required, resp.Mediation.FailClosed, resp.Mediation.Port, resp.Mediation.Target)
		}
		if resp.Verification != nil {
			fmt.Fprintf(stdout, "Verification: %s\n", colorizeState(stdout, humanOK(resp.Verification.OK)))
		}
		if resp.Readiness != nil {
			mediation := "disabled"
			if resp.Mediation != nil && resp.Mediation.Enabled {
				mediation = humanReady(resp.Readiness.MediationReady.Ready)
			}
			fmt.Fprintf(stdout, "Readiness: guest=%s shell=%s result=%s mediation=%s\n",
				colorizeState(stdout, humanReady(resp.Readiness.GuestReady.Ready)),
				colorizeState(stdout, humanReady(resp.Readiness.ShellReady.Ready)),
				colorizeState(stdout, humanReady(resp.Readiness.ResultReady.Ready)),
				colorizeState(stdout, mediation),
			)
		}
		if resp.Artifacts != nil {
			fmt.Fprintf(stdout, "Artifacts: ingress=%d egress=%d\n", len(resp.Artifacts.Ingress), len(resp.Artifacts.Egress))
		}
		if resp.Result != nil {
			fmt.Fprintf(stdout, "Exit code: %d\n", resp.Result.ExitCode)
			if resp.Result.CompletedAt != "" {
				fmt.Fprintf(stdout, "Completed: %s\n", resp.Result.CompletedAt)
			}
		}
		if resp.Event.Detail != "" {
			fmt.Fprintf(stdout, "Detail: %s\n", resp.Event.Detail)
		}
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func contractItemNames(items []vmkit.ContractItem) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}

func contractStateNames(states []vmkit.ContractState) []string {
	names := make([]string, 0, len(states))
	for _, state := range states {
		names = append(names, string(state.Name))
	}
	return names
}

func writeResultResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	if resp.Result == nil {
		if resp.Error != "" {
			fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
		}
		return nil
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", resp.Result.Identity.RuntimeID)
	if resp.Result.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", resp.Result.Backend)
	}
	fmt.Fprintf(stdout, "Exit code: %d\n", resp.Result.ExitCode)
	if resp.Result.StartedAt != "" {
		fmt.Fprintf(stdout, "Started: %s\n", resp.Result.StartedAt)
	}
	if resp.Result.CompletedAt != "" {
		fmt.Fprintf(stdout, "Completed: %s\n", resp.Result.CompletedAt)
	}
	if resp.Result.ResultPath != "" {
		fmt.Fprintf(stdout, "Result: %s\n", resp.Result.ResultPath)
	}
	if strings.TrimSpace(resp.Result.Stdout) != "" {
		fmt.Fprintf(stdout, "\n%s", sanitizeHumanOutput(resp.Result.Stdout))
		if !strings.HasSuffix(resp.Result.Stdout, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if strings.TrimSpace(resp.Result.Stderr) != "" {
		fmt.Fprintf(stdout, "\nStderr:\n%s", sanitizeHumanOutput(resp.Result.Stderr))
		if !strings.HasSuffix(resp.Result.Stderr, "\n") {
			fmt.Fprintln(stdout)
		}
	}
	if resp.Result.Error != "" {
		fmt.Fprintf(stdout, "Result error: %s\n", resp.Result.Error)
	}
	if resp.Error != "" {
		fmt.Fprintf(stdout, "Error: %s\n", resp.Error)
	}
	return nil
}

func humanOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "failed"
}

func humanReady(ready bool) string {
	if ready {
		return "ready"
	}
	return "not-ready"
}

func availability(ok bool) string {
	if ok {
		return "available"
	}
	return "unavailable"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
