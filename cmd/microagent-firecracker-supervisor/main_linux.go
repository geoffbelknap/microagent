//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/geoffbelknap/microagent/internal/egress"
	firecrackersupervisor "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type egressAllowFlag []string

func (f *egressAllowFlag) String() string     { return strings.Join(*f, ",") }
func (f *egressAllowFlag) Set(v string) error { *f = append(*f, v); return nil }

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	if len(args) > 0 && args[0] == "--port-forwarder" {
		fs := flag.NewFlagSet("port-forwarder", flag.ContinueOnError)
		stateDir := fs.String("state-dir", "", "State directory")
		name := fs.String("name", "", "Workspace name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *stateDir == "" || *name == "" {
			return fmt.Errorf("usage: microagent-firecracker-supervisor --port-forwarder --state-dir <dir> --name <name>")
		}
		return firecrackersupervisor.RunPortForwarder(ctx, firecrackersupervisor.Options{StateDir: *stateDir, Name: *name})
	}
	if len(args) > 0 && args[0] == "--deadman" {
		fs := flag.NewFlagSet("deadman", flag.ContinueOnError)
		stateDir := fs.String("state-dir", "", "State directory")
		name := fs.String("name", "", "Workspace name")
		leaseFD := fs.Int("lease-fd", -1, "Inherited runtime lease descriptor")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *stateDir == "" || *name == "" {
			return fmt.Errorf("usage: microagent-firecracker-supervisor --deadman --state-dir <dir> --name <name>")
		}
		if *leaseFD >= 0 {
			if *leaseFD < 3 {
				return fmt.Errorf("deadman runtime lease descriptor must be at least 3")
			}
			leaseFile := os.NewFile(uintptr(*leaseFD), "runtime-lease")
			if leaseFile == nil {
				return fmt.Errorf("open inherited deadman runtime lease descriptor %d", *leaseFD)
			}
			defer func() { _ = leaseFile.Close() }()
			return firecrackersupervisor.RunDeadmanWithRuntimeLease(ctx, firecrackersupervisor.Options{StateDir: *stateDir, Name: *name})
		}
		return firecrackersupervisor.RunDeadman(ctx, firecrackersupervisor.Options{StateDir: *stateDir, Name: *name})
	}
	if len(args) > 0 && args[0] == "--fork-mount-exec" {
		return firecrackersupervisor.RunForkMountExec(args[1:])
	}
	if len(args) > 0 && args[0] == "--confined-exec" {
		return firecrackersupervisor.RunConfinedExec(args[1:])
	}
	if len(args) > 0 && args[0] == "--tproxy-selfcheck" {
		// Doctor's TPROXY probe: install the real steering rule in the scratch
		// user+net namespace this process was launched into, and exit by
		// whether the kernel accepted it. See ProbeEgressTProxySupport.
		return firecrackersupervisor.RunEgressTProxyProbe()
	}
	if len(args) > 0 && args[0] == "--vsock-listener" {
		fs := flag.NewFlagSet("vsock-listener", flag.ContinueOnError)
		stateDir := fs.String("state-dir", "", "State directory")
		name := fs.String("name", "", "Workspace name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *stateDir == "" || *name == "" {
			return fmt.Errorf("usage: microagent-firecracker-supervisor --vsock-listener --state-dir <dir> --name <name>")
		}
		return firecrackersupervisor.RunVsockListener(ctx, firecrackersupervisor.Options{StateDir: *stateDir, Name: *name})
	}
	if len(args) > 0 && args[0] == "--egress-mediator" {
		opts, err := parseEgressMediatorOptions(args[1:])
		if err != nil {
			return err
		}
		opts.Ready = stdout
		mctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return egress.Run(mctx, opts)
	}
	req, err := readRequest(args)
	if err != nil {
		resp := vmkit.Response{OK: false, Backend: vmkit.BackendLinuxKVM, Error: err.Error()}
		_ = writeResponse(stdout, resp)
		return err
	}
	// For snapshot only: cancel the request context on SIGTERM/SIGINT so an
	// interrupted snapshot (which has paused the VM) sees cancellation and runs its
	// cleanup — resuming the guest — instead of dying with the VM left frozen. The
	// parent client (ExecutableSupervisor.Do) sends SIGTERM and grants a grace
	// window before forcing SIGKILL. Other commands keep the process's default
	// signal disposition: start in particular daemonizes long-lived processes and
	// must not have its boot aborted by catching these signals.
	rctx := ctx
	if req.Command == "snapshot" {
		var stop context.CancelFunc
		rctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	resp, err := firecrackersupervisor.Supervisor{}.Do(rctx, req)
	if writeErr := writeResponse(stdout, resp); writeErr != nil && err == nil {
		return writeErr
	}
	return err
}

