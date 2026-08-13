package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"gopkg.in/yaml.v3"
)

type SpecApplyOptions struct {
	MemoryExplicit bool
	CPUExplicit    bool
	SizeExplicit   bool
}

func ReadSpec(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, specDecodeError(path, err)
	}
	return spec, nil
}

func ApplySpecFile(opts *Options, path string, apply SpecApplyOptions) error {
	spec, err := ReadSpec(path)
	if err != nil {
		return err
	}
	return ApplySpec(opts, spec, filepath.Dir(path), apply)
}

func ApplySpec(opts *Options, spec Spec, baseDir string, apply SpecApplyOptions) error {
	if opts.CapabilityRiskAcknowledgement == "" {
		opts.CapabilityRiskAcknowledgement = strings.TrimSpace(spec.CapabilityRiskAcknowledgement)
	}
	if strings.TrimSpace(spec.Name) != "" {
		opts.Name = strings.TrimSpace(spec.Name)
	}
	if strings.TrimSpace(spec.ImageRef) != "" {
		opts.ImageRef = strings.TrimSpace(spec.ImageRef)
	}
	if strings.TrimSpace(spec.Profile) != "" {
		opts.Profile = strings.TrimSpace(spec.Profile)
		if err := ApplyProfile(opts, apply.MemoryExplicit, apply.CPUExplicit, apply.SizeExplicit); err != nil {
			return err
		}
	}
	if strings.TrimSpace(spec.Restart) != "" {
		opts.RestartPolicy = NormalizeRestartPolicy(spec.Restart)
	}
	if spec.Resources.MemoryMiB != 0 && !apply.MemoryExplicit {
		opts.MemoryMiB = spec.Resources.MemoryMiB
		opts.SpecMemory = true
	}
	if spec.Resources.CPUCount != 0 && !apply.CPUExplicit {
		opts.CPUCount = spec.Resources.CPUCount
		opts.SpecCPU = true
	}
	if spec.Resources.SizeMiB != 0 && !apply.SizeExplicit {
		opts.SizeMiB = spec.Resources.SizeMiB
		opts.SpecSize = true
	}
	if spec.Resources.HeadroomMiB != 0 && opts.HeadroomMiB == 0 {
		opts.HeadroomMiB = spec.Resources.HeadroomMiB
	}
	if specHasNetwork(spec.Network) {
		opts.Network = NetworkConfigFromSpec(spec.Network)
	}
	if spec.Mediation.Enabled || spec.Mediation.Required || spec.Mediation.Port != 0 || strings.TrimSpace(spec.Mediation.Target) != "" || spec.Mediation.FailClosed {
		mediation := NormalizeMediationConfig(spec.Mediation)
		if err := vmkit.ValidateMediationConfig(mediation); err != nil {
			return err
		}
		opts.Mediation = &mediation
	}
	if spec.Health.Declared() {
		health := NormalizeHealthCheck(spec.Health)
		if err := ValidateHealthCheck(health); err != nil {
			return err
		}
		opts.Health = health
	}
	if strings.TrimSpace(spec.Entrypoint) != "" {
		opts.Entrypoint = spec.Entrypoint
	}
	if strings.TrimSpace(spec.Service) != "" {
		opts.ServiceCommand = strings.TrimSpace(spec.Service)
	}
	if strings.TrimSpace(spec.Shell) != "" {
		opts.ConsoleShell = strings.TrimSpace(spec.Shell)
	}
	if strings.TrimSpace(spec.Hostname) != "" {
		opts.Hostname = strings.TrimSpace(spec.Hostname)
	}
	if strings.TrimSpace(spec.Model) != "" {
		opts.Model = strings.TrimSpace(spec.Model)
	}
	if modelRunnerSpecDeclared(spec.ModelRunner) {
		opts.ModelRunner = spec.ModelRunner
	}
	if modelMediationSpecDeclared(spec.ModelMediation) {
		if strings.TrimSpace(spec.ModelMediation.PolicyFile) != "" && !filepath.IsAbs(spec.ModelMediation.PolicyFile) {
			spec.ModelMediation.PolicyFile = filepath.Join(baseDir, spec.ModelMediation.PolicyFile)
		}
		opts.ModelMediation = spec.ModelMediation
	}
	if len(spec.Setup) != 0 || len(spec.SetupFiles) != 0 {
		setupCommands, err := SetupCommandsFromSpec(spec.Setup, spec.SetupFiles, baseDir)
		if err != nil {
			return err
		}
		opts.SetupCommands = setupCommands
	}
	if spec.Agent.Declared() {
		if err := applyAgentSpec(opts, spec.Agent, baseDir); err != nil {
			return err
		}
	}
	opts.Env = MergeEnv(opts.Env, spec.Env)
	files, err := ValidateFiles(spec.Files, baseDir)
	if err != nil {
		return err
	}
	opts.Files = append(opts.Files, files...)
	disks, err := SpecDisks(spec)
	if err != nil {
		return err
	}
	opts.Disks = append(opts.Disks, disks...)
	outputs, err := ValidateOutputs(spec.Outputs)
	if err != nil {
		return err
	}
	opts.Outputs = append(opts.Outputs, outputs...)
	return nil
}

