package vmkit

// Egress capture provider reporting.
//
// Egress mediation is not an implied property of the network mode — it is a
// backend-specific capture provider that must report exactly what it captures,
// what it drops fail-closed, where enforcement lives, and whether the guest can
// bypass it. This file defines the machine-readable report and a pure
// negotiation function that maps (backend, network mode, egress mode) to the
// provider that would be active. The workspace layer uses the report to gate
// CA-listener allocation and to fail closed when a requested egress posture has
// no acceptable provider, instead of the old backend-blind
// NetworkModeMediates(mode) heuristic.

// Egress capture provider IDs.
const (
	// EgressProviderNone is reported when no capture provider is active
	// (egress off, isolated network, or an unsupported backend).
	EgressProviderNone = "none"
	// EgressProviderLinuxNetfilter is the Firecracker/Linux host-datapath
	// provider: nftables PREROUTING REDIRECT (TCP) + TPROXY (UDP) in the
	// per-VM network namespace.
	EgressProviderLinuxNetfilter = "linux-netfilter-prerouting"
	// EgressProviderHyperVGuestShim is the experimental windows-hyperv
	// provider: guest-local OUTPUT REDIRECT to a shim over hvsock, enforced by
	// a no-uplink topology.
	EgressProviderHyperVGuestShim = "hyperv-guest-shim"
	// EgressProviderAppleVFHostFD is the Apple VF host-datapath provider built
	// on VZFileHandleNetworkDeviceAttachment.
	EgressProviderAppleVFHostFD = "applevf-host-fd-gateway"
)

// EgressProviderStatus is the support tier of a capture provider.
type EgressProviderStatus string

const (
	EgressProviderSupported    EgressProviderStatus = "supported"
	EgressProviderExperimental EgressProviderStatus = "experimental"
	EgressProviderUnsupported  EgressProviderStatus = "unsupported"
)

// EgressCoverageStatus is the coarse rollup of what a provider covers.
type EgressCoverageStatus string

const (
	EgressCoverageComplete    EgressCoverageStatus = "complete"
	EgressCoverageConstrained EgressCoverageStatus = "constrained"
	EgressCoverageOff         EgressCoverageStatus = "off"
	EgressCoverageUnsupported EgressCoverageStatus = "unsupported"
)

// EgressProtocolCoverage is the per-protocol-class behavior of a provider.
type EgressProtocolCoverage string

const (
	// EgressClassMediate routes the class through the mediator.
	EgressClassMediate EgressProtocolCoverage = "mediate"
	// EgressClassDrop blocks the class fail-closed (compatibility loss, not an
	// authority gain).
	EgressClassDrop EgressProtocolCoverage = "drop"
	// EgressClassNotApplicable means the class cannot occur (e.g. no network).
	EgressClassNotApplicable EgressProtocolCoverage = "not-applicable"
	// EgressClassUncovered means the class can reach the network WITHOUT
	// mediation — the dangerous state. Any uncovered class for an enabled
	// egress policy must fail validation.
	EgressClassUncovered EgressProtocolCoverage = "uncovered"
)

// Enforcement boundaries: where bypass resistance comes from.
const (
	EgressEnforcementHostDatapath = "host-datapath"
	EgressEnforcementGuestShim    = "topology-guest-shim"
	EgressEnforcementGatewayVM    = "gateway-vm"
	EgressEnforcementUnmediated   = "unmediated"
)

// Guest roles in the capture path.
const (
	EgressGuestOblivious    = "oblivious"
	EgressGuestShimRequired = "shim-required"
	EgressGuestNone         = "none"
)

// Bypass-resistance descriptors.
const (
	EgressBypassHostEnforced = "host-enforced"
	EgressBypassTamperBreaks = "tamper-breaks-egress-not-bypass"
	EgressBypassNone         = "none"
)

// EgressCoverage is the per-protocol-class coverage of a provider.
type EgressCoverage struct {
	TCP           EgressProtocolCoverage `json:"tcp"`
	DNS           EgressProtocolCoverage `json:"dns"`
	UDP           EgressProtocolCoverage `json:"udp"`
	IPv6          EgressProtocolCoverage `json:"ipv6"`
	NonTCPUDPIPv4 EgressProtocolCoverage `json:"nonTcpUdpIPv4"`
}

// EgressOriginalDestination reports whether the provider preserves the original
// destination for each transport (so policy decides on the real dst, not SNI).
type EgressOriginalDestination struct {
	TCP bool `json:"tcp"`
	UDP bool `json:"udp"`
}

// EgressCaptureReport is the machine-readable record of how a workspace's egress
// is captured. It is surfaced in runtime state and CLI/AX/MCP output.
type EgressCaptureReport struct {
	Mode                string                    `json:"mode"`
	Provider            string                    `json:"provider"`
	ProviderStatus      EgressProviderStatus      `json:"providerStatus"`
	CoverageStatus      EgressCoverageStatus      `json:"coverageStatus"`
	EnforcementBoundary string                    `json:"enforcementBoundary"`
	GuestRole           string                    `json:"guestRole"`
	Coverage            EgressCoverage            `json:"coverage"`
	OriginalDestination EgressOriginalDestination `json:"originalDestination"`
	BypassResistance    string                    `json:"bypassResistance"`
	Limitations         []string                  `json:"limitations,omitempty"`
}

