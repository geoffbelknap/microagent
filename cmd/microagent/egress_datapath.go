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
func runEgressDatapath(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("egress-datapath", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fdNum := fs.Int("fd", -1, "inherited datagram socket fd carrying guest Ethernet frames")
	gatewayIP := fs.String("gateway-ip", "", "IPv4 address the gateway owns and answers ARP for")
	gatewayMAC := fs.String("gateway-mac", "", "gateway MAC address (optional)")
	mode := fs.String("egress-mode", "", "egress mediation mode: guarded, strict, or off")
	stateDir := fs.String("state-dir", "", "workspace state directory")
	name := fs.String("name", "", "workspace name")
	swapConfig := fs.String("swap-config", "", "credential swap config path")
	var allow csvFlag
	var passthrough csvFlag
	fs.Var(&allow, "allow", "allowlisted egress destination host (repeatable)")
	fs.Var(&passthrough, "passthrough", "passthrough host (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fdNum < 0 {
		return fmt.Errorf("egress-datapath: --fd is required")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go exitWhenParentExits(ctx, os.Getppid(), func() { os.Exit(0) })
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "egress-datapath: "+format+"\n", a...)
	}
	cfg := applevfnet.Config{Logf: logf}
	if vmkit.EgressMediationOn(*mode) {
		if strings.TrimSpace(*stateDir) == "" || strings.TrimSpace(*name) == "" {
			return fmt.Errorf("egress-datapath: --state-dir and --name are required for mediated egress")
		}
		h, err := hostFDMediator(*stateDir, *name, *mode, []string(allow), []string(passthrough), *swapConfig)
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
	return applevfnet.RunFromFDConfig(ctx, *fdNum, *gatewayIP, *gatewayMAC, cfg)
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

func hostFDMediator(stateDir, name, mode string, allow, passthrough []string, swapConfig string) (*egress.Handler, error) {
	policy, err := egress.NewPolicy(allow)
	if err != nil {
		return nil, err
	}
	auditPath := filepath.Join(stateDir, name, "egress-access.jsonl")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		return nil, err
	}
	logger, err := egress.NewFileLogger(auditPath)
	if err != nil {
		return nil, err
	}
	certPath, keyPath, err := ensureHostFDEgressCA(stateDir, name)
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
	if len(passthrough) > 0 {
		pass, err = egress.NewPolicy(passthrough)
		if err != nil {
			return nil, err
		}
	}
	var swaps *egress.SwapTable
	if strings.TrimSpace(swapConfig) != "" {
		data, err := os.ReadFile(swapConfig)
		if err != nil {
			return nil, fmt.Errorf("egress: read swap config: %w", err)
		}
		swaps, err = egress.LoadSwapTable(data)
		if err != nil {
			return nil, err
		}
	}
	h := &egress.Handler{
		Mode: mode, Policy: policy, Logger: logger, Dial: net.Dial,
		CA: ca, Passthrough: pass, NameCache: egress.NewNameCache(),
	}
	h.EnableSwaps(swaps)
	logger.Log("egress_listen", map[string]any{"provider": vmkit.EgressProviderAppleVFHostFD, "allow": allow})
	return h, nil
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
