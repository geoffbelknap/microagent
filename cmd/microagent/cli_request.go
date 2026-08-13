package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func requestFromFlagsOrJSON(jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config, disks []string, vsocks []string, networkMode string, publishes []string) (vmkit.Request, error) {
	if jsonPath != "" {
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("--request-json does not accept positional request paths")
		}
		req, err := readRequest(jsonPath)
		if err != nil {
			return vmkit.Request{}, err
		}
		if req.Identity != nil && req.Config != nil {
			if err := vmkit.ValidateBackendVsockListeners(req.Identity.Backend, req.Config.VsockListeners); err != nil {
				return vmkit.Request{}, err
			}
		}
		return req, nil
	}
	if len(args) != 0 {
		return vmkit.Request{}, fmt.Errorf("unexpected argument: %s", args[0])
	}
	if identity.RequestID == "" {
		identity.RequestID = newRequestID()
	}
	listeners, err := parseVsockMappings(vsocks)
	if err != nil {
		return vmkit.Request{}, err
	}
	if err := vmkit.ValidateBackendVsockListeners(identity.Backend, listeners); err != nil {
		return vmkit.Request{}, err
	}
	parsedDisks, err := parseWorkspaceDisks(disks, false)
	if err != nil {
		return vmkit.Request{}, err
	}
	portForwards, err := parsePortForwardMappings(publishes)
	if err != nil {
		return vmkit.Request{}, err
	}
	for _, disk := range parsedDisks {
		config.Disks = append(config.Disks, vmkit.Disk{
			Name:       disk.Name,
			Path:       disk.Path,
			Mountpoint: disk.Mountpoint,
			Mode:       disk.Mode,
		})
	}
	config.VsockListeners = listeners
	network := normalizeNetworkConfig(vmkit.NetworkConfig{Mode: networkMode, PortForwards: portForwards})
	if err := vmkit.ValidateNetworkConfig(network); err != nil {
		return vmkit.Request{}, err
	}
	config.Network = &network
	return vmkit.Request{Identity: &identity, Config: &config}, nil
}

func stateRequestFromFlagsOrJSON(command, jsonPath string, args []string, identity vmkit.Identity, config vmkit.Config) (vmkit.Request, error) {
	if jsonPath != "" {
		if len(args) != 0 {
			return vmkit.Request{}, fmt.Errorf("--request-json does not accept positional request paths")
		}
		return readRequest(jsonPath)
	}
	if len(args) > 1 {
		return vmkit.Request{}, fmt.Errorf("usage: microagent %s [id] --state-dir <dir>", command)
	}
	if len(args) == 1 && identity.RuntimeID == "" {
		identity.RuntimeID = args[0]
	}
	if identity.RequestID == "" {
		identity.RequestID = newRequestID()
	}
	return vmkit.Request{Identity: &identity, Config: &config}, nil
}

func workspaceValueFlags() map[string]bool {
	return map[string]bool{
		"-supervisor":                true,
		"-json":                      true,
		"-id":                        true,
		"-name":                      true,
		"-image":                     true,
		"-exec":                      true,
		"-setup-file":                true,
		"-service-command":           true,
		"-entrypoint":                true,
		"-shell":                     true,
		"-hostname":                  true,
		"-file":                      true,
		"-env":                       true,
		"-setup":                     true,
		"-request-id":                true,
		"-reason":                    true,
		"-role":                      true,
		"-backend":                   true,
		"-kernel":                    true,
		"-rootfs":                    true,
		"-disk":                      true,
		"-bundle":                    true,
		"-volume":                    true,
		"-v":                         true,
		"-output":                    true,
		"-debugfs":                   true,
		"-profile":                   true,
		"-restart":                   true,
		"-network":                   true,
		"-mediation":                 true,
		"-publish":                   true,
		"-p":                         true,
		"-state-dir":                 true,
		"-tag":                       true,
		"-provider":                  true,
		"-dir":                       true,
		"-subnet":                    true,
		"-from-snapshot":             true,
		"-url":                       true,
		"-from":                      true,
		"-sha256":                    true,
		"-out":                       true,
		"-path":                      true,
		"-memory":                    true,
		"-cpus":                      true,
		"-vsock":                     true,
		"-mke2fs":                    true,
		"-guest-init":                true,
		"-arch":                      true,
		"-size-mib":                  true,
		"-headroom-mib":              true,
		"-timeout":                   true,
		"-wait-timeout":              true,
		"-ttl":                       true,
		"-ready-timeout":             true,
		"-duration":                  true,
		"-interval":                  true,
		"-max-restarts":              true,
		"-result-port":               true,
		"-send":                      true,
		"-e":                         true,
		"-model":                     true,
		"-model-token":               true,
		"-model-runner":              true,
		"-model-gpu":                 true,
		"-model-runner-model":        true,
		"-model-runner-served-model": true,
		"-model-runner-command":      true,
		"-model-runner-name":         true,
		"-model-runner-health-path":  true,
		"-model-runner-arg":          true,
		"-model-runner-env":          true,
		"-model-mediation":           true,
		"-model-policy-url":          true,
		"-model-policy-file":         true,
		"-model-policy-timeout":      true,
		"-runner":                    true,
		"-runner-gpu":                true,
		"-runner-model":              true,
		"-runner-served-model":       true,
		"-runner-command":            true,
		"-runner-name":               true,
		"-runner-health-path":        true,
		"-runner-arg":                true,
		"-runner-env":                true,
		"-method":                    true,
		"-workspace-id":              true,
		"-capability":                true,
		"-worker-id":                 true,
		"-request-bytes":             true,
		"-text-bytes":                true,
		"-messages":                  true,
		"-max-tokens":                true,
		"-stream":                    true,
		"-tool":                      true,
		"-expect":                    true,
		"-secret":                    true,
		"-secrets-env-file":          true,
		"-secret-on-demand":          true,
		"-egress":                    true,
		"-egress-allow":              true,
		"-egress-passthrough":        true,
		"-egress-policy":             true,
		"-egress-swap-config":        true,
		"-egress-max-total-bytes":    true,
		"-egress-max-bps":            true,
		"-egress-max-conns":          true,
		"-cred-swap":                 true,
		"-broker-upstream":           true,
		"-broker-secret":             true,
		"-broker-env":                true,
		"-broker-ca":                 true,
		"-broker-endpoint":           true,
		"-broker-assurance":          true,
		"-broker-grant":              true,
	}
}

