// Package secretxfer carries host-resolved secrets to the guest over an
// io.ReadWriter (a vsock connection) using a length-prefixed JSON protocol, and
// materializes them as files in a guest tmpfs. It is dependency-free so the
// minimal guest init binary can import it. Values live only in memory here and
// in the guest tmpfs; secretxfer never writes them to disk on the host.
package secretxfer

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	// ProtocolVersion identifies the wire format. Mirrors the exec.v1 framing.
	ProtocolVersion = "secrets.v1"
	// MaxMessageBytes bounds a single frame. Secret bundles are small; 8 MiB is
	// generous headroom and guards against a runaway length prefix.
	MaxMessageBytes uint32 = 8 * 1024 * 1024
)

// Request is sent by the guest to open a secrets exchange.
type Request struct {
	ProtocolVersion string `json:"protocol_version"`
}

// Entry is one secret. Value is base64-encoded in JSON, preserving binary bytes.
type Entry struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// Bundle is the host's response: every declared secret, resolved.
type Bundle struct {
	ProtocolVersion string  `json:"protocol_version"`
	Secrets         []Entry `json:"secrets"`
}

// EncodeMessage writes a 4-byte big-endian length prefix followed by JSON.
func EncodeMessage(w io.Writer, msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(data) > int(^uint32(0)) {
		return fmt.Errorf("secretxfer message length %d exceeds uint32 prefix", len(data))
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(data)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// DecodeMessage reads a framed JSON message into out using MaxMessageBytes.
func DecodeMessage(r io.Reader, out any) error {
	return DecodeMessageWithMax(r, out, MaxMessageBytes)
}

// DecodeMessageWithMax reads a framed JSON message, rejecting frames larger than
// maxBytes before allocating.
func DecodeMessageWithMax(r io.Reader, out any, maxBytes uint32) error {
	if out == nil {
		return errors.New("secretxfer decode target is nil")
	}
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(prefix[:])
	if length > maxBytes {
		return fmt.Errorf("secretxfer message length %d exceeds maximum %d", length, maxBytes)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
