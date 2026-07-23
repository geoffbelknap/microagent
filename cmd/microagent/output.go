package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func writeJSON(stdout *os.File, value any) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func writeVersion(stdout *os.File) error {
	if outputStructured() {
		return writeJSON(stdout, map[string]any{
			"name":    "microagent",
			"version": version,
		})
	}
	fmt.Fprintf(stdout, "microagent %s\n", version)
	return nil
}

func outputStructured() bool {
	return currentOutputMode() == outputModeAX || outputFormat == "json"
}

// outputJSON decides whether a command should render JSON or text.
// Precedence, exactly: explicit format flag (outputFormat, set by --output/
// --json) > MICROAGENT_OUTPUT env > (mode == AX defaults to json) > TTY
// detection. AX no longer unconditionally forces JSON: an explicit format
// flag or explicit MICROAGENT_OUTPUT still wins under AX; AX only wins over
// TTY detection.
func outputJSON(stdout *os.File) bool {
	switch outputFormat {
	case "json":
		return true
	case "text":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MICROAGENT_OUTPUT"))) {
	case "json":
		return true
	case "text":
		return false
	}
	if currentOutputMode() == outputModeAX {
		return true
	}
	info, err := stdout.Stat()
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeCharDevice == 0
}

func rootfsProgress(stdout *os.File, prefix string) rootfs.ProgressFunc {
	if outputJSON(stdout) {
		return nil
	}
	printer := &progressPrinter{
		out:         os.Stderr,
		prefix:      prefix,
		interactive: fileIsTerminal(os.Stderr),
	}
	return printer.print
}

type progressPrinter struct {
	out         *os.File
	prefix      string
	interactive bool
	active      bool
}

func (p *progressPrinter) print(event rootfs.ProgressEvent) {
	line := fmt.Sprintf("%s: %s", p.prefix, formatProgressEvent(event))
	if !p.interactive {
		fmt.Fprintln(p.out, line)
		return
	}
	if isProgressEvent(event) {
		fmt.Fprintf(p.out, "\r\033[2K%s", line)
		p.active = true
		if event.Phase == "complete" {
			fmt.Fprintln(p.out)
			p.active = false
		}
		return
	}
	if p.active {
		fmt.Fprintln(p.out)
		p.active = false
	}
	fmt.Fprintln(p.out, line)
}

func isProgressEvent(event rootfs.ProgressEvent) bool {
	return event.Indeterminate || event.Total > 0 || event.TotalBytes > 0
}

func formatProgressEvent(event rootfs.ProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = event.Phase
	}
	if event.Indeterminate {
		elapsed := event.Current
		if elapsed < 0 {
			elapsed = 0
		}
		spinner := []string{"|", "/", "-", "\\"}
		if elapsed > 0 {
			return fmt.Sprintf("[%s] %s (%s)", spinner[elapsed%int64(len(spinner))], message, formatElapsed(elapsed))
		}
		return fmt.Sprintf("[%s] %s", spinner[0], message)
	}
	if event.Total <= 0 && event.TotalBytes <= 0 {
		return message
	}
	var done, total int64
	if event.TotalBytes > 0 {
		done = event.Bytes
		total = event.TotalBytes
	} else {
		done = event.Current
		total = event.Total
	}
	if total <= 0 {
		return message
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	bar := progressBar(done, total, 20)
	if event.TotalBytes > 0 {
		if event.Total > 0 {
			return fmt.Sprintf("%s %s %s/%s (layer %d/%d)", bar, message, formatBytes(done), formatBytes(total), event.Current, event.Total)
		}
		return fmt.Sprintf("%s %s %s/%s", bar, message, formatBytes(done), formatBytes(total))
	}
	return fmt.Sprintf("%s %s %d/%d", bar, message, event.Current, event.Total)
}

