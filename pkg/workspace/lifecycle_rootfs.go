package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/internal/egress"
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
	if opts.RootfsBaseline != nil && CanReuseRootfsBaseline(opts) {
		// BaselineSatisfiesSize guards profile-implied sizes the gate cannot
		// see: a baseline smaller than the workspace's effective size must
		// fall through to a real build, not silently hand over a small disk.
		if baseline, prov, ok := opts.RootfsBaseline(rootfsPath); ok && BaselineSatisfiesSize(prov, opts) {
			if err := CopyFile(baseline, rootfsPath, 0o644); err != nil {
				return Result{}, err
			}
			// The manifest must record the disk the workspace actually has —
			// the baseline's size — mirroring what the build branch does when
			// auto-sizing grows the disk.
			if clonedMiB := prov.SizeBytes / (1024 * 1024); clonedMiB > 0 {
				opts.SizeMiB = clonedMiB
			}
			opts.SizeDerived = sizeIsDerived(opts)
			return buildRootfsResult(opts, rootfsPath, prov), nil
		}
	}

	provenance, err := rootfs.NewBuilder().Build(ctx, buildRootfsRequest(opts, rootfsPath))
	if builtMiB := provenance.SizeBytes / (1024 * 1024); builtMiB > 0 && (builtMiB > opts.SizeMiB || sizeIsDerived(opts)) {
		opts.SizeMiB = builtMiB
	}
	opts.SizeDerived = sizeIsDerived(opts)
	if err == nil && opts.RootfsBaselineSave != nil && CanReuseRootfsBaseline(opts) {
		opts.RootfsBaselineSave(rootfsPath, provenance)
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
		SizeDerived:  opts.SizeDerived,
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

// CanReuseRootfsBaseline reports whether the workspace's rootfs would be
// identical to a pulled/tagged image baseline. With every per-workspace
// fact — command, env, ports, mounts, forwards, console shell, hostname,
// declared files — traveling on the per-boot config disk, a rootfs depends
// only on the OCI image, the injected guest init, and the size:
//
//   - an explicitly requested size disqualifies because baselines are
//     built at the default size (BaselineSatisfiesSize additionally guards
//     profile-implied sizes at resolve time);
//   - the guest-init match is checked by the resolver against the
//     baseline's recorded init hash (BaselineMatchesInit) — otherwise an
//     upgraded microagent would keep cloning baselines carrying the old
//     init forever.
func CanReuseRootfsBaseline(opts Options) bool {
	// A custom headroom changes what the derived size would be, and the
	// baseline was built with the default; build fresh rather than hand
	// over a disk sized for someone else's headroom. A setuid-preserving
	// workspace also builds fresh: baselines are stripped (the default), and
	// the resolver refuses records that don't say so — so the preserved
	// variant never enters the baseline pool from either side.
	return !opts.SizeExplicit && !opts.SpecSize && opts.HeadroomMiB == 0 && !opts.AllowGuestSetuid
}

// GuestInitSHA256 hashes the guest init binary the workspace would inject;
// empty when the path is unset or unreadable. Baseline records carry the
// hash of the init they were built with, and reuse requires equality.
func GuestInitSHA256(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return ""
	}
	return sum
}

// BaselineSatisfiesSize reports whether a baseline's actual disk is at
// least as large as the workspace's effective size request. Profile
// defaults can imply sizes above the baseline's build size without setting
// SizeExplicit/SpecSize, so this is checked against the resolved
// provenance rather than inside CanReuseRootfsBaseline.
func BaselineSatisfiesSize(prov rootfs.Provenance, opts Options) bool {
	if prov.SizeBytes <= 0 {
		return false
	}
	return prov.SizeBytes >= opts.SizeMiB*1024*1024
}

// localImageLayoutPath returns the committed-OCI layout path for stateDir.
// This mirrors commit.LayoutPath without importing pkg/commit: pkg/commit
// already imports pkg/workspace, so importing it back here would create an
// import cycle.
func localImageLayoutPath(stateDir string) string {
	return filepath.Join(stateDir, "images", "oci")
}

// buildRootfsRequest composes the rootfs build request. Nothing
// per-workspace goes in: command, env, ports, mounts, forwards, console
// shell, and declared files all travel on the per-boot config disk, so the
// built image depends only on the OCI image, the injected init binary, and
// the size — which is what makes baseline reuse possible at all.
func buildRootfsRequest(opts Options, rootfsPath string) rootfs.BuildRequest {
	return rootfs.BuildRequest{
		ImageRef:         opts.ImageRef,
		Platform:         rootfs.Platform{OS: "linux", Architecture: opts.Architecture},
		OutputPath:       rootfsPath,
		Format:           WorkspaceRootfsFormat(opts.Backend),
		InitPath:         rootfs.DefaultInitPath,
		InitBinaryPath:   opts.GuestInitPath,
		StateDir:         filepath.Join(opts.StateDir, "build"),
		BaseCacheDir:     rootfs.BaseCacheDirFor(opts.StateDir),
		LocalImageLayout: localImageLayoutPath(opts.StateDir),
		Mke2fsPath:       opts.Mke2fsPath,
		DebugfsPath:      opts.DebugfsPath,
		SizeMiB:          opts.SizeMiB,
		AutoSize:         !opts.SizeExplicit && !opts.SpecSize,
		DeriveSize:       sizeIsDerived(opts),
		HeadroomMiB:      opts.HeadroomMiB,
		AllowMutable:     true,
		AllowGuestSetuid: opts.AllowGuestSetuid,
		Progress:         opts.Progress,
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

// guestInitArtifactPath is the durable, content-addressed per-workspace copy of
// the init binary embedded in that workspace's rootfs.
func guestInitArtifactPath(stateDir, name, arch, contentSHA string) string {
	filename := "microagent-guestinit-" + NormalizeArch(arch)
	if contentSHA != "" {
		filename += "-" + contentSHA
	}
	return filepath.Join(stateDir, "workspaces", name, "artifacts", filename)
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
				SourcePath:  disk.SourcePath,
				OutputPath:  outputPath,
				Format:      WorkspaceDiskFormat(opts.Backend),
				StateDir:    filepath.Join(opts.StateDir, "build"),
				Mke2fsPath:  opts.Mke2fsPath,
				DebugfsPath: opts.DebugfsPath,
				SizeMiB:     64,
				AutoSize:    true,
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
// mediator resolves and injects the reference host-side at request time, so it
// is absent from guest request state. Upstream response behavior remains
// service trust. This is a no-op when no providers are declared.
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

// sizeIsDerived reports whether nothing pinned the disk size: no explicit
// --size, no spec size, and no explicitly chosen profile. When true, the
// built disk is sized from image content plus headroom, in either
// direction, instead of starting from the profile constant.
func sizeIsDerived(opts Options) bool {
	return !opts.SizeExplicit && !opts.SpecSize && !opts.ProfileExplicit
}
