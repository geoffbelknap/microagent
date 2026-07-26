package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/broker"
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
	"gopkg.in/yaml.v3"
)

func BuildRootfs(ctx context.Context, opts Options) (Result, error) {
	rootfsPath := WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend)

	// Fast path: for a plain workspace, clone a previously pulled/tagged baseline
	// rootfs instead of pulling and rebuilding. The resolver is injected by the
	// CLI (which owns the image cache) so pkg/workspace does not depend on
	// pkg/imagecache; it returns ok=false when there is no reusable baseline.
	if opts.RootfsBaseline != nil && canReuseRootfsBaseline(opts) {
		if baseline, prov, ok := opts.RootfsBaseline(rootfsPath); ok {
			if err := CopyFile(baseline, rootfsPath, 0o644); err != nil {
				return Result{}, err
			}
			return buildRootfsResult(opts, rootfsPath, prov), nil
		}
	}

	req, err := rootfsRequest(opts, rootfsPath)
	if err != nil {
		return Result{}, err
	}
	provenance, err := rootfs.NewBuilder().Build(ctx, req)
	if builtMiB := provenance.SizeBytes / (1024 * 1024); builtMiB > opts.SizeMiB {
		opts.SizeMiB = builtMiB
	}
	return buildRootfsResult(opts, rootfsPath, provenance), err
}

func buildRootfsResult(opts Options, rootfsPath string, image rootfs.Provenance) Result {
	return Result{
		Workspace:    opts.Name,
		StateDir:     opts.StateDir,
		Profile:      opts.Profile,
		Restart:      opts.RestartPolicy,
		Resources:    ResourcesFromOptions(opts),
		Network:      NetworkSpecFromConfig(opts.Network),
		Service:      strings.TrimSpace(opts.ServiceCommand),
		ConsoleShell: strings.TrimSpace(opts.ConsoleShell),
		Hostname:     strings.TrimSpace(opts.Hostname),
		RootfsPath:   rootfsPath,
		KernelPath:   opts.KernelPath,
		Artifacts:    ArtifactsFromOptions(opts),
		Image:        image,
	}
}

// canReuseRootfsBaseline reports whether the workspace's rootfs would be identical
// to a plain pulled/tagged image baseline — i.e. nothing bakes workspace-specific
// content into it. Only then is cloning a baseline safe instead of building.
func canReuseRootfsBaseline(opts Options) bool {
	return opts.PrepareForStart &&
		!HasGuestCommand(opts) &&
		strings.TrimSpace(opts.ConsoleShell) == "" &&
		strings.TrimSpace(opts.Hostname) == "" &&
		len(opts.Files) == 0 &&
		len(opts.Disks) == 0 &&
		len(opts.Env) == 0 &&
		len(opts.Network.PortForwards) == 0
}

// rootfsRequest composes the rootfs build request, baking the broker guest
// env (vsock bridge, proxy, base URLs) into the image env when a broker is
// configured. Fail-closed: an invalid broker config fails the build rather
// than producing a workspace whose egress silently bypasses the broker.
func rootfsRequest(opts Options, rootfsPath string) (rootfs.BuildRequest, error) {
	req := buildRootfsRequest(opts, rootfsPath)
	brokers, err := normalizeEffectiveBrokers(opts)
	if err != nil {
		return rootfs.BuildRequest{}, err
	}
	for _, bc := range brokers {
		guest := broker.GuestConfig{
			GuestListen: bc.GuestListen,
			VsockPort:   bc.VsockPort,
			Proxy:       bc.Proxy,
			BaseURL:     bc.BaseURLEnv,
		}
		env, err := guest.MergeGuestEnvMap(req.Env)
		if err != nil {
			return rootfs.BuildRequest{}, fmt.Errorf("broker guest env: %w", err)
		}
		req.Env = env
	}
	return req, nil
}

// localImageLayoutPath returns the committed-OCI layout path for stateDir.
// This mirrors commit.LayoutPath without importing pkg/commit: pkg/commit
// already imports pkg/workspace, so importing it back here would create an
// import cycle.
func localImageLayoutPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "oci")
}