func progressBar(done, total int64, width int) string {
	if width <= 0 {
		width = 20
	}
	filled := 0
	if total > 0 {
		filled = int(done * int64(width) / total)
	}
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func formatElapsed(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds = seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	hours := minutes / 60
	minutes = minutes % 60
	return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	for _, suffix := range units {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f%s", size, suffix)
		}
	}
	return fmt.Sprintf("%.1fPiB", size/unit)
}

func fileIsTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// parseGlobalFlags extracts the global output flags (--json, --output,
// --mode) wherever they appear in an ordinary command line. --text and
// --human are no longer global flags: they are left in args untouched, where
// they fail as an unrecognized flag at the command's own flagset (see
// MIGRATION.md). Use "--output text" instead.
//
// It first checks whether args is actually a special-mode re-exec line —
// "--windows-hyperv-listener", "--windows-hyperv-deadman",
// "--host-worker-mediator", or "--egress-datapath" as the first token — and
// if so returns args verbatim, untouched, with no globals set. Those argvs
// are built and consumed internally (see internal/hostworker/process.go and
// the windows-hyperv supervisor) and are not ordinary microagent command
// lines; walking them looking for "--mode"/"--output" would silently corrupt
// a value meant for that special mode (e.g. the mediator's own "--mode
// policy") rather than any global output flag.
//
// For everything else, extraction always stops at a literal "--". "--output
// v" / "--output=v" is only extracted when v normalizes to a known output
// format, and "--mode v" / "--mode=v" only when v names a known output mode;
// an unrecognized value leaves both the flag and its value token in args
// untouched, so a command-owned flag that happens to be spelled "--output"
// or "--mode" (e.g. create/start's own "--output name=/guest/path" artifact
// declaration) is never mistaken for the global flag. For commands that
// carry a guest payload (TrailingArgs), known workspace value flags
// (workspaceValueFlags) are skipped over together with their value token
// once past the command word, so a value like "alpine" in "--image alpine"
// is never mistaken for the guest/payload positional; this mirrors (but does
// not fully replicate) reorderArgsStopAtGuestCommand in main.go, which
// additionally distinguishes an image given as a bare positional from one
// given via --image. The first true positional after the command word
// starts guest/payload territory — nothing from there on is touched.
func parseGlobalFlags(args []string) []string {
	if len(args) > 0 {
		switch args[0] {
		case "--windows-hyperv-listener", "--windows-hyperv-deadman", "--host-worker-mediator", "--egress-datapath":
			return args
		}
	}
	out := make([]string, 0, len(args))
	commandSeen := false
	trailing := false
	requestJSON := false
	skipNextAsValue := false
	valueFlags := workspaceValueFlags()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			return out
		}
		if trailing && commandSeen {
			if skipNextAsValue {
				// Value of a preceding known workspace value flag (e.g.
				// "alpine" in "--image alpine"); keep it verbatim, it is not
				// the guest/payload positional.
				out = append(out, a)
				skipNextAsValue = false
				continue
			}
			if !strings.HasPrefix(a, "-") {
				// First true positional after the command word: guest/payload
				// territory begins here. Nothing after this point is touched.
				out = append(out, args[i:]...)
				return out
			}
		}
		switch a {
		case "--mode":
			if i+1 < len(args) && isRecognizedOutputModeValue(args[i+1]) {
				globalOutputMode = normalizeOutputMode(args[i+1])
				i++
			} else {
				out = append(out, a)
			}
		case "--json":
			if commandSeen && requestJSON {
				// For the low-level request family (create/start and the
				// lifecycle verbs), a post-command --json is the documented
				// compat alias for --request-json <path> — leave it for the
				// command's own flagset. The global output flag for these
				// commands goes before the command word (the documented
				// `microagent --json <command>` convention).
				out = append(out, a)
			} else {
				outputFormat = "json"
			}
		case "--output":
			if i+1 < len(args) && normalizeOutputFormat(args[i+1]) != "" {
				outputFormat = normalizeOutputFormat(args[i+1])
				i++
			} else {
				out = append(out, a)
			}
		default:
			switch {
			case strings.HasPrefix(a, "--mode=") && isRecognizedOutputModeValue(strings.TrimPrefix(a, "--mode=")):
				globalOutputMode = normalizeOutputMode(strings.TrimPrefix(a, "--mode="))
			case strings.HasPrefix(a, "--output=") && normalizeOutputFormat(strings.TrimPrefix(a, "--output=")) != "":
				outputFormat = normalizeOutputFormat(strings.TrimPrefix(a, "--output="))
			default:
				out = append(out, a)
				if !commandSeen && !strings.HasPrefix(a, "-") {
					commandSeen = true
					if spec, ok := lookupCommand(a); ok {
						trailing = spec.TrailingArgs
						requestJSON = spec.RequestJSON
					}
				} else if trailing && commandSeen && strings.HasPrefix(a, "-") {
					// Not a global flag (handled above) but a dash-prefixed
					// token in the trailing region. If it's a known
					// workspace value flag (and not one of the ambiguous
					// names that is also a bool flag, e.g. -json's create/
					// start compat alias), its value token must be skipped
					// too so it isn't mistaken for the guest/payload
					// positional. Unknown dash-prefixed flags are kept as-is
					// without skipping a value — conservative, since their
					// value (if any) will simply hit the positional stop.
					norm := a
					if strings.HasPrefix(norm, "--") {
						norm = "-" + strings.TrimPrefix(norm, "--")
					}
					flagName := norm
					hasInlineValue := false
					if name, _, ok := strings.Cut(norm, "="); ok {
						flagName = name
						hasInlineValue = true
					}
					if !hasInlineValue && valueFlags[flagName] && !isBoolReorderFlag(flagName) {
						skipNextAsValue = true
					}
				}
			}
		}
	}
	return out
}

func normalizeOutputFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json"
	case "text", "human":
		return "text"
	default:
		return ""
	}
}

func writeDoctorResponse(stdout *os.File, resp vmkit.Response) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, resp)
	}
	fmt.Fprintf(stdout, "Backend: %s\n", nonEmpty(resp.Backend, "unknown"))
	fmt.Fprintf(stdout, "Status: %s\n", humanOK(resp.OK))
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
	if host.Backend == vmkit.BackendAppleVF {
		networkReady := host.FrameworkAvailable && host.VirtualizationSupported && host.SupervisorAvailable
		fmt.Fprintf(stdout, "Networking: isolated %s, user %s\n", ready(networkReady), ready(networkReady))
		return
	}
	fmt.Fprintf(stdout, "Networking: isolated %s, user %s\n",
		ready(host.IsolatedNetworkReady),
		ready(host.UserNetworkReady))
	if host.Backend == vmkit.BackendLinuxKVM {
		status := "PASS"
		if !host.EgressTProxyReady {
			status = "WARN"
		}
		fmt.Fprintf(stdout, "Egress TPROXY modules: %s", status)
		if len(host.EgressTProxyMissingModules) > 0 {
			fmt.Fprintf(stdout, " (missing: %s)", strings.Join(host.EgressTProxyMissingModules, ", "))
		}
		fmt.Fprintln(stdout)
		if hint := diagnostics.EgressTProxyRemediation(host); hint != "" {
			fmt.Fprintf(stdout, "  %s\n", hint)
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
	fmt.Fprintf(stdout, "Status: %s\n", humanOK(resp.OK))
	if resp.Backend != "" {
		fmt.Fprintf(stdout, "Backend: %s\n", resp.Backend)
	}
	if resp.Event != nil {
		fmt.Fprintf(stdout, "Workspace: %s\n", resp.Event.Identity.RuntimeID)
		fmt.Fprintf(stdout, "State: %s\n", resp.Event.State)
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
			fmt.Fprintf(stdout, "Verification: %s\n", humanOK(resp.Verification.OK))
		}
		if resp.Readiness != nil {
			mediation := "disabled"
			if resp.Mediation != nil && resp.Mediation.Enabled {
				mediation = humanReady(resp.Readiness.MediationReady.Ready)
			}
			fmt.Fprintf(stdout, "Readiness: guest=%s shell=%s result=%s mediation=%s\n",
				humanReady(resp.Readiness.GuestReady.Ready),
				humanReady(resp.Readiness.ShellReady.Ready),
				humanReady(resp.Readiness.ResultReady.Ready),
				mediation,
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
}

func writeWorkspaceResultWithOptions(stdout *os.File, result workspaceResult, opts workspaceResultOptions) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	if opts.CreatedSummary {
		fmt.Fprintf(stdout, "Created workspace: %s\n", result.Workspace)
	} else {
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

func writeCreateResult(stdout *os.File, result workspaceResult, err error) error {
	return writeWorkspaceResultWithOptions(stdout, result, workspaceResultOptions{
		SuppressSuccessfulResult: err == nil,
		CreatedSummary:           err == nil,
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
		fmt.Fprintf(stdout, "Final state: %s\n", result.FinalState)
	}
	return nil
}

func writeWaitResult(stdout *os.File, result waitResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Workspace: %s\n", result.Workspace)
	fmt.Fprintf(stdout, "State: %s\n", result.State)
	return nil
}

func writeWorkspaceList(stdout *os.File, entries []workspaceListEntry) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"workspaces": entries})
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No workspaces.")
		return nil
	}
	fmt.Fprintf(stdout, "%-24s %-12s %-12s %-12s %-10s %s\n", "NAME", "STATE", "BACKEND", "PROFILE", "NETWORK", "RESTART")
	for _, entry := range entries {
		fmt.Fprintf(stdout, "%-24s %-12s %-12s %-12s %-10s %s\n", entry.Name, entry.State, entry.Backend, entry.Profile, entry.Network, entry.Restart)
	}
	return nil
}

func writeImageList(stdout *os.File, images []imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"images": images})
	}
	if len(images) == 0 {
		fmt.Fprintln(stdout, "No images.")
		return nil
	}
	fmt.Fprintf(stdout, "%-48s %-72s %-16s %-10s %s\n", "IMAGE", "DIGEST", "PLATFORM", "SIZE", "LAST USED")
	for _, image := range images {
		platform := image.Platform.OS + "/" + image.Platform.Architecture
		if image.Platform.Variant != "" {
			platform += "/" + image.Platform.Variant
		}
		fmt.Fprintf(stdout, "%-48s %-72s %-16s %-10d %s\n", image.ImageRef, image.Digest, platform, image.SizeBytes, image.LastUsedAt)
	}
	return nil
}

func writeImageRecord(stdout *os.File, record imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Image: %s\n", record.ImageRef)
	if record.ResolvedRef != "" {
		fmt.Fprintf(stdout, "Resolved: %s\n", record.ResolvedRef)
	}
	if record.Digest != "" {
		fmt.Fprintf(stdout, "Digest: %s\n", record.Digest)
	}
	platform := record.Platform.OS + "/" + record.Platform.Architecture
	if record.Platform.Variant != "" {
		platform += "/" + record.Platform.Variant
	}
	fmt.Fprintf(stdout, "Platform: %s\n", platform)
	if record.OutputPath != "" {
		fmt.Fprintf(stdout, "Rootfs: %s\n", record.OutputPath)
	}
	if record.SizeBytes != 0 {
		fmt.Fprintf(stdout, "Size: %d\n", record.SizeBytes)
	}
	return nil
}

func writeImagePruneResult(stdout *os.File, result imagePruneResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Removed: %d\n", len(result.Removed))
	fmt.Fprintf(stdout, "Deleted: %d\n", len(result.Deleted))
	fmt.Fprintf(stdout, "Kept: %d\n", len(result.Kept))
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
