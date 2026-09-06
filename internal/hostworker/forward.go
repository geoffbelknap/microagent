package hostworker

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/internal/netlimit"
)

// runForward provides a stable address for an unmediated worker. Unlike an HTTP
// mediator it does not rewrite headers, buffer bodies, or impose request limits.
func runForward(ctx context.Context, opts Options) error {
	if strings.TrimSpace(opts.PolicyURL) != "" || strings.TrimSpace(opts.PolicyFile) != "" {
		return fmt.Errorf("byte forwarding cannot enforce a policy")
	}
	target, err := parseEndpointURL(opts.TargetBaseURL, "target")
	if err != nil {
		return err
	}
	host := opts.BindHost
	if host == "" {
		host = defaultListenHost
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(opts.BindPort)))
	if err != nil {
		return err
	}
	return serveForward(ctx, listener, target.Host, opts.ResolveUpstreamHost, opts.Ready)
}

func serveForward(ctx context.Context, listener net.Listener, target string, resolve func() string, ready io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bounded := netlimit.New(listener, netlimit.DefaultMaxConnections)
	defer func() { _ = bounded.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = bounded.Close() })
	defer stop()
	if ready != nil {
		_, _ = fmt.Fprintf(ready, "ready %s\n", listener.Addr())
	}
	var active sync.WaitGroup
	defer active.Wait()
	for {
		conn, err := bounded.Accept()
		if err != nil {
			// Also close active connections on an unexpected accept failure.
			_ = bounded.Close()
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return err
		}
		active.Add(1)
		go func() {
			defer active.Done()
			forwardConnection(ctx, conn, target, resolve)
		}()
	}
}

func forwardConnection(ctx context.Context, conn net.Conn, target string, resolve func() string) {
	defer func() { _ = conn.Close() }()
	if resolve != nil {
		target = resolve()
		if target == "" {
			// A removed runner must not fall through to a recycled bootstrap port.
			return
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	remote, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer func() { _ = remote.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = remote.Close() })
	defer stop()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		_, _ = io.Copy(remote, conn)
		if tcp, ok := remote.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	_, _ = io.Copy(conn, remote)
	_ = conn.Close()
	_ = remote.Close()
	<-sent
}
