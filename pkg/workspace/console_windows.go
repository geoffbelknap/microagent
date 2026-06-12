//go:build windows

package workspace

import (
	"context"
	"fmt"
	"net"

	"github.com/Microsoft/go-winio"
	"github.com/Microsoft/go-winio/pkg/guid"
)

// dialWindowsHyperVShell dials the guest shell service over hv_sock. The
// hv_sock transport can hold a connect attempt far past context cancellation
// while the guest service is not yet listening, so the dial runs in its own
// goroutine and the caller's context deadline is enforced regardless.
func dialWindowsHyperVShell(ctx context.Context, runtimeID string, port uint32) (net.Conn, error) {
	vmID, err := guid.FromString(runtimeID)
	if err != nil {
		return nil, fmt.Errorf("parse windows-hyperv runtime ID %q: %w", runtimeID, err)
	}
	type dialResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan dialResult, 1)
	go func() {
		conn, err := winio.Dial(ctx, &winio.HvsockAddr{
			VMID:      vmID,
			ServiceID: winio.VsockServiceID(port),
		})
		resultCh <- dialResult{conn: conn, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.conn, result.err
	case <-ctx.Done():
		// Reap the dial if it ever completes so the connection does not leak.
		go func() {
			if result := <-resultCh; result.err == nil {
				_ = result.conn.Close()
			}
		}()
		return nil, fmt.Errorf("dial windows-hyperv shell hvsock %s:%d: %w", runtimeID, port, ctx.Err())
	}
}