func buildRootfsRequest(opts Options, rootfsPath string) rootfs.BuildRequest {
	command, resultPort := BuildCommandAndPort(opts)
	mode := ""
	if opts.PrepareForStart && opts.UseImageCommand {
		mode = "service"
	} else if opts.PrepareForStart && strings.TrimSpace(opts.ServiceCommand) != "" && !HasSetupCommand(opts) && strings.TrimSpace(opts.ExecCommand) == "" {
		mode = "managed-service"
	}
	finalCommand, finalMode, resetFinal := FinalCommandAndMode(opts)
	return rootfs.BuildRequest{
		ImageRef:         opts.ImageRef,
		Platform:         rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:       rootfsPath,
		Format:           WorkspaceRootfsFormat(opts.Backend),
		InitPath:         rootfs.DefaultInitPath,
		Command:          command,
		Mode:             mode,
		ConsoleShell:     opts.ConsoleShell,
		Hostname:         opts.Hostname,
		ShellPort:        ShellPort(opts),
		ExecPort:         ExecPort(opts),
		InitBinaryPath:   opts.GuestInitPath,
		ResultPort:       resultPort,
		NoImageCommand:   opts.PrepareForStart && !HasGuestCommand(opts) && !opts.UseImageCommand,
		StateDir:         filepath.Join(opts.StateDir, "build"),
		LocalImageLayout: localImageLayoutPath(opts.StateDir),
		Mke2fsPath:       opts.Mke2fsPath,
		SizeMiB:          opts.SizeMiB,
		AutoSize:         !opts.SizeExplicit && !opts.SpecSize,
		Env:              opts.Env,
		Files:            RootfsFiles(opts.Files),
		Mounts:           MountsForBackend(opts.Backend, opts.Disks),
		HostForwards:     RootfsPortForwards(opts.Network.PortForwards),
		AllowMutable:     true,
		Progress:         opts.Progress,
		ResetFinalConfig: resetFinal,
		FinalCommand:     finalCommand,
		FinalMode:        finalMode,
	}
}

func WorkspaceRootfsFormat(backend string) string {
	return rootfs.FormatExt4
}

func WorkspaceDiskFormat(backend string) string {
	return WorkspaceRootfsFormat(backend)
}

func WorkspaceRootfsFilename(backend string) string {
	return "rootfs.ext4"
}

func WorkspaceDiskFilename(backend, name string) string {
	return name + ".ext4"
}

func WorkspaceDiskPath(stateDir, workspaceName, backend, diskName string) string {
	return filepath.Join(stateDir, "workspaces", workspaceName, "disks", WorkspaceDiskFilename(backend, diskName))
}

func WorkspaceRootfsPath(stateDir, name, backend string) string {
	return filepath.Join(stateDir, "workspaces", name, WorkspaceRootfsFilename(backend))
}

func CandidateWorkspaceRootfsPaths(stateDir, name, backend string) []string {
	primary := WorkspaceRootfsPath(stateDir, name, backend)
	secondary := WorkspaceRootfsPath(stateDir, name, "")
	if primary == secondary {
		return []string{primary}
	}
	return []string{primary, secondary}
}

// volumeHolderActive reports whether a workspace still counts as holding a
// named volume — i.e. it is in a state where the VM could be using the disk.
// A stopped, halted, failed, or absent workspace is reclaimable.
func volumeHolderActive(stateDir string) func(string) bool {
	return func(name string) bool {
		event, err := ReadEvent(Options{StateDir: stateDir, Name: name})
		if err != nil {
			return false
		}
		switch event.State {
		case vmkit.StateStarting, vmkit.StateRunning, vmkit.StatePaused, vmkit.StateQuarantined:
			return true
		default:
			return false
		}
	}
}

