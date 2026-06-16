package secretxfer

import (
	"fmt"
	"io"
)

// CACertTarget marks a vsock listener that serves the per-workspace egress CA
// certificate. Must equal the workspace package's sentinel.
const CACertTarget = "cacert://serve"

// MaxCACertBytes is the maximum PEM payload accepted from the host. A typical
// CA certificate PEM is under 2 KiB; 1 MiB provides ample headroom.
const MaxCACertBytes = 1 * 1024 * 1024

// ServeCACert writes certPEM to w and closes. The payload is length-prefixed
// using the same 4-byte big-endian framing as the rest of secretxfer so the
// guest can detect truncation.
func ServeCACert(w io.Writer, certPEM []byte) error {
	if len(certPEM) == 0 {
		return fmt.Errorf("cacert: PEM is empty")
	}
	if uint32(len(certPEM)) > MaxCACertBytes {
		return fmt.Errorf("cacert: PEM length %d exceeds maximum %d", len(certPEM), MaxCACertBytes)
	}
	var prefix [4]byte
	// binary.BigEndian.PutUint32 inline — avoid importing encoding/binary here
	// since EncodeMessage already does this; reuse it by wrapping as raw bytes.
	length := uint32(len(certPEM))
	prefix[0] = byte(length >> 24)
	prefix[1] = byte(length >> 16)
	prefix[2] = byte(length >> 8)
	prefix[3] = byte(length)
	if _, err := w.Write(prefix[:]); err != nil {
		return fmt.Errorf("cacert: write length prefix: %w", err)
	}
	if _, err := w.Write(certPEM); err != nil {
		return fmt.Errorf("cacert: write PEM: %w", err)
	}
	return nil
}

// FetchCACert reads a length-prefixed PEM payload from r and returns it.
func FetchCACert(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("cacert: read length prefix: %w", err)
	}
	length := uint32(prefix[0])<<24 | uint32(prefix[1])<<16 | uint32(prefix[2])<<8 | uint32(prefix[3])
	if length == 0 {
		return nil, fmt.Errorf("cacert: zero-length PEM")
	}
	if length > MaxCACertBytes {
		return nil, fmt.Errorf("cacert: PEM length %d exceeds maximum %d", length, MaxCACertBytes)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("cacert: read PEM: %w", err)
	}
	return buf, nil
}
