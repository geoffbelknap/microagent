package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// checkLine is one verified fact on the doctor page: a label, a glyph, and
// optional detail plus indented remediation lines. The page renders every
// check as a line — including passing and not-applicable ones — so failure is
// always presence, never a phrase missing from a list.
type checkLine struct {
	label  string
	glyph  string // glyphOK, glyphWarn, or glyphBad
	detail string
	remedy []string
}

const (
	glyphOK   = "✓"
	glyphWarn = "⚠"
	glyphBad  = "✗"
)

func writeDoctorResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	verdict := resp.Verdict
	if verdict == "" {
		verdict = diagnostics.DeriveVerdict(&resp)
	}
	// Identity header: what the host is. Checks — what was verified — never
	// share this line.
	arch := "unknown arch"
	backend := resp.Backend
	if resp.Host != nil {
		if resp.Host.Architecture != "" {
			arch = resp.Host.Architecture
		}
		backend = nonEmpty(backend, resp.Host.Backend)
	}
	fmt.Fprintf(stdout, "Host: %s on %s\n", nonEmpty(backend, "unknown backend"), arch)
	lines := doctorCheckLines(resp)
	if len(lines) > 0 {
		fmt.Fprintln(stdout)
		width := 0
		for _, l := range lines {
			if len([]rune(l.label)) > width {
				width = len([]rune(l.label))
			}
		}
		for _, l := range lines {
			pad := strings.Repeat(" ", width-len([]rune(l.label)))
			fmt.Fprintf(stdout, "  %s%s  %s", l.label, pad, colorizeGlyph(stdout, l.glyph))
			if l.detail != "" {
				fmt.Fprintf(stdout, " %s", l.detail)
			}
			fmt.Fprintln(stdout)
			for _, r := range l.remedy {
				fmt.Fprintf(stdout, "  %s  %s\n", strings.Repeat(" ", width), r)
			}
		}
	}
	// Probe issues render once, here: the failing check lines above carry the
	// short reason, this block carries the full diagnosis and remediation.
	if resp.Error != "" {
		fmt.Fprintln(stdout)
		fmt.Fprintf(stdout, "Problems: %s\n", resp.Error)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, doctorVerdictSentence(verdict, lines))
	return nil
}

// doctorCheckLines builds the page's check list: host boot facts first, then
// one line per declared capability. Paths stay off the healthy lines — a
// passing check names what works, a failing one names what is missing and
// where it was expected.
func doctorCheckLines(resp vmkit.Response) []checkLine {
	h := resp.Host
	if h == nil {
		return nil
	}
	var lines []checkLine
	appleVF := h.Backend == vmkit.BackendAppleVF
	switch {
	case appleVF && h.VirtualizationSupported && h.FrameworkAvailable:
		lines = append(lines, checkLine{"virtualization", glyphOK, "Virtualization.framework", nil})
	case appleVF:
		lines = append(lines, checkLine{"virtualization", glyphBad, "Virtualization.framework is not available", nil})
	case h.KVMAvailable:
		lines = append(lines, checkLine{"virtualization", glyphOK, "KVM", nil})
	case h.VirtualizationSupported:
		lines = append(lines, checkLine{"virtualization", glyphBad, "/dev/kvm is not available (the CPU supports virtualization)", nil})
	default:
		lines = append(lines, checkLine{"virtualization", glyphBad, "not supported by this CPU", nil})
	}
	if !appleVF {
		if h.FrameworkAvailable {
			lines = append(lines, checkLine{"vmm", glyphOK, h.BinaryVersion, nil})
		} else {
			lines = append(lines, checkLine{"vmm", glyphBad, "firecracker binary not found", nil})
		}
	}
	if h.SupervisorAvailable {
		lines = append(lines, checkLine{"supervisor", glyphOK, "", nil})
	} else {
		lines = append(lines, checkLine{"supervisor", glyphBad, missingAt("not found", h.SupervisorPath), nil})
	}
	if h.GuestInitAvailable {
		lines = append(lines, checkLine{"guest init", glyphOK, "", nil})
	} else {
		lines = append(lines, checkLine{"guest init", glyphBad, missingAt("not found", h.GuestInitPath), nil})
	}
	if resp.Kernel != nil {
		if resp.Kernel.Status == "present" {
			lines = append(lines, checkLine{"kernel", glyphOK, "installed", nil})
		} else {
			lines = append(lines, checkLine{"kernel", glyphBad, nonEmpty(resp.Kernel.Status, "unknown"), []string{"install it with `microagent kernel install`"}})
		}
	}
	if h.VsockAvailable {
		lines = append(lines, checkLine{"vsock", glyphOK, "", nil})
	} else if appleVF {
		lines = append(lines, checkLine{"vsock", glyphBad, "not available", nil})
	} else {
		lines = append(lines, checkLine{"vsock", glyphBad, "/dev/vhost-vsock is not available", nil})
	}
	lines = append(lines, networkingCheckLine(h))
	if h.ConfinementActive {
		lines = append(lines, checkLine{"confinement", glyphOK, fmt.Sprintf("active (%s)", nonEmpty(h.ConfinementMode, "unknown")), nil})
	} else {
		lines = append(lines, checkLine{"confinement", glyphWarn, "off — the VMM process is not confined", nil})
	}
	for _, c := range h.Capabilities {
		lines = append(lines, capabilityCheckLine(h, c))
	}
	return lines
}