// parseEgressMediatorOptions parses the `--egress-mediator` subcommand flags
// (args excluding the leading "--egress-mediator") into egress.Options. Pure (no
// I/O, no signal wiring) so the flag plumbing is unit-testable without starting
// the blocking mediator.
func parseEgressMediatorOptions(args []string) (egress.Options, error) {
	fs := flag.NewFlagSet("egress-mediator", flag.ContinueOnError)
	var bindHost, auditLog, mode string
	var bindPort int
	var allow egressAllowFlag
	var caCert, caKey string
	var swapConfig string
	var passthrough egressAllowFlag
	var resolvers egressAllowFlag
	// Bounded-operations caps (ASK tenet 8). Zero/default = unlimited (current
	// behavior). These are per-mediator-process (= per-workspace) and reset on
	// restart.
	var maxBPS, maxBytes, auditMaxBytes int64
	var maxConns int
	var auditMaxBackups int
	fs.StringVar(&mode, "mode", "", "Enforcement mode: guarded (default; deny-the-inside), broker (allow-broad, opaque splice), or strict (default-deny allowlist)")
	var lockAllowlist bool
	fs.BoolVar(&lockAllowlist, "lock-allowlist", false, "In broker mode, restrict egress to allowlisted destinations only (drop the allow-broad grant)")
	fs.StringVar(&bindHost, "bind-host", "127.0.0.1", "Bind host")
	fs.IntVar(&bindPort, "bind-port", 0, "Bind port")
	fs.StringVar(&auditLog, "audit-log", "", "JSONL audit log path")
	fs.Var(&allow, "allow", "Allowlisted destination host (repeatable)")
	fs.StringVar(&caCert, "ca-cert", "", "CA cert PEM path (enables TLS interception)")
	fs.StringVar(&caKey, "ca-key", "", "CA key PEM path")
	fs.StringVar(&swapConfig, "swap-config", "", "credential-swaps.yaml path")
	fs.Var(&passthrough, "passthrough", "Passthrough destination host (allowed, not intercepted; repeatable)")
	fs.Var(&resolvers, "resolver", "Resolver IP the mediator may forward guest DNS to (the workspace's configured nameservers; repeatable). Empty keeps the internal-address floor.")
	fs.Int64Var(&maxBPS, "max-bps", 0, "Max egress bytes/sec on the upstream-bound copy (0=unlimited)")
	fs.Int64Var(&maxBytes, "max-bytes", 0, "Max cumulative egress bytes across tcp+udp before the breaching flow is torn down (0=unlimited)")
	fs.IntVar(&maxConns, "max-conns", 0, "Max concurrent mediated TCP connections (0=unlimited)")
	fs.Int64Var(&auditMaxBytes, "audit-max-bytes", 0, "Rotate the audit log when an active file would exceed this many bytes (0=unbounded)")
	fs.IntVar(&auditMaxBackups, "audit-max-backups", 0, "Number of rotated audit-log backups to keep (with --audit-max-bytes)")
	if err := fs.Parse(args); err != nil {
		return egress.Options{}, err
	}
	if bindPort == 0 || strings.TrimSpace(auditLog) == "" {
		return egress.Options{}, fmt.Errorf("usage: microagent-firecracker-supervisor --egress-mediator --bind-port <port> --audit-log <path> [--bind-host <host>] [--allow <host> ...]")
	}
	return egress.Options{
		Mode:            mode,
		LockAllowlist:   lockAllowlist,
		BindHost:        bindHost,
		BindPort:        bindPort,
		AuditLogPath:    auditLog,
		Allow:           []string(allow),
		CACertPath:      caCert,
		CAKeyPath:       caKey,
		SwapConfigPath:  swapConfig,
		Passthrough:     []string(passthrough),
		Resolvers:       []string(resolvers),
		Limits:          egress.Limits{MaxBytesPerSec: maxBPS, MaxTotalBytes: maxBytes, MaxConcurrentConns: int32(maxConns)},
		AuditMaxBytes:   auditMaxBytes,
		AuditMaxBackups: auditMaxBackups,
	}, nil
}

func readRequest(args []string) (vmkit.Request, error) {
	switch {
	case len(args) == 2 && args[0] == "--request":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return vmkit.Request{}, err
		}
		return decodeRequest(data)
	case len(args) == 2 && args[0] == "--request-json":
		return decodeRequest([]byte(args[1]))
	case len(args) == 0:
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return vmkit.Request{}, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return vmkit.Request{}, fmt.Errorf("request JSON is required on stdin or with --request")
		}
		return decodeRequest(data)
	default:
		return vmkit.Request{}, fmt.Errorf("usage: microagent-firecracker-supervisor [--request <path>|--request-json <json>]")
	}
}

func decodeRequest(data []byte) (vmkit.Request, error) {
	var req vmkit.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return vmkit.Request{}, err
	}
	return req, nil
}

func writeResponse(stdout *os.File, resp vmkit.Response) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}
