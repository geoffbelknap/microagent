package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"time"
)

// Options configures the mediator listener.
type Options struct {
	BindHost     string
	BindPort     int
	Allow        []string
	AuditLogPath string
	Logger       Logger                                  // optional; if nil and AuditLogPath set, a FileLogger is opened
	OrigDst      func(net.Conn) (netip.AddrPort, error) // optional; defaults to DefaultOrigDst
	Ready        io.Writer                               // optional; bound address written here once listening
	SniffTimeout time.Duration                           // optional; passed to Handler (Handler defaults to 2s when <=0)
	CACertPath   string                                  // if set with CAKeyPath, enables TLS interception
	CAKeyPath    string
	Passthrough  []string // allowed hosts that are NOT intercepted (L4 splice + audit)
}

// Run binds BindHost:BindPort and serves until ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	ln, err := net.Listen("tcp", net.JoinHostPort(opts.BindHost, fmt.Sprintf("%d", opts.BindPort)))
	if err != nil {
		return fmt.Errorf("egress: listen: %w", err)
	}
	return Serve(ctx, ln, opts)
}

// Serve services connections on ln until ctx is cancelled, closing ln on return.
func Serve(ctx context.Context, ln net.Listener, opts Options) error {
	policy, err := NewPolicy(opts.Allow)
	if err != nil {
		_ = ln.Close()
		return err
	}
	logger := opts.Logger
	if logger == nil {
		if opts.AuditLogPath == "" {
			_ = ln.Close()
			return fmt.Errorf("egress: a logger or audit log path is required")
		}
		fl, err := NewFileLogger(opts.AuditLogPath)
		if err != nil {
			_ = ln.Close()
			return err
		}
		defer fl.Close()
		logger = fl
	}
	orig := opts.OrigDst
	if orig == nil {
		orig = DefaultOrigDst
	}
	if (opts.CACertPath == "") != (opts.CAKeyPath == "") {
		_ = ln.Close()
		return fmt.Errorf("egress: CACertPath and CAKeyPath must be set together")
	}
	var ca *CA
	if opts.CACertPath != "" && opts.CAKeyPath != "" {
		certPEM, rerr := os.ReadFile(opts.CACertPath)
		if rerr != nil {
			_ = ln.Close()
			return fmt.Errorf("egress: read CA cert: %w", rerr)
		}
		keyPEM, rerr := os.ReadFile(opts.CAKeyPath)
		if rerr != nil {
			_ = ln.Close()
			return fmt.Errorf("egress: read CA key: %w", rerr)
		}
		ca, rerr = LoadCA(certPEM, keyPEM)
		if rerr != nil {
			_ = ln.Close()
			return rerr
		}
	}
	var passthrough *Policy
	if len(opts.Passthrough) > 0 {
		passthrough, err = NewPolicy(opts.Passthrough)
		if err != nil {
			_ = ln.Close()
			return err
		}
	}
	h := &Handler{Policy: policy, Logger: logger, OrigDst: orig, Dial: net.Dial, CA: ca, Passthrough: passthrough, SniffTimeout: opts.SniffTimeout}
	logger.Log("egress_listen", map[string]any{"addr": ln.Addr().String(), "allow": opts.Allow})
	if opts.Ready != nil {
		fmt.Fprintln(opts.Ready, ln.Addr().String())
	}
	go func() { <-ctx.Done(); _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("egress: accept: %w", err)
		}
		go h.Handle(conn)
	}
}