func networkingCheckLine(h *vmkit.HostSupport) checkLine {
	isolated := h.IsolatedNetworkReady
	user := h.UserNetworkReady
	if h.Backend == vmkit.BackendAppleVF {
		ready := h.FrameworkAvailable && h.VirtualizationSupported && h.SupervisorAvailable
		isolated, user = ready, ready
	}
	switch {
	case isolated && user:
		return checkLine{"networking", glyphOK, "isolated, user", nil}
	case isolated:
		return checkLine{"networking", glyphWarn, "isolated ready; user is not", nil}
	case user:
		return checkLine{"networking", glyphWarn, "user ready; isolated is not", nil}
	default:
		return checkLine{"networking", glyphBad, "not ready", nil}
	}
}

// capabilityLabels maps declared capability identifiers to the label a reader
// scans; identifiers stay in JSON for machines.
var capabilityLabels = map[vmkit.FeatureCapability]string{
	vmkit.FeatureCapabilityStructuredExec:   "structured exec",
	vmkit.FeatureCapabilityNetworkPublish:   "port publish",
	vmkit.FeatureCapabilityLiveNetworkApply: "live port apply",
	vmkit.FeatureCapabilityOfflineFileCopy:  "file copy",
	vmkit.FeatureCapabilityLiveFileCopy:     "live file copy",
	vmkit.FeatureCapabilityPauseResume:      "pause/resume",
	vmkit.FeatureCapabilitySnapshotCreate:   "snapshot create",
	vmkit.FeatureCapabilitySnapshotRestore:  "snapshot restore",
	vmkit.FeatureCapabilitySnapshotFork:     "snapshot fork",
	vmkit.FeatureCapabilityBrokerEndpoints:  "secret broker",
	vmkit.FeatureCapabilityConsole:          "console",
	vmkit.FeatureCapabilityEgressMediation:  "egress mediation",
}

func capabilityLabel(capability vmkit.FeatureCapability) string {
	if label, ok := capabilityLabels[capability]; ok {
		return label
	}
	return string(capability)
}

func capabilityCheckLine(h *vmkit.HostSupport, c vmkit.CapabilityDiagnostic) checkLine {
	line := checkLine{label: capabilityLabel(c.Capability), glyph: glyphOK}
	if c.Ready {
		switch c.Capability {
		case vmkit.FeatureCapabilityConsole:
			line.detail = h.ConsoleMode
		case vmkit.FeatureCapabilityOfflineFileCopy:
			line.detail = "offline"
		}
		return line
	}
	// A missing core capability means no workspace can be used; safety and
	// feature capabilities degrade the host and fail closed where needed.
	line.glyph = glyphWarn
	if vmkit.CapabilityTierOf(c.Capability) == vmkit.CapabilityTierCore {
		line.glyph = glyphBad
	}
	if len(c.Missing) > 0 {
		line.detail = "missing: " + strings.Join(c.Missing, ", ")
	} else {
		line.detail = "not ready"
	}
	if c.Capability == vmkit.FeatureCapabilityEgressMediation {
		if hint := diagnostics.EgressTProxyRemediation(h); hint != "" {
			line.remedy = append(line.remedy, hint)
		}
	}
	return line
}

// doctorVerdictSentence is the rollup, last and gated on the whole page: it
// states what will work and what will not, instead of a bare state word.
func doctorVerdictSentence(verdict string, lines []checkLine) string {
	switch verdict {
	case vmkit.VerdictOK:
		return "Workspaces will boot and run on this host. Everything this backend advertises is ready."
	case vmkit.VerdictDegraded:
		var unready []string
		for _, l := range lines {
			if l.glyph != glyphOK {
				unready = append(unready, l.label)
			}
		}
		if len(unready) == 0 {
			return "Workspaces will boot and run on this host, but doctor reported problems above."
		}
		return fmt.Sprintf("Workspaces will boot and run on this host, but not everything is ready: %s. Whatever needs a missing capability fails closed until it is fixed.", strings.Join(unready, ", "))
	default:
		return "This host cannot boot workspaces. Fix the ✗ items above and run doctor again."
	}
}

func missingAt(reason, path string) string {
	if strings.TrimSpace(path) == "" {
		return reason
	}
	return fmt.Sprintf("%s (expected at %s)", reason, path)
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

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
