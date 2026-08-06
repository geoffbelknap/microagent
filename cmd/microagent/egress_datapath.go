//go:build !windows

package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/applevfnet"
	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// runEgressDatapath is the hidden subprocess the apple-vf supervisor spawns to
// own the guest's host-fd NIC: it runs the userspace gVisor datapath over an
// inherited datagram socket fd carrying the guest's Ethernet frames. It exits
// when the socket closes or it receives SIGTERM/SIGINT (the supervisor signals
// it when the VM stops).
// egressDatapathOpts holds the parsed --egress-datapath flags. Registration is
// factored into newEgressDatapathFlagSet so the parity test can introspect the
// flag surface (every vmkit.EgressDatapathFields control must have a flag here).
type egressDatapathOpts struct {
	fdNum                              *int
	gatewayIP, gatewayIPv6, gatewayMAC *string
	mode, stateDir, name, sessionID    *string
	swapConfig                         *string
	lockAllowlist                      *bool
	allow, passthrough, resolver       csvFlag
	maxBytesPerSec, maxTotalBytes      *int64
	maxConns                           *int
	auditMaxBytes                      *int64
	auditMaxBackups                    *int
}

func newEgressDatapathFlagSet() (*flag.FlagSet, *egressDatapathOpts) {
	fs := flag.NewFlagSet("egress-datapath", flag.ContinueOnError)
	o := &egressDatapathOpts{}
	o.fdNum = fs.Int("fd", -1, "inherited datagram socket fd carrying guest Ethernet frames")
	o.gatewayIP = fs.String("gateway-ip", "", "IPv4 address the gateway owns and answers ARP for")
	o.gatewayIPv6 = fs.String("gateway-ipv6", "", "IPv6 address the gateway owns")
	o.gatewayMAC = fs.String("gateway-mac", "", "gateway MAC address (optional)")
	o.mode = fs.String("egress-mode", "", "egress mediation mode: broker, mitm, or off")
	o.stateDir = fs.String("state-dir", "", "workspace state directory")
	o.name = fs.String("name", "", "workspace name")
	o.sessionID = fs.String("session-id", "", "workspace execution session identity")
	o.swapConfig = fs.String("swap-config", "", "credential swap config path")
	o.lockAllowlist = fs.Bool("lock-allowlist", false, "restrict egress to allowlisted destinations only (drop the allow-broad grant)")
	fs.Var(&o.allow, "allow", "allowlisted egress destination host (repeatable)")
	fs.Var(&o.passthrough, "passthrough", "passthrough host (repeatable)")
	fs.Var(&o.resolver, "resolver", "resolver IP the datapath may forward guest DNS to (the workspace nameservers; repeatable)")
	o.maxBytesPerSec = fs.Int64("max-bps", 0, "rate-limit the upstream-bound copy of each flow (bytes/sec; 0=unlimited)")
	o.maxTotalBytes = fs.Int64("max-bytes", 0, "cap cumulative egress bytes before the breaching flow is torn down (0=unlimited)")
	o.maxConns = fs.Int("max-conns", 0, "cap concurrently mediated TCP connections (0=unlimited)")
	o.auditMaxBytes = fs.Int64("audit-max-bytes", 0, "rotate the audit log when an active file would exceed this many bytes (0=unbounded)")
	o.auditMaxBackups = fs.Int("audit-max-backups", 0, "number of rotated audit-log backups to keep (with --audit-max-bytes)")
	return fs, o
}

func runEgressDatapath(ctx context.Context, args []string) error {
	fs, o := newEgressDatapathFlagSet()
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *o.fdNum < 0 {
		return fmt.Errorf("egress-datapath: --fd is required")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go exitWhenParentExits(ctx, os.Getppid(), func() { os.Exit(0) })
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "egress-datapath: "+format+"\n", a...)
	}
	cfg := applevfnet.Config{Logf: logf}
	if vmkit.EgressMediationOn(*o.mode) {
		if strings.TrimSpace(*o.stateDir) == "" || strings.TrimSpace(*o.name) == "" {
			return fmt.Errorf("egress-datapath: --state-dir and --name are required for mediated egress")
		}
		h, err := hostFDMediator(hostFDEgressConfig{
			stateDir:      *o.stateDir,
			name:          *o.name,
			sessionID:     *o.sessionID,
			mode:          *o.mode,
			swapConfig:    *o.swapConfig,
			lockAllowlist: *o.lockAllowlist,
			allow:         []string(o.allow),
			passthrough:   []string(o.passthrough),
			resolvers:     []string(o.resolver),
			limits: egress.Limits{
				MaxBytesPerSec:     *o.maxBytesPerSec,
				MaxTotalBytes:      *o.maxTotalBytes,
				MaxConcurrentConns: int32(*o.maxConns),
			},
			auditMaxBytes:   *o.auditMaxBytes,
			auditMaxBackups: *o.auditMaxBackups,
		})
		if err != nil {
			return err
		}
		var orig sync.Map
		h.OrigDst = func(c net.Conn) (netip.AddrPort, error) {
			if v, ok := orig.Load(c); ok {
				orig.Delete(c)
				return v.(netip.AddrPort), nil
			}
			return netip.AddrPort{}, fmt.Errorf("apple-vf host-fd: unknown mediated tcp connection")
		}
		cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network != "tcp" {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			}
			dst, err := netip.ParseAddrPort(addr)
			if err != nil {
				return nil, err
			}
			guest, mediator := net.Pipe()
			orig.Store(mediator, dst)
			go h.Handle(mediator)
			return guest, nil
		}
		cfg.UDPHandler = h.HandleUDPConn
	}
	return applevfnet.RunFromFDConfig(ctx, *o.fdNum, *o.gatewayIP, *o.gatewayIPv6, *o.gatewayMAC, cfg)
}

