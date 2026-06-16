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
	if len(args) > 0 && args[0] == "--fork-mount-exec" {
		return firecrackersupervisor.RunForkMountExec(args[1:])
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
		fs := flag.NewFlagSet("egress-mediator", flag.ContinueOnError)
		var bindHost, auditLog string
		var bindPort int
		var allow egressAllowFlag
		var caCert, caKey string
		var passthrough egressAllowFlag
		fs.StringVar(&bindHost, "bind-host", "127.0.0.1", "Bind host")
		fs.IntVar(&bindPort, "bind-port", 0, "Bind port")
		fs.StringVar(&auditLog, "audit-log", "", "JSONL audit log path")
		fs.Var(&allow, "allow", "Allowlisted destination host (repeatable)")
		fs.StringVar(&caCert, "ca-cert", "", "CA cert PEM path (enables TLS interception)")
		fs.StringVar(&caKey, "ca-key", "", "CA key PEM path")
		fs.Var(&passthrough, "passthrough", "Passthrough destination host (allowed, not intercepted; repeatable)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if bindPort == 0 || strings.TrimSpace(auditLog) == "" {
			return fmt.Errorf("usage: microagent-firecracker-supervisor --egress-mediator --bind-port <port> --audit-log <path> [--bind-host <host>] [--allow <host>]...")
		}
		mctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return egress.Run(mctx, egress.Options{BindHost: bindHost, BindPort: bindPort, AuditLogPath: auditLog, Allow: []string(allow), CACertPath: caCert, CAKeyPath: caKey, Passthrough: []string(passthrough), Ready: stdout})
	}
	req, err := readRequest(args)
	if err != nil {
		resp := vmkit.Response{OK: false, Backend: vmkit.BackendFirecracker, Error: err.Error()}
		_ = writeResponse(stdout, resp)
		return err
	}
	resp, err := firecrackersupervisor.Supervisor{}.Do(ctx, req)
	if writeErr := writeResponse(stdout, resp); writeErr != nil && err == nil {
		return writeErr
	}
	return err
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
