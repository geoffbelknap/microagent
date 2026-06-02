package secretxfer

import (
	"fmt"
	"io"
)

// ServeBundle reads the guest's Request from r and writes the Bundle to w. The
// caller supplies the resolved bundle; ServeBundle performs no resolution.
func ServeBundle(w io.Writer, r io.Reader, bundle Bundle) error {
	var req Request
	if err := DecodeMessage(r, &req); err != nil {
		return fmt.Errorf("read secrets request: %w", err)
	}
	if req.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("unsupported secrets protocol %q", req.ProtocolVersion)
	}
	bundle.ProtocolVersion = ProtocolVersion
	return EncodeMessage(w, bundle)
}

// FetchBundle sends a Request and returns the host's Bundle.
func FetchBundle(rw io.ReadWriter) (Bundle, error) {
	if err := EncodeMessage(rw, Request{ProtocolVersion: ProtocolVersion}); err != nil {
		return Bundle{}, fmt.Errorf("send secrets request: %w", err)
	}
	var bundle Bundle
	if err := DecodeMessage(rw, &bundle); err != nil {
		return Bundle{}, fmt.Errorf("read secrets bundle: %w", err)
	}
	if bundle.ProtocolVersion != ProtocolVersion {
		return Bundle{}, fmt.Errorf("unsupported secrets protocol %q", bundle.ProtocolVersion)
	}
	return bundle, nil
}