// applyAgentSpec maps the Agentfile `agent:` block onto Options. It carries the
// three knobs the rest of the Spec cannot express: the one-shot entry command,
// the egress envelope, and cred-swap. It is additive and defers to values
// already on Options (e.g. a CLI flag applied before the spec wins): entry only
// fills an empty ExecCommand, allow/cred-swap union with what is there.
func applyAgentSpec(opts *Options, agent AgentSpec, baseDir string) error {
	if entry := strings.TrimSpace(agent.Entry); entry != "" && strings.TrimSpace(opts.ExecCommand) == "" {
		opts.ExecCommand = entry
	}
	if strings.TrimSpace(agent.Egress) != "" {
		mode, err := vmkit.ValidateEgressMode(agent.Egress)
		if err != nil {
			return fmt.Errorf("agent egress: %w", err)
		}
		opts.EgressMode = mode
	}
	if len(agent.Allow) != 0 {
		opts.EgressAllow = egress.DedupeHosts(append(append([]string(nil), opts.EgressAllow...), agent.Allow...))
	}
	if agent.LockAllowlist {
		opts.EgressAllowlistLocked = true
	}
	for _, raw := range agent.CredSwap {
		provider, ok, err := ParseCredSwapProvider(raw)
		if err != nil {
			return err
		}
		if ok {
			opts.CredSwapProviders = append(opts.CredSwapProviders, provider)
		}
	}
	if agent.Broker != nil && len(agent.Brokers) > 0 {
		return fmt.Errorf("agent: set either broker or brokers, not both")
	}
	// A broker supplied earlier (e.g. a --broker-* CLI flag) wins; the agent
	// block only fills an unset one, consistent with the rest of ApplySpec.
	if len(agent.Brokers) > 0 && len(opts.Brokers) == 0 && opts.Broker == nil {
		brokers := make([]*vmkit.BrokerConfig, 0, len(agent.Brokers))
		for i, b := range agent.Brokers {
			if b.Grant != "" && !filepath.IsAbs(b.Grant) {
				b.Grant = filepath.Join(baseDir, b.Grant)
			}
			cfg, err := ParseBrokerConfig(b.Upstream, b.Secret, b.Env, b.Proxy, b.Capture, b.CA, BrokerSecurityOptions{Assurance: b.Assurance, GrantPath: b.Grant})
			if err != nil {
				return fmt.Errorf("agent.brokers[%d]: %w", i, err)
			}
			if cfg == nil {
				return fmt.Errorf("agent.brokers[%d]: declares nothing (upstream and secret are required)", i)
			}
			brokers = append(brokers, cfg)
		}
		if err := validateBrokerEndpointSet(brokers); err != nil {
			return err
		}
		opts.Brokers = brokers
	} else if agent.Broker != nil && opts.Broker == nil && len(opts.Brokers) == 0 {
		b := *agent.Broker
		if b.Grant != "" && !filepath.IsAbs(b.Grant) {
			b.Grant = filepath.Join(baseDir, b.Grant)
		}
		broker, err := ParseBrokerConfig(b.Upstream, b.Secret, b.Env, b.Proxy, b.Capture, b.CA, BrokerSecurityOptions{Assurance: b.Assurance, GrantPath: b.Grant})
		if err != nil {
			return err
		}
		opts.Broker = broker
	}
	return nil
}

