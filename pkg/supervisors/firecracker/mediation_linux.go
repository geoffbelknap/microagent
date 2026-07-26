//go:build linux

package firecracker

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type egressCaps struct {
	maxBytesPerSec  int64
	maxTotalBytes   int64
	maxConns        int32
	auditMaxBytes   int64
	auditMaxBackups int
}

// applyManifestEgressCaps re-applies the bounded-operations caps recorded in a
// snapshot manifest onto the restore request's Config (ASK tenet 8), so a
// restored/forked workspace keeps the SAME bounds it was snapshotted under —
// mirroring how the persisted CA is reused. A no-op when config is nil. Manifest
// values overwrite the config's so the snapshot is authoritative for the restored
// posture (the manifest reproduces what the workspace was actually running).
func applyManifestEgressCaps(config *vmkit.Config, manifest vmkit.SnapshotManifest) {
	if config == nil {
		return
	}
	config.EgressMaxBytesPerSec = manifest.EgressMaxBytesPerSec
	config.EgressMaxTotalBytes = manifest.EgressMaxTotalBytes
	config.EgressMaxConcurrentConns = manifest.EgressMaxConcurrentConns
	config.EgressAuditMaxBytes = manifest.EgressAuditMaxBytes
	config.EgressAuditMaxBackups = manifest.EgressAuditMaxBackups
}

// egressCapsFromConfig extracts the caps from a workspace Config. Nil config (or
// all-zero caps) yields a zero egressCaps (unlimited).
func egressCapsFromConfig(config *vmkit.Config) egressCaps {
	if config == nil {
		return egressCaps{}
	}
	return egressCaps{
		maxBytesPerSec:  config.EgressMaxBytesPerSec,
		maxTotalBytes:   config.EgressMaxTotalBytes,
		maxConns:        config.EgressMaxConcurrentConns,
		auditMaxBytes:   config.EgressAuditMaxBytes,
		auditMaxBackups: config.EgressAuditMaxBackups,
	}
}

func egressMediatorArgs(bindHost string, port int, auditPath, mode string, lockAllowlist bool, allow, passthrough, resolvers []string, swapConfigPath string, peers []string, caCertPath, caKeyPath string, caps egressCaps) []string {
	args := []string{"--egress-mediator", "--bind-host", bindHost, "--bind-port", strconv.Itoa(port), "--audit-log", auditPath, "--mode", vmkit.ResolveEgressModeDefault(mode)}
	if lockAllowlist {
		args = append(args, "--lock-allowlist")
	}
	for _, h := range allow {
		args = append(args, "--allow", h)
	}
	if caCertPath != "" && caKeyPath != "" {
		args = append(args, "--ca-cert", caCertPath, "--ca-key", caKeyPath)
	}
	if swapConfigPath != "" {
		args = append(args, "--swap-config", swapConfigPath)
	}
	for _, h := range passthrough {
		args = append(args, "--passthrough", h)
	}
	// Resolver allowlist: the workspace's configured nameservers. Empty for a
	// workspace with no configured DNS, leaving the mediator's internal-address
	// floor in force.
	for _, r := range resolvers {
		args = append(args, "--resolver", r)
	}
	// Named-network peer roster (name=ip). Empty for nat/user (no roster). The
	// mediator reverse-resolves a bare-IP east-west destination to the peer's
	// workspace name and polices it by name under the same default-deny allowlist.
	for _, p := range peers {
		args = append(args, "--peer", p)
	}
	// Bounded-operations caps (ASK tenet 8). Each is emitted only when non-zero so
	// an uncapped workspace's argv is byte-identical to the pre-caps one.
	if caps.maxBytesPerSec > 0 {
		args = append(args, "--max-bps", strconv.FormatInt(caps.maxBytesPerSec, 10))
	}
	if caps.maxTotalBytes > 0 {
		args = append(args, "--max-bytes", strconv.FormatInt(caps.maxTotalBytes, 10))
	}
	if caps.maxConns > 0 {
		args = append(args, "--max-conns", strconv.Itoa(int(caps.maxConns)))
	}
	if caps.auditMaxBytes > 0 {
		args = append(args, "--audit-max-bytes", strconv.FormatInt(caps.auditMaxBytes, 10))
		if caps.auditMaxBackups > 0 {
			args = append(args, "--audit-max-backups", strconv.Itoa(caps.auditMaxBackups))
		}
	}
	return args
}

func startEgressMediator(opts Options, bindHost, mode string, lockAllowlist bool, allow, passthrough, resolvers []string, swapConfigPath string, peers []string, caCertPath, caKeyPath string, caps egressCaps) (int, int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		return 0, 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // port-allocation race is bounded: mediator readiness probe retries until it accepts
	exe, err := os.Executable()
	if err != nil {
		return 0, 0, err
	}
	auditPath := filepath.Join(opts.StateDir, opts.Name, "egress-access.jsonl")
	args := egressMediatorArgs(bindHost, port, auditPath, mode, lockAllowlist, allow, passthrough, resolvers, swapConfigPath, peers, caCertPath, caKeyPath, caps)
	logPath := filepath.Join(opts.StateDir, opts.Name, "egress-mediator.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, 0, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, err
	}
	// The logfile is opened O_APPEND and reused across mediator restarts, so a
	// stale readiness marker from a PRIOR run may already be present. Record the
	// current size and scan only bytes written AFTER this offset, so the marker
	// check observes this child's signal and never a stale one (which would be a
	// false-positive ready).
	var logStart int64
	if info, serr := logFile.Stat(); serr == nil {
		logStart = info.Size()
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	// Readiness requires BOTH the TCP listener to accept AND the mediator to have
	// emitted its post-UDP readiness marker to its logfile. The TCP listener
	// binds (in egress.Run) before the transparent UDP socket opens, so a
	// TCP-dial-only probe can pass during the window where UDP has not yet come
	// up — and if the UDP open then fails the mediator exits, leaving a confusing
	// half-provisioned start. Gating on the marker (written only after UDP is up)
	// closes that window. Fail-closed is preserved: a mediator that never signals
	// ready (never opens UDP) → the marker never appears → the deadline trips and
	// the start aborts after terminating the child.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, derr := net.DialTimeout("tcp", net.JoinHostPort(bindHost, strconv.Itoa(port)), 200*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			if egressMediatorLoggedReady(logPath, logStart) {
				return pid, port, nil
			}
		}
		if time.Now().After(deadline) {
			terminateAuxProcess(pid)
			return 0, 0, fmt.Errorf("egress mediator did not become ready on %s:%d", bindHost, port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// egressMediatorLoggedReady reports whether the mediator's logfile contains the
// post-UDP readiness marker (egress.ReadyMarker) in the bytes written at or
// after startOffset. The offset scoping ignores any stale marker from a prior
// run that shares this append-mode logfile. A read error (e.g. the file not yet
// created) reports false so the caller keeps polling until the deadline.
func egressMediatorLoggedReady(logPath string, startOffset int64) bool {
	f, err := os.Open(logPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if startOffset > 0 {
		if _, err := f.Seek(startOffset, io.SeekStart); err != nil {
			return false
		}
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), egress.ReadyMarker) {
			return true
		}
	}
	return false
}
