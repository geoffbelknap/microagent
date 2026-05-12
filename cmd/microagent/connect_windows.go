//go:build windows

package main

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

func dialWindowsHyperVShell(ctx context.Context, runtimeID string, port uint32) (net.Conn, error) {
	vmID, err := guid.FromString(runtimeID)
	if err != nil {
		return nil, fmt.Errorf("parse windows-hyperv runtime ID %q: %w", runtimeID, err)
	}
	return winio.Dial(ctx, &winio.HvsockAddr{
		VMID:      vmID,
		ServiceID: winio.VsockServiceID(port),
	})
}
