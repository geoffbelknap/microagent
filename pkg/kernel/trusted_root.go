package kernel

import _ "embed"

// trustedRoot is the TUF root metadata baked into the binary — the trust anchor
// for the signed kernel manifest. It is rotated by the supply side re-delegating
// from the cold root key; updating it here ships a new anchor.
//
//go:embed trusted_root.json
var trustedRoot []byte

// DefaultSource returns the production manifest source: the embedded TUF root as
// the trust anchor plus the canonical metadata/targets URLs on the permanent
// distribution.
func DefaultSource() ManifestSource {
	return ManifestSource{
		TrustedRoot: trustedRoot,
		MetadataURL: DefaultMetadataURL,
		TargetsURL:  DefaultTargetsURL,
	}
}
