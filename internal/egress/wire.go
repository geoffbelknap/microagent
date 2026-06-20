package egress

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Wire encoding for DestHeader over a Hyper-V socket stream:
//
//	byte 0:     proto  (0x01 = tcp, 0x02 = udp)
//	byte 1:     host length N  (1–255; 0 is rejected)
//	bytes 2…N+1: host (UTF-8, hostname or IP literal)
//	bytes N+2…N+3: port (uint16, big-endian)
const (
	protoTCP byte = 0x01
	protoUDP byte = 0x02
)

// DestHeader carries the original connection destination that the guest
// forwarder observed before handing the stream to the host mediator over an
// hvsock. Both sides of the hvsock bridge (guest forwarder and host mediator
// front-end) use WriteDestHeader / ReadDestHeader to frame this metadata at
// the start of each proxied stream.
type DestHeader struct {
	Proto string // "tcp" or "udp"
	Host  string // hostname or IP literal (as seen by the guest)
	Port  uint16
}

// WriteDestHeader encodes h into w using the compact wire format described
// above. It returns an error if Proto is not "tcp" or "udp", or if Host
// exceeds 255 bytes (the 1-byte length field limit).
func WriteDestHeader(w io.Writer, h DestHeader) error {
	var protoByte byte
	switch h.Proto {
	case "tcp":
		protoByte = protoTCP
	case "udp":
		protoByte = protoUDP
	default:
		return fmt.Errorf("egress: WriteDestHeader: unknown proto %q (want \"tcp\" or \"udp\")", h.Proto)
	}

	hostBytes := []byte(h.Host)
	if len(hostBytes) > 255 {
		return fmt.Errorf("egress: WriteDestHeader: host %q too long (%d bytes; max 255)", h.Host, len(hostBytes))
	}

	buf := make([]byte, 0, 1+1+len(hostBytes)+2)
	buf = append(buf, protoByte)
	buf = append(buf, byte(len(hostBytes)))
	buf = append(buf, hostBytes...)
	buf = binary.BigEndian.AppendUint16(buf, h.Port)

	_, err := w.Write(buf)
	return err
}

// ReadDestHeader decodes a DestHeader from r. It returns an error if the
// stream is truncated, the proto byte is unknown, or the host length is zero.
func ReadDestHeader(r io.Reader) (DestHeader, error) {
	// Read proto byte and host-length byte together.
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return DestHeader{}, fmt.Errorf("egress: ReadDestHeader: reading header bytes: %w", err)
	}

	var proto string
	switch hdr[0] {
	case protoTCP:
		proto = "tcp"
	case protoUDP:
		proto = "udp"
	default:
		return DestHeader{}, fmt.Errorf("egress: ReadDestHeader: unknown proto byte 0x%02x", hdr[0])
	}

	hostLen := int(hdr[1])
	if hostLen == 0 {
		return DestHeader{}, fmt.Errorf("egress: ReadDestHeader: host length is 0 (empty host not allowed)")
	}

	hostBuf := make([]byte, hostLen)
	if _, err := io.ReadFull(r, hostBuf); err != nil {
		return DestHeader{}, fmt.Errorf("egress: ReadDestHeader: reading host (%d bytes): %w", hostLen, err)
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(r, portBuf[:]); err != nil {
		return DestHeader{}, fmt.Errorf("egress: ReadDestHeader: reading port: %w", err)
	}
	port := binary.BigEndian.Uint16(portBuf[:])

	return DestHeader{Proto: proto, Host: string(hostBuf), Port: port}, nil
}
