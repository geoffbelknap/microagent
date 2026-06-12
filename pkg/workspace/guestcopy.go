package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// Guest-mediated copy: on backends without host ext4 tooling for the disk
// format (GuestMediatedCopy capability), cp, artifact extraction, and commit
// ride the guest's structured exec channel. The workspace must be stopped —
// the operation boots it transiently in maintenance mode (shell/exec only,
// no service command, no secrets), performs the file operation, and halts it
// again.

// guestCopyChunkBytes stays comfortably under the exec protocol's 10 MiB
// stdin and stdout caps so every transfer chunk fits one exec round trip.
const guestCopyChunkBytes = 8 * 1024 * 1024

// guestMediatedCopyEnabled reports whether file operations ride the guest
// exec channel on this host. A package variable so the debugfs-path unit
// tests stay runnable on Windows hosts.
var guestMediatedCopyEnabled = func() bool {
	return vmkit.BackendCapabilities(HostBackend()).GuestMediatedCopy
}

// maintenanceHaltTimeout bounds the halt that ends a maintenance boot. It
// runs on its own context so a canceled operation context cannot leak a
// running maintenance workspace.
var maintenanceHaltTimeout = 2 * time.Minute

// startMaintenanceHook and controlMaintenanceHook let tests substitute the
// boot machinery.
var startMaintenanceHook = func(ctx context.Context, opts Options) error {
	_, err := Start(ctx, opts)
	return err
}
var controlMaintenanceHook = func(ctx context.Context, opts Options, command string) error {
	_, err := Control(ctx, opts, command)
	return err
}

// withMaintenanceBoot boots the stopped workspace in maintenance mode, runs
// fn, and halts the workspace again. The boot is minimal and host-neutral:
// isolated networking (no HNS/NAT requirement), no secrets, no model
// pairing, no result delivery — only the channels file operations need.
func withMaintenanceBoot(ctx context.Context, stateDir, name string, fn func(Options) error) error {
	manifest, err := ReadManifest(stateDir, name)
	if err != nil {
		return err
	}
	base := Options{
		StateDir:     stateDir,
		Name:         name,
		Backend:      HostBackend(),
		Architecture: GuestArch(),
	}
	opts := OptionsFromManifest(base, manifest)
	opts.Name = name
	opts.MaintenanceBoot = true
	opts.Network = NormalizeNetworkConfig(vmkit.NetworkConfig{Mode: "isolated"})
	opts.Secrets = nil
	opts.SecretEnvFiles = nil
	opts.OnDemandSecrets = nil
	opts.SecretsAudit = false
	opts.Model = ""
	opts.ModelTarget = ""
	opts.Mediation = nil
	opts.ResultPort = 0
	opts.SerialInput = BackendSupportsConsoleInput(opts.Backend)
	if err := startMaintenanceHook(ctx, opts); err != nil {
		return fmt.Errorf("maintenance boot of workspace %s: %w", name, err)
	}
	defer func() {
		// The halt gets its own context: a canceled operation context must
		// not leak a running maintenance workspace.
		haltCtx, cancel := context.WithTimeout(context.Background(), maintenanceHaltTimeout)
		defer cancel()
		if err := controlMaintenanceHook(haltCtx, Options{StateDir: stateDir, Name: name, Backend: opts.Backend}, "halt"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: halt after maintenance boot of workspace %s: %v\n", name, err)
		}
	}()
	return fn(opts)
}

// guestExecArgv runs a positional-argument shell command in the guest so
// attacker-influenced paths can never be parsed as shell syntax.
func guestExec(ctx context.Context, opts Options, stdin []byte, script string, args ...string) (execprotocol.ExecResult, error) {
	argv := append([]string{"sh", "-c", script, "sh"}, args...)
	req := execprotocol.NewExecRequest(argv)
	req.Stdin = stdin
	req.OutputLimitBytesStdout = execprotocol.DefaultOutputLimitBytes
	req.OutputLimitBytesStderr = 64 * 1024
	return Exec(ctx, opts, req)
}

func guestExecFailed(result execprotocol.ExecResult, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		detail := strings.TrimSpace(string(result.Stderr))
		if detail == "" && result.ExitCode != nil {
			detail = fmt.Sprintf("exit code %d", *result.ExitCode)
		}
		if detail == "" {
			detail = fmt.Sprintf("status %s", result.Status)
		}
		return fmt.Errorf("%s: %s", operation, detail)
	}
	return nil
}

