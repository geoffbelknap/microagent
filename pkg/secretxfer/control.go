package secretxfer

import (
	"fmt"
	"io"
)

// SendControl sends a control op to the guest control agent and waits for its
// ack. w and r may be the same connection, or a connection plus a buffered
// reader (the host path reads through a bufio.Reader after the vsock CONNECT
// handshake).
func SendControl(w io.Writer, r io.Reader, op string) error {
	if err := EncodeMessage(w, ControlRequest{ProtocolVersion: ProtocolVersion, Op: op}); err != nil {
		return fmt.Errorf("send control %q: %w", op, err)
	}
	var resp ControlResponse
	if err := DecodeMessage(r, &resp); err != nil {
		return fmt.Errorf("read control %q ack: %w", op, err)
	}
	if resp.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported secrets protocol %q", resp.ProtocolVersion)
	}
	if !resp.OK {
		return fmt.Errorf("control %q failed: %s", op, resp.Error)
	}
	return nil
}