// HasUncoveredClass reports whether any protocol class can reach the network
// unmediated. Such a report must fail validation for an enabled egress policy.
func (r EgressCaptureReport) HasUncoveredClass() bool {
	for _, c := range []EgressProtocolCoverage{r.Coverage.TCP, r.Coverage.DNS, r.Coverage.UDP, r.Coverage.IPv6, r.Coverage.NonTCPUDPIPv4} {
		if c == EgressClassUncovered {
			return true
		}
	}
	return false
}

// MediatesAnyClass reports whether the provider routes at least one class
// through the mediator — i.e. a per-workspace CA listener is meaningful. Used to
// allocate the CA-cert listener only when a real mediator will exist.
func (r EgressCaptureReport) MediatesAnyClass() bool {
	for _, c := range []EgressProtocolCoverage{r.Coverage.TCP, r.Coverage.DNS, r.Coverage.UDP} {
		if c == EgressClassMediate {
			return true
		}
	}
	return false
}

// NegotiateEgressCapture returns the capture provider that would be active for a
// workspace with the given backend, network mode, and egress mode. It is pure
// (no host probing): it reports the provider's intended coverage. Runtime
// prerequisite failures (e.g. missing nft modules) are layered on by the
// supervisor/validation later. egressMode is normalized here, so callers may
// pass the raw flag value.
func NegotiateEgressCapture(backend, networkMode, egressMode string) EgressCaptureReport {
	mode := ResolveEgressModeDefault(egressMode)

	// Egress off: no capture provider, no CA, no mediator.
	if mode == EgressModeOff {
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderNone,
			ProviderStatus:      EgressProviderSupported,
			CoverageStatus:      EgressCoverageOff,
			EnforcementBoundary: EgressEnforcementUnmediated,
			GuestRole:           EgressGuestNone,
			Coverage:            uniformCoverage(EgressClassNotApplicable),
		}
	}

	// Non-mediating network mode (isolated, or anything that does not route
	// guest egress): mediation is a no-op no-egress state, not a running
	// mediator. Valid, but reports off with no network path.
	if !NetworkModeMediates(networkMode) {
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderNone,
			ProviderStatus:      EgressProviderSupported,
			CoverageStatus:      EgressCoverageOff,
			EnforcementBoundary: EgressEnforcementUnmediated,
			GuestRole:           EgressGuestNone,
			Coverage:            uniformCoverage(EgressClassNotApplicable),
		}
	}

	switch backend {
	case BackendLinuxKVM:
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderLinuxNetfilter,
			ProviderStatus:      EgressProviderSupported,
			CoverageStatus:      EgressCoverageComplete,
			EnforcementBoundary: EgressEnforcementHostDatapath,
			GuestRole:           EgressGuestOblivious,
			Coverage: EgressCoverage{
				TCP:           EgressClassMediate,
				DNS:           EgressClassMediate,
				UDP:           EgressClassMediate,
				IPv6:          EgressClassDrop,
				NonTCPUDPIPv4: EgressClassDrop,
			},
			OriginalDestination: EgressOriginalDestination{TCP: true, UDP: true},
			BypassResistance:    EgressBypassHostEnforced,
		}
	case BackendWindowsHyperV:
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderHyperVGuestShim,
			ProviderStatus:      EgressProviderExperimental,
			CoverageStatus:      EgressCoverageConstrained,
			EnforcementBoundary: EgressEnforcementGuestShim,
			GuestRole:           EgressGuestShimRequired,
			Coverage: EgressCoverage{
				TCP:           EgressClassMediate,
				DNS:           EgressClassMediate,
				UDP:           EgressClassDrop,
				IPv6:          EgressClassDrop,
				NonTCPUDPIPv4: EgressClassDrop,
			},
			OriginalDestination: EgressOriginalDestination{TCP: true, UDP: false},
			BypassResistance:    EgressBypassTamperBreaks,
			Limitations: []string{
				"non-DNS UDP is dropped fail-closed on this provider",
				"experimental backend; diagnostic unless explicitly scoped",
			},
		}
	case BackendAppleVF:
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderAppleVFHostFD,
			ProviderStatus:      EgressProviderSupported,
			CoverageStatus:      EgressCoverageComplete,
			EnforcementBoundary: EgressEnforcementHostDatapath,
			GuestRole:           EgressGuestOblivious,
			Coverage: EgressCoverage{
				TCP:           EgressClassMediate,
				DNS:           EgressClassMediate,
				UDP:           EgressClassMediate,
				IPv6:          EgressClassDrop,
				NonTCPUDPIPv4: EgressClassDrop,
			},
			OriginalDestination: EgressOriginalDestination{TCP: true, UDP: true},
			BypassResistance:    EgressBypassHostEnforced,
		}
	default:
		// Unknown backend fails closed: unsupported, everything uncovered.
		return EgressCaptureReport{
			Mode:                mode,
			Provider:            EgressProviderNone,
			ProviderStatus:      EgressProviderUnsupported,
			CoverageStatus:      EgressCoverageUnsupported,
			EnforcementBoundary: EgressEnforcementUnmediated,
			GuestRole:           EgressGuestNone,
			Coverage:            uniformCoverage(EgressClassUncovered),
			BypassResistance:    EgressBypassNone,
			Limitations:         []string{"unknown backend has no egress capture provider"},
		}
	}
}

func uniformCoverage(c EgressProtocolCoverage) EgressCoverage {
	return EgressCoverage{TCP: c, DNS: c, UDP: c, IPv6: c, NonTCPUDPIPv4: c}
}