// guestFileSize returns the byte size of a guest file, failing when the file
// does not exist or is not a regular file.
func guestFileSize(ctx context.Context, opts Options, guestPath string) (int64, error) {
	result, err := guestExec(ctx, opts, nil, `test -f "$1" && wc -c < "$1"`, guestPath)
	if err := guestExecFailed(result, err, fmt.Sprintf("stat guest file %s", guestPath)); err != nil {
		return 0, fmt.Errorf("workspace path %s is not a readable file: %w", guestPath, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(result.Stdout)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse guest file size for %s: %w", guestPath, err)
	}
	return size, nil
}

// guestReadFile streams a guest file to w in exec-sized chunks.
func guestReadFile(ctx context.Context, opts Options, guestPath string, w io.Writer) (int64, error) {
	size, err := guestFileSize(ctx, opts, guestPath)
	if err != nil {
		return 0, err
	}
	var total int64
	for total < size {
		// bs=chunk with skip in whole chunks keeps dd reads aligned; count=1
		// reads exactly one chunk (short at EOF).
		result, err := guestExec(ctx, opts, nil,
			`dd if="$1" bs=`+strconv.Itoa(guestCopyChunkBytes)+` skip="$2" count=1 2>/dev/null`,
			guestPath, strconv.FormatInt(total/guestCopyChunkBytes, 10))
		if err := guestExecFailed(result, err, fmt.Sprintf("read guest file %s", guestPath)); err != nil {
			return total, err
		}
		if result.StdoutTruncated {
			return total, fmt.Errorf("read guest file %s: chunk exceeded the exec output limit", guestPath)
		}
		if len(result.Stdout) == 0 {
			break
		}
		n, err := w.Write(result.Stdout)
		if err != nil {
			return total, err
		}
		total += int64(n)
	}
	if total != size {
		return total, fmt.Errorf("read guest file %s: got %d bytes, want %d", guestPath, total, size)
	}
	return total, nil
}

// guestWriteFile writes data to a guest path in exec-sized chunks. The
// parent directory must already exist (matching the debugfs copy contract).
func guestWriteFile(ctx context.Context, opts Options, guestPath string, data []byte) error {
	parent := path.Dir(guestPath)
	result, err := guestExec(ctx, opts, nil, `test -d "$1"`, parent)
	if err != nil {
		return fmt.Errorf("check guest parent %s: %w", parent, err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		return fmt.Errorf("workspace path parent %s does not exist in the image", parent)
	}
	for offset := 0; ; offset += guestCopyChunkBytes {
		end := offset + guestCopyChunkBytes
		if end > len(data) {
			end = len(data)
		}
		script := `cat >> "$1"`
		if offset == 0 {
			script = `cat > "$1"`
		}
		result, err := guestExec(ctx, opts, data[offset:end], script, guestPath)
		if err := guestExecFailed(result, err, fmt.Sprintf("write guest file %s", guestPath)); err != nil {
			return err
		}
		if end == len(data) {
			break
		}
	}
	// The maintenance halt is graceful, but make the write durable before
	// the boot ends regardless.
	result, err = guestExec(ctx, opts, nil, `sync`)
	return guestExecFailed(result, err, fmt.Sprintf("sync guest file %s", guestPath))
}

// guestPathForEndpoint maps a remote copy endpoint onto the guest mount
// namespace: rootfs paths pass through, disk paths gain their mountpoint.
func guestPathForEndpoint(stateDir string, remote remoteCopyEndpoint) (string, error) {
	if remote.Disk == "" || remote.Disk == "rootfs" {
		return remote.Path, nil
	}
	manifest, err := ReadManifest(stateDir, remote.Workspace)
	if err != nil {
		return "", err
	}
	for _, disk := range manifest.Disks {
		if disk.Name == remote.Disk {
			mount := strings.TrimRight(disk.Mountpoint, "/")
			if mount == "" {
				return "", fmt.Errorf("workspace %s disk %q has no mountpoint", remote.Workspace, remote.Disk)
			}
			return mount + remote.Path, nil
		}
	}
	return "", fmt.Errorf("workspace %s has no disk %q", remote.Workspace, remote.Disk)
}

func copyFromWorkspaceGuest(ctx context.Context, stateDir string, remote remoteCopyEndpoint, localTarget string) (CopyResult, error) {
	if err := validateRemoteCopyPath(remote.Path); err != nil {
		return CopyResult{}, err
	}
	if err := EnsureCloneable(stateDir, remote.Workspace); err != nil {
		return CopyResult{}, err
	}
	guestPath, err := guestPathForEndpoint(stateDir, remote)
	if err != nil {
		return CopyResult{}, err
	}
	target, err := localCopyTarget(localTarget, path.Base(remote.Path))
	if err != nil {
		return CopyResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return CopyResult{}, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".microagent-cp-*")
	if err != nil {
		return CopyResult{}, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	var bytes int64
	err = withMaintenanceBoot(ctx, stateDir, remote.Workspace, func(opts Options) error {
		n, err := guestReadFile(ctx, Options{StateDir: stateDir, Name: remote.Workspace, Backend: opts.Backend}, guestPath, tmp)
		bytes = n
		return err
	})
	if err != nil {
		return CopyResult{}, err
	}
	if err := tmp.Close(); err != nil {
		return CopyResult{}, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return CopyResult{}, err
	}
	cleanup = false
	return CopyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "from-workspace",
		Source:    remote.Raw,
		Target:    target,
		ImagePath: WorkspaceRootfsPath(stateDir, remote.Workspace, HostBackend()),
		Bytes:     bytes,
	}, nil
}

func copyToWorkspaceGuest(ctx context.Context, stateDir, localSource string, remote remoteCopyEndpoint) (CopyResult, error) {
	if err := validateRemoteCopyPath(remote.Path); err != nil {
		return CopyResult{}, err
	}
	if err := EnsureCloneable(stateDir, remote.Workspace); err != nil {
		return CopyResult{}, err
	}
	info, err := os.Stat(localSource)
	if err != nil {
		return CopyResult{}, err
	}
	if !info.Mode().IsRegular() {
		return CopyResult{}, fmt.Errorf("source must be a regular file: %s", localSource)
	}
	if err := ensureRemoteWritable(stateDir, remote); err != nil {
		return CopyResult{}, err
	}
	guestPath, err := guestPathForEndpoint(stateDir, remote)
	if err != nil {
		return CopyResult{}, err
	}
	data, err := os.ReadFile(localSource)
	if err != nil {
		return CopyResult{}, err
	}
	err = withMaintenanceBoot(ctx, stateDir, remote.Workspace, func(opts Options) error {
		return guestWriteFile(ctx, Options{StateDir: stateDir, Name: remote.Workspace, Backend: opts.Backend}, guestPath, data)
	})
	if err != nil {
		return CopyResult{}, err
	}
	return CopyResult{
		Workspace: remote.Workspace,
		Disk:      remote.Disk,
		Direction: "to-workspace",
		Source:    localSource,
		Target:    remote.Raw,
		ImagePath: WorkspaceRootfsPath(stateDir, remote.Workspace, HostBackend()),
		Bytes:     int64(len(data)),
	}, nil
}

// GuestRootfsLayerTar tars the workspace's filesystem from inside a
// maintenance boot and returns it normalized as an OCI layer tar — the
// guest-mediated equivalent of a debugfs rdump for commit, without staging
// through the host filesystem (which cannot represent symlinks
// unprivileged on Windows). Kernel-managed virtual filesystems and the
// transient export tarball itself are excluded.
func GuestRootfsLayerTar(ctx context.Context, stateDir, name string) ([]byte, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := EnsureCloneable(stateDir, name); err != nil {
		return nil, err
	}
	const exportPath = "/.microagent-commit-export.tar"
	var layer []byte
	err := withMaintenanceBoot(ctx, stateDir, name, func(opts Options) error {
		execOpts := Options{StateDir: stateDir, Name: name, Backend: opts.Backend}
		// Tar to a file first: a single exec caps stdout at the protocol
		// limit, so the archive is chunk-read afterwards. Virtual
		// filesystems are kernel state, not image content.
		result, err := guestExec(ctx, execOpts, nil,
			`cd / && tar -cf "$1" --exclude proc --exclude sys --exclude dev --exclude tmp --exclude run --exclude "$2" . && sync`,
			exportPath, strings.TrimPrefix(exportPath, "/"))
		if err := guestExecFailed(result, err, "archive guest filesystem"); err != nil {
			return err
		}
		var raw bytes.Buffer
		if _, err := guestReadFile(ctx, execOpts, exportPath, &raw); err != nil {
			return err
		}
		// Remove the export before the halt so it never lands in a clone
		// or a later commit.
		result, err = guestExec(ctx, execOpts, nil, `rm -f "$1" && sync`, exportPath)
		if err := guestExecFailed(result, err, "remove guest export archive"); err != nil {
			return err
		}
		normalized, err := normalizeGuestLayerTar(raw.Bytes())
		if err != nil {
			return err
		}
		layer = normalized
		return nil
	})
	return layer, err
}

// normalizeGuestLayerTar rewrites a guest-produced tar into OCI layer
// conventions: clean relative slash paths, zeroed timestamps, and only
// regular files, directories, symlinks, and hard links (devices, sockets,
// and FIFOs are skipped, matching the host-side layer builder). Entry order
// is preserved — busybox-style hard links reference the first occurrence of
// their target, so reordering would break extraction.
func normalizeGuestLayerTar(data []byte) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(data))
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read guest archive: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return nil, fmt.Errorf("unsafe archive path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
		default:
			// Devices, sockets, and FIFOs cannot be represented
			// unprivileged and do not belong in a committed layer.
			continue
		}
		out := *header
		out.Name = name
		if header.Typeflag == tar.TypeDir && !strings.HasSuffix(out.Name, "/") {
			out.Name += "/"
		}
		if header.Typeflag == tar.TypeLink {
			out.Linkname = path.Clean(strings.TrimPrefix(header.Linkname, "./"))
			if strings.HasPrefix(out.Linkname, "../") || path.IsAbs(out.Linkname) {
				return nil, fmt.Errorf("unsafe archive hard link target %q", header.Linkname)
			}
		}
		out.ModTime = time.Unix(0, 0).UTC()
		out.AccessTime = time.Time{}
		out.ChangeTime = time.Time{}
		out.Uid = 0
		out.Gid = 0
		out.Uname = ""
		out.Gname = ""
		if err := writer.WriteHeader(&out); err != nil {
			return nil, err
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := io.Copy(writer, reader); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