func SetupCommandsFromSpec(steps SetupSteps, setupFiles []string, baseDir string) ([]string, error) {
	commands := make([]string, 0, len(steps)+len(setupFiles))
	for _, step := range steps {
		run := strings.TrimSpace(step.Run)
		file := strings.TrimSpace(step.File)
		if run != "" && file != "" {
			return nil, fmt.Errorf("setup entry cannot use both run and file")
		}
		if run != "" {
			commands = append(commands, run)
			continue
		}
		if file != "" {
			command, err := SetupCommandFromFile(file, baseDir)
			if err != nil {
				return nil, err
			}
			commands = append(commands, command)
		}
	}
	fileCommands, err := SetupCommandsFromFiles(setupFiles, baseDir)
	if err != nil {
		return nil, err
	}
	commands = append(commands, fileCommands...)
	return commands, nil
}

func SetupCommandsFromFiles(files []string, baseDir string) ([]string, error) {
	commands := make([]string, 0, len(files))
	for _, file := range files {
		command, err := SetupCommandFromFile(file, baseDir)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func SetupCommandFromFile(pathValue, baseDir string) (string, error) {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return "", fmt.Errorf("setup file path is required")
	}
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(baseDir, pathValue)
	}
	info, err := os.Stat(pathValue)
	if err != nil {
		return "", fmt.Errorf("setup file %q: %w", pathValue, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("setup file must be a regular file: %s", pathValue)
	}
	data, err := os.ReadFile(pathValue)
	if err != nil {
		return "", fmt.Errorf("read setup file %q: %w", pathValue, err)
	}
	command := strings.TrimSpace(string(data))
	if command == "" {
		return "", fmt.Errorf("setup file is empty: %s", pathValue)
	}
	return command, nil
}

func SpecDisks(spec Spec) ([]Disk, error) {
	disks := make([]Disk, 0, len(spec.Disks)+len(spec.Bundles))
	for _, disk := range spec.Disks {
		disk.Bundle = false
		if !vmkit.SafeIdentifier(disk.Name) {
			return nil, fmt.Errorf("invalid disk name: %s", disk.Name)
		}
		if err := ValidateDisk(disk); err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	for _, disk := range spec.Bundles {
		disk.Bundle = true
		if disk.SourcePath == "" {
			disk.SourcePath = disk.Path
		}
		if !vmkit.SafeIdentifier(disk.Name) {
			return nil, fmt.Errorf("invalid disk name: %s", disk.Name)
		}
		if err := ValidateDisk(disk); err != nil {
			return nil, err
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func ValidateOutputs(outputs []Output) ([]Output, error) {
	seen := map[string]bool{}
	validated := make([]Output, 0, len(outputs))
	for _, output := range outputs {
		output.Name = strings.TrimSpace(output.Name)
		output.Path = strings.TrimSpace(output.Path)
		if err := ValidateOutput(output); err != nil {
			return nil, err
		}
		if seen[output.Name] {
			return nil, fmt.Errorf("duplicate output artifact %q", output.Name)
		}
		seen[output.Name] = true
		validated = append(validated, output)
	}
	return validated, nil
}

func ValidateFiles(files []File, baseDir string) ([]File, error) {
	seen := map[string]bool{}
	validated := make([]File, 0, len(files))
	for _, file := range files {
		file.SourcePath = strings.TrimSpace(file.SourcePath)
		file.Path = strings.TrimSpace(file.Path)
		file.Mode = strings.TrimSpace(file.Mode)
		if file.SourcePath == "" {
			return nil, fmt.Errorf("file src is required")
		}
		if !filepath.IsAbs(file.SourcePath) {
			file.SourcePath = filepath.Join(baseDir, file.SourcePath)
		}
		info, err := os.Stat(file.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("file src %q: %w", file.SourcePath, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("file src must be a regular file: %s", file.SourcePath)
		}
		if file.Path == "" {
			return nil, fmt.Errorf("file dst is required for %s", file.SourcePath)
		}
		if !path.IsAbs(file.Path) {
			return nil, fmt.Errorf("file dst must be absolute: %s", file.Path)
		}
		if strings.ContainsRune(file.Path, 0) {
			return nil, fmt.Errorf("file dst contains NUL")
		}
		cleanPath := path.Clean(file.Path)
		if cleanPath == "/" {
			return nil, fmt.Errorf("file dst must name a file: %s", file.Path)
		}
		if seen[cleanPath] {
			return nil, fmt.Errorf("duplicate file dst %q", cleanPath)
		}
		seen[cleanPath] = true
		file.Path = cleanPath
		if file.Mode != "" {
			if _, err := strconv.ParseUint(file.Mode, 8, 32); err != nil {
				return nil, fmt.Errorf("file %s mode: %w", file.Path, err)
			}
		}
		validated = append(validated, file)
	}
	return validated, nil
}

func MergeEnv(base, overrides map[string]string) map[string]string {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overrides {
		out[key] = value
	}
	return out
}

func NormalizeMediationConfig(mediation vmkit.MediationConfig) vmkit.MediationConfig {
	mediation.Target = strings.TrimSpace(mediation.Target)
	if mediation.Port != 0 || mediation.Target != "" || mediation.Required || mediation.FailClosed {
		mediation.Enabled = true
	}
	if mediation.Required {
		mediation.FailClosed = true
	}
	return mediation
}

func specDecodeError(path string, err error) error {
	var typeErr *yaml.TypeError
	if !errors.As(err, &typeErr) {
		return fmt.Errorf("invalid workspace spec %s: %w", path, err)
	}
	messages := make([]string, 0, len(typeErr.Errors))
	for _, msg := range typeErr.Errors {
		messages = append(messages, humanSpecFieldError(msg))
	}
	return fmt.Errorf("invalid workspace spec %s: %s", path, strings.Join(messages, "; "))
}

func humanSpecFieldError(msg string) string {
	line := ""
	rest := msg
	if strings.HasPrefix(rest, "line ") {
		if index := strings.Index(rest, ": "); index >= 0 {
			line = rest[:index]
			rest = rest[index+2:]
		}
	}
	const prefix = "field "
	const separator = " not found in type "
	if strings.HasPrefix(rest, prefix) && strings.Contains(rest, separator) {
		parts := strings.SplitN(strings.TrimPrefix(rest, prefix), separator, 2)
		field := parts[0]
		typeName := parts[1]
		var text string
		switch typeName {
		case "workspace.Spec":
			text = fmt.Sprintf("unknown top-level field %q", field)
		case "workspace.Resources":
			if field == "network" {
				text = `unknown field "network" under resources; move network to the top level, aligned with resources`
			} else {
				text = fmt.Sprintf("unknown field %q under resources", field)
			}
		default:
			text = fmt.Sprintf("unknown field %q under %s", field, strings.TrimPrefix(typeName, "workspace."))
		}
		if line != "" {
			return line + ": " + text
		}
		return text
	}
	return msg
}

func specHasNetwork(network NetworkSpec) bool {
	return network.Mode != "" ||
		len(network.PortForwards) != 0 ||
		len(network.DNS) != 0 ||
		len(network.Routes) != 0 ||
		network.IP != "" ||
		network.Subnet != "" ||
		network.Gateway != "" ||
		network.IPv6 != "" ||
		network.IPv6Subnet != "" ||
		network.IPv6Gateway != ""
}

func modelRunnerSpecDeclared(spec ModelRunnerSpec) bool {
	return strings.TrimSpace(spec.Backend) != "" ||
		strings.TrimSpace(spec.GPU) != "" ||
		strings.TrimSpace(spec.BackendModel) != "" ||
		strings.TrimSpace(spec.ServedModel) != "" ||
		len(spec.Command) != 0 ||
		strings.TrimSpace(spec.Name) != "" ||
		strings.TrimSpace(spec.HealthPath) != "" ||
		len(spec.Args) != 0
}

func modelMediationSpecDeclared(spec ModelMediationSpec) bool {
	return strings.TrimSpace(spec.Mode) != "" ||
		strings.TrimSpace(spec.PolicyURL) != "" ||
		strings.TrimSpace(spec.PolicyFile) != "" ||
		strings.TrimSpace(spec.PolicyTimeout) != ""
}