func PrepareDisks(ctx context.Context, opts Options) ([]Disk, error) {
	if len(opts.Disks) == 0 {
		return nil, nil
	}
	disks := make([]Disk, 0, len(opts.Disks))
	seenNames := map[string]bool{}
	seenMountpoints := map[string]bool{}
	for _, disk := range opts.Disks {
		if disk.ManagedVolume {
			path, err := volume.Path(opts.StateDir, opts.Backend, disk.Name)
			if err != nil {
				return nil, err
			}
			if _, err := volume.Attach(opts.StateDir, disk.Name, opts.Name, volumeHolderActive(opts.StateDir)); err != nil {
				return nil, err
			}
			disk.SourcePath = path
			disk.Path = path
			disk.Bundle = false
			disk.ManagedVolume = false
		}
		if err := ValidateDisk(disk); err != nil {
			return nil, err
		}
		if seenNames[disk.Name] {
			return nil, fmt.Errorf("duplicate disk name %q", disk.Name)
		}
		seenNames[disk.Name] = true
		if seenMountpoints[disk.Mountpoint] {
			return nil, fmt.Errorf("duplicate disk mountpoint %q", disk.Mountpoint)
		}
		seenMountpoints[disk.Mountpoint] = true
		if disk.Bundle {
			outputPath := WorkspaceDiskPath(opts.StateDir, opts.Name, opts.Backend, disk.Name)
			_, err := rootfs.NewBuilder().BuildBundle(ctx, rootfs.BundleRequest{
				SourcePath: disk.SourcePath,
				OutputPath: outputPath,
				Format:     WorkspaceDiskFormat(opts.Backend),
				StateDir:   filepath.Join(opts.StateDir, "build"),
				Mke2fsPath: opts.Mke2fsPath,
				SizeMiB:    64,
				AutoSize:   true,
			})
			if err != nil {
				return nil, err
			}
			disk.Path = outputPath
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

// materializeCredSwapConfig resolves opts.CredSwapProviders into a generated
// credential-swap config file and wires it into the egress fields. It is the
// library-side realization of the `--cred-swap PROVIDER` surface: for each
// provider it (1) unions the provider's egress host(s) into EgressAllow so the
// mediator permits the connection and (2) builds a static swap entry. The
// entries are merged with any operator-supplied EgressSwapConfigPath (union by
// name; a name collision is an error so nothing is silently overwritten),
// marshaled to YAML, and written to a durable per-workspace path
// (<StateDir>/workspaces/<name>/cred-swap.yaml) which becomes the new
// EgressSwapConfigPath. The path must be durable, not process-tied: it is
// persisted in the manifest and snapshot manifest and re-read by the mediator
// on restart/restore.
//
// Only references (env:/file:/vault:) are written — never the secret value. The
// mediator resolves the reference host-side at request time, so the guest never
// holds the key. This is a no-op when no providers are declared.
func materializeCredSwapConfig(opts *Options) error {
	if len(opts.CredSwapProviders) == 0 {
		return nil
	}
	// cred-swap is performed by the egress mediator (host-side MITM injection),
	// which only runs in mitm mode. With egress off there is no mediator to
	// inject the key, so the swap would silently do nothing — fail loud. This is
	// the library backstop for direct Go-API callers; the CLI catches it earlier.
	if vmkit.ResolveEgressModeDefault(opts.EgressMode) == vmkit.EgressModeOff {
		return fmt.Errorf("cred-swap: credential swap requires egress mitm, not off")
	}
	cfg := egress.SwapConfigFile{Swaps: map[string]egress.SwapEntry{}}
	// Merge an operator-supplied swap config first so generated provider entries
	// are added on top of it (collision below catches an overlapping name).
	if existing := strings.TrimSpace(opts.EgressSwapConfigPath); existing != "" {
		data, err := os.ReadFile(existing)
		if err != nil {
			return fmt.Errorf("cred-swap: read --egress-swap-config %q: %w", existing, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("cred-swap: parse --egress-swap-config %q: %w", existing, err)
		}
		if cfg.Swaps == nil {
			cfg.Swaps = map[string]egress.SwapEntry{}
		}
	}
	var hosts []string
	for _, p := range opts.CredSwapProviders {
		entry, entryHosts, err := egress.ProviderSwapEntry(p.Provider, p.Ref)
		if err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(p.Provider))
		if _, exists := cfg.Swaps[name]; exists {
			return fmt.Errorf("cred-swap: swap entry %q already defined (collides with an --egress-swap-config entry or a repeated --cred-swap)", name)
		}
		cfg.Swaps[name] = entry
		hosts = append(hosts, entryHosts...)
	}
	// The guest must be allowed to reach the provider host for the injected
	// credential to matter; union into the allowlist (dedupe with what's already
	// there) before the egress policy is built.
	opts.EgressAllow = egress.DedupeHosts(append(append([]string(nil), opts.EgressAllow...), hosts...))

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("cred-swap: marshal config: %w", err)
	}
	workspaceDir := filepath.Join(opts.StateDir, "workspaces", opts.Name)
	if err := os.MkdirAll(workspaceDir, 0o700); err != nil {
		return err
	}
	outPath := filepath.Join(workspaceDir, "cred-swap.yaml")
	if err := os.WriteFile(outPath, data, 0o600); err != nil {
		return fmt.Errorf("cred-swap: write %q: %w", outPath, err)
	}
	opts.EgressSwapConfigPath = outPath
	return nil
}