type csvFlag []string

func (f *csvFlag) String() string { return strings.Join(*f, ",") }
func (f *csvFlag) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if s := strings.TrimSpace(part); s != "" {
			*f = append(*f, s)
		}
	}
	return nil
}

// hostFDEgressConfig is the full egress-policy input to hostFDMediator, mirroring
// the controls the Firecracker mediator receives (see vmkit.EgressDatapathFields).
// Every field here must trace back to a --egress-datapath flag so nothing an
// operator sets is silently dropped on Apple VF.
type hostFDEgressConfig struct {
	stateDir, name, sessionID, mode, swapConfig string
	lockAllowlist                               bool
	allow, passthrough, resolvers               []string
	limits                                      egress.Limits
	auditMaxBytes                               int64
	auditMaxBackups                             int
}

func hostFDMediator(cfg hostFDEgressConfig) (*egress.Handler, error) {
	policy, err := egress.NewPolicy(cfg.allow)
	if err != nil {
		return nil, err
	}
	auditPath := filepath.Join(cfg.stateDir, cfg.name, "egress-access.jsonl")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		return nil, err
	}
	// Size-bounded rotating audit log when a cap is set (ASK tenet 8), matching
	// the Firecracker mediator; otherwise the unbounded single-file logger.
	logger, err := newHostFDAuditLogger(auditPath, cfg.auditMaxBytes, cfg.auditMaxBackups)
	if err != nil {
		return nil, err
	}
	identityLogger := egress.IdentityLogger{Logger: logger, RuntimeID: cfg.name, SessionID: cfg.sessionID}
	// Resolver allowlist: the workspace nameservers. Invalid entries are skipped
	// (mirrors egress.Run); an empty set leaves the internal-address floor.
	var resolvers []netip.Addr
	for _, r := range cfg.resolvers {
		if a, perr := netip.ParseAddr(strings.TrimSpace(r)); perr == nil {
			resolvers = append(resolvers, a)
		}
	}
	certPath, keyPath, err := ensureHostFDEgressCA(cfg.stateDir, cfg.name)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	ca, err := egress.LoadCA(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	var pass *egress.Policy
	if len(cfg.passthrough) > 0 {
		pass, err = egress.NewPolicy(cfg.passthrough)
		if err != nil {
			return nil, err
		}
	}
	var swaps *egress.SwapTable
	if strings.TrimSpace(cfg.swapConfig) != "" {
		data, err := os.ReadFile(cfg.swapConfig)
		if err != nil {
			return nil, fmt.Errorf("egress: read swap config: %w", err)
		}
		swaps, err = egress.LoadSwapTable(data)
		if err != nil {
			return nil, err
		}
	}
	h := &egress.Handler{
		Mode: cfg.mode, Policy: policy, Logger: identityLogger, Dial: net.Dial,
		CA: ca, Passthrough: pass, NameCache: egress.NewNameCache(),
		AllowlistLocked: cfg.lockAllowlist,
		Resolvers:       resolvers,
		Limits:          cfg.limits,
	}
	h.EnableSwaps(swaps)
	identityLogger.Log("egress_listen", map[string]any{"provider": vmkit.EgressProviderAppleVFHostFD, "allow": cfg.allow, "allowlistLocked": cfg.lockAllowlist})
	return h, nil
}

// newHostFDAuditLogger opens the datapath's audit log, size-bounded and rotating
// when maxBytes > 0 (ASK tenet 8), else an unbounded single file — the same
// choice egress.Run makes for the Firecracker mediator.
func newHostFDAuditLogger(path string, maxBytes int64, maxBackups int) (egress.Logger, error) {
	if maxBytes > 0 {
		return egress.NewRotatingFileLogger(path, maxBytes, maxBackups)
	}
	return egress.NewFileLogger(path)
}

func ensureHostFDEgressCA(stateDir, name string) (string, string, error) {
	wsDir := filepath.Join(stateDir, name)
	certPath := filepath.Join(wsDir, "egress-ca.pem")
	keyPath := filepath.Join(wsDir, "egress-ca-key.pem")
	if certPEM, certErr := os.ReadFile(certPath); certErr == nil {
		if _, keyErr := os.Stat(keyPath); keyErr != nil {
			return "", "", fmt.Errorf("egress: CA cert exists but key is unavailable: %w", keyErr)
		}
		if _, err := egressCertSHA256(certPEM); err != nil {
			return "", "", err
		}
		return certPath, keyPath, nil
	}
	ca, err := egress.NewCA(name, 720*time.Hour)
	if err != nil {
		return "", "", err
	}
	keyPEM, err := ca.KeyPEM()
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(wsDir, 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(certPath, ca.CertPEM(), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		_ = os.Remove(certPath)
		return "", "", err
	}
	return certPath, keyPath, nil
}

func egressCertSHA256(pemBytes []byte) (string, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", fmt.Errorf("egress: invalid CA cert PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(parsed.Raw)
	return hex.EncodeToString(sum[:]), nil
}

func exitWhenParentExits(ctx context.Context, parentPID int, exit func()) {
	if parentPID <= 1 {
		exit()
		return
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ppid := os.Getppid(); ppid <= 1 || ppid != parentPID {
				exit()
				return
			}
		}
	}
}
