//go:build !windows

package main

import (
	"context"
	"fmt"
	"net"
)

func dialWindowsHyperVShell(ctx context.Context, runtimeID string, port uint32) (net.Conn, error) {
	return nil, fmt.Errorf("windows-hyperv connect is only supported on windows")
}
