package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const DefaultMaxResponseBytes = 40 * 1024 * 1024

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type Client struct {
	addr             string
	dialer           Dialer
	maxResponseBytes uint32
}

type UnreachableError struct {
	Addr string
	Err  error
}

func (err UnreachableError) Error() string {
	return fmt.Sprintf("exec service unreachable at %s: %v", err.Addr, err.Err)
}

func (err UnreachableError) Unwrap() error {
	return err.Err
}

type ProtocolError struct {
	Op  string
	Err error
}

func (err ProtocolError) Error() string {
	return fmt.Sprintf("exec protocol %s failed: %v", err.Op, err.Err)
}

func (err ProtocolError) Unwrap() error {
	return err.Err
}

func New(addr string) *Client {
	return &Client{addr: addr, dialer: &net.Dialer{}, maxResponseBytes: DefaultMaxResponseBytes}
}

func (c *Client) WithDialer(dialer Dialer) *Client {
	next := *c
	next.dialer = dialer
	return &next
}

func (c *Client) WithMaxResponseBytes(max uint32) *Client {
	next := *c
	next.maxResponseBytes = max
	return &next
}

func (c *Client) Exec(ctx context.Context, req protocol.ExecRequest) (protocol.ExecResult, error) {
	dialer := c.dialer
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return protocol.ExecResult{}, UnreachableError{Addr: c.addr, Err: err}
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Time{})
	}
	if err := protocol.EncodeMessage(conn, req); err != nil {
		return protocol.ExecResult{}, ProtocolError{Op: "encode request", Err: err}
	}
	var result protocol.ExecResult
	maxBytes := c.maxResponseBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxResponseBytes
	}
	if err := protocol.DecodeMessageWithMax(conn, &result, maxBytes); err != nil {
		return protocol.ExecResult{}, ProtocolError{Op: "decode response", Err: err}
	}
	return result, nil
}