func reorderFlagArgs(args []string) []string {
	valueFlags := workspaceValueFlags()
	return reorderArgs(args, func(name string) bool { return valueFlags[name] }, isBoolReorderFlag)
}

// reorderFlagArgsForRunDispatch is reorderFlagArgs for run/dispatch: it hoists
// microagent flags (which may sit before or after the IMAGE) but leaves the
// guest command and its args untouched, so guest flags (-e, -f, -p, ...) that
// collide with microagent flag names are never lifted out of the guest command.
func reorderFlagArgsForRunDispatch(args []string) []string {
	valueFlags := workspaceValueFlags()
	return reorderArgsStopAtGuestCommand(args, func(name string) bool { return valueFlags[name] }, isBoolReorderFlag)
}

// reorderArgs hoists recognized flags ahead of positionals so a FlagSet stops at the
// first positional rather than at an interleaved flag. isValueFlag/isBoolFlag report
// which flag names the CALLER's FlagSet knows — a command must pass only its OWN
// flags, never a global set, or it will lift a guest/positional command's flags (e.g.
// `run <image> id -u`) out of the tail and misparse them.
func reorderArgs(args []string, isValueFlag, isBoolFlag func(string) bool) []string {
	var flags []string
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		if strings.HasPrefix(arg, "--") {
			arg = "-" + strings.TrimPrefix(arg, "--")
		}
		flagName := arg
		if name, _, ok := strings.Cut(arg, "="); ok {
			flagName = name
		}
		if strings.Contains(arg, "=") {
			positional = append(positional, args[i])
			continue
		}
		if !isValueFlag(flagName) && !isBoolFlag(flagName) {
			positional = append(positional, args[i])
			continue
		}
		flags = append(flags, arg)
		if isValueFlag(flagName) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

// reorderArgsStopAtGuestCommand is reorderArgs for run/dispatch, whose grammar
// is `[FLAGS] IMAGE [FLAGS] GUEST_COMMAND [GUEST_ARGS...]`. microagent flags may
// appear before AND after the IMAGE (Docker-style), but the GUEST_COMMAND begins
// a verbatim tail: from there every token — including flags like -e/-f/-p that
// collide with microagent's own flag names — must reach the guest untouched.
//
// The GUEST_COMMAND is the first positional AFTER the IMAGE. The IMAGE is either
// supplied via --image (a value flag) or is the first positional; so the guest
// command is the 1st positional when --image is present, else the 2nd. Only the
// args up to (not including) the guest command are hoisted; the rest is verbatim.
// Value flags consume their argument so a value is never mistaken for a positional.
func reorderArgsStopAtGuestCommand(args []string, isValueFlag, isBoolFlag func(string) bool) []string {
	imageFromFlag := false
	positionals := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positionals++
			guestCommand := (imageFromFlag && positionals == 1) || (!imageFromFlag && positionals == 2)
			if guestCommand {
				return append(reorderArgs(args[:i], isValueFlag, isBoolFlag), args[i:]...)
			}
			continue
		}
		norm := arg
		if strings.HasPrefix(norm, "--") {
			norm = "-" + strings.TrimPrefix(norm, "--")
		}
		flagName := norm
		if name, _, ok := strings.Cut(norm, "="); ok {
			flagName = name
		}
		if flagName == "-image" {
			imageFromFlag = true
		}
		// A `-flag value` form (no `=`) consumes the next token; skip it so a
		// value is not mistaken for the IMAGE or the guest command.
		if !strings.Contains(norm, "=") && isValueFlag(flagName) && i+1 < len(args) {
			i++
		}
	}
	// No guest command present (image only, or pure flags): reorder as usual.
	return reorderArgs(args, isValueFlag, isBoolFlag)
}

func isBoolReorderFlag(name string) bool {
	switch name {
	case "-json", "-text", "-human", "-keep", "-rm", "-dry-run", "-image-command", "-mediation-optional", "-secrets-audit", "-egress-lock-allowlist", "-broker-proxy", "-broker-capture", "-forensic", "-no-capture", "-unsupported", "-purge", "-yes", "-y", "-force", "-f", "-follow", "-images", "-install", "-uninstall", "-push", "-allow-registry-shadow", "-wait":
		return true
	default:
		return false
	}
}

func readRequest(path string) (vmkit.Request, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return vmkit.Request{}, err
	}
	var req vmkit.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return vmkit.Request{}, err
	}
	return req, nil
}

func mapCLICommand(command string) string {
	if command == "status" {
		return "inspect"
	}
	return command
}
