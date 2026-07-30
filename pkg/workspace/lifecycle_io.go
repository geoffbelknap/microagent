package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/eventhistory"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func appendEvent(path string, event EventFile) error {
	return eventhistory.Append(path, event, eventhistory.Options{})
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func CopyFile(source, target string, mode os.FileMode) error {
	// Reflink first: on btrfs/XFS (Linux) and APFS (macOS) the clone is
	// metadata-only, so cloning a baseline rootfs costs milliseconds
	// instead of a full byte copy. Filesystems without reflink fall
	// through to the byte copy below.
	if cloneFile(source, target, mode) {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr != nil {
		_ = out.Close()
		return copyErr
	}
	if chmodErr := out.Chmod(mode); chmodErr != nil {
		_ = out.Close()
		return chmodErr
	}
	closeErr := out.Close()
	if closeErr != nil {
		return closeErr
	}
	return nil
}

// CopyFileReplace copies source over target, replacing any existing file.
func CopyFileReplace(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr != nil {
		_ = out.Close()
		return copyErr
	}
	if chmodErr := out.Chmod(mode); chmodErr != nil {
		_ = out.Close()
		return chmodErr
	}
	if closeErr := out.Close(); closeErr != nil {
		return closeErr
	}
	return nil
}

// pinGuestInitArtifact copies the init binary a workspace is about to inject
// into that workspace's durable state. Package-manager install paths (notably a
// versioned Homebrew Cellar directory) disappear on upgrade; recording one in
// workspace verification made an unchanged rootfs look divergent later.
//
// The pinned copy is also the build input, so the path and SHA-256 recorded in
// the manifest identify the exact bytes embedded in the rootfs.
func pinGuestInitArtifact(opts *Options) error {
	source := strings.TrimSpace(opts.GuestInitPath)
	if source == "" {
		return fmt.Errorf("guest init path is empty")
	}
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("read guest init %s: %w", source, err)
	}
	if info.IsDir() {
		return fmt.Errorf("guest init %s is a directory", source)
	}
	contentSHA, err := FileSHA256(source)
	if err != nil {
		return fmt.Errorf("hash guest init %s: %w", source, err)
	}
	target := guestInitArtifactPath(opts.StateDir, opts.Name, opts.Architecture, contentSHA)
	if filepath.Clean(source) == filepath.Clean(target) {
		opts.GuestInitPath = target
		return nil
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create guest init artifact directory: %w", err)
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open guest init %s: %w", source, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(dir, ".guest-init-*.tmp")
	if err != nil {
		return fmt.Errorf("create guest init artifact: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy guest init artifact: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("make guest init artifact executable: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close guest init artifact: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("install guest init artifact: %w", err)
	}
	cleanup = false
	opts.GuestInitPath = target
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func egressCACertSHA256(wsDir string) (string, error) {
	pemBytes, err := os.ReadFile(filepath.Join(wsDir, "egress-ca.pem"))
	if err != nil {
		return "", fmt.Errorf("read egress CA cert: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("egress CA cert at %s is not a valid CERTIFICATE PEM", wsDir)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func guestIPFromNetwork(network vmkit.NetworkConfig) string {
	ip := strings.TrimSpace(network.IP)
	if ip == "" && network.Runtime != nil {
		ip = strings.TrimSpace(network.Runtime.IP)
	}
	if ip == "" {
		return ""
	}
	if host, _, err := net.ParseCIDR(ip); err == nil {
		return host.String()
	}
	if strings.Contains(ip, "/") {
		return strings.SplitN(ip, "/", 2)[0]
	}
	return ip
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func firstTime(values ...string) *time.Time {
	for _, value := range values {
		if parsed := parseOptionalTime(value); parsed != nil {
			return parsed
		}
	}
	return nil
}

func fileModTime(path string) *time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mod := info.ModTime().UTC()
	return &mod
}
