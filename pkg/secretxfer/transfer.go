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

// FetchOne requests a single on-demand secret by name and returns its value.
func FetchOne(rw io.ReadWriter, name string) ([]byte, error) {
	if err := EncodeMessage(rw, Request{ProtocolVersion: ProtocolVersion, Name: name}); err != nil {
		return nil, fmt.Errorf("send secret get request: %w", err)
	}
	var resp GetResponse
	if err := DecodeMessage(rw, &resp); err != nil {
		return nil, fmt.Errorf("read secret get response: %w", err)
	}
	if resp.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("unsupported secrets protocol %q", resp.ProtocolVersion)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("secret %q: %s", name, resp.Error)
	}
	return resp.Value, nil
}
