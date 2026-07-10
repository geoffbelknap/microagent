package vmkit

import (
	"encoding/json"
	"testing"
)

func TestNegotiateEgressCapture(t *testing.T) {
	tests := []struct {
		name          string
		backend       string
		networkMode   string
		egressMode    string
		wantProvider  string
		wantStatus    EgressProviderStatus
		wantCoverage  EgressCoverageStatus
		wantUncovered bool // any class can reach the network unmediated
		wantMediates  bool // at least one class is mediated (CA listener meaningful)
	}{
		{
			name:    "linux-kvm broker user is supported+complete",
			backend: BackendLinuxKVM, networkMode: "user", egressMode: "broker",
			wantProvider: EgressProviderLinuxNetfilter, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageComplete, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "linux-kvm mitm user is supported+complete",
			backend: BackendLinuxKVM, networkMode: "user", egressMode: "mitm",
			wantProvider: EgressProviderLinuxNetfilter, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageComplete, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "linux-kvm empty mode defaults to broker+mediates",
			backend: BackendLinuxKVM, networkMode: "", egressMode: "",
			wantProvider: EgressProviderLinuxNetfilter, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageComplete, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "windows-hyperv broker is experimental+constrained (udp dropped)",
			backend: BackendWindowsHyperV, networkMode: "user", egressMode: "broker",
			wantProvider: EgressProviderHyperVGuestShim, wantStatus: EgressProviderExperimental,
			wantCoverage: EgressCoverageConstrained, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "apple-vf broker user is supported+complete",
			backend: BackendAppleVF, networkMode: "user", egressMode: "broker",
			wantProvider: EgressProviderAppleVFHostFD, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageComplete, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "apple-vf mitm user is supported+complete",
			backend: BackendAppleVF, networkMode: "user", egressMode: "mitm",
			wantProvider: EgressProviderAppleVFHostFD, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageComplete, wantUncovered: false, wantMediates: true,
		},
		{
			name:    "egress off: no provider, no uncovered class, no mediation (any backend)",
			backend: BackendAppleVF, networkMode: "user", egressMode: "off",
			wantProvider: EgressProviderNone, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageOff, wantUncovered: false, wantMediates: false,
		},
		{
			name:    "isolated network: mediation is a no-op no-egress state, not uncovered",
			backend: BackendLinuxKVM, networkMode: "isolated", egressMode: "broker",
			wantProvider: EgressProviderNone, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageOff, wantUncovered: false, wantMediates: false,
		},
		{
			name:    "isolated on apple-vf is also a clean no-op (not uncovered)",
			backend: BackendAppleVF, networkMode: "isolated", egressMode: "broker",
			wantProvider: EgressProviderNone, wantStatus: EgressProviderSupported,
			wantCoverage: EgressCoverageOff, wantUncovered: false, wantMediates: false,
		},
		{
			name:    "unknown backend fails closed: unsupported+uncovered",
			backend: "made-up", networkMode: "user", egressMode: "broker",
			wantProvider: EgressProviderNone, wantStatus: EgressProviderUnsupported,
			wantCoverage: EgressCoverageUnsupported, wantUncovered: true, wantMediates: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NegotiateEgressCapture(tc.backend, tc.networkMode, tc.egressMode)
			if r.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", r.Provider, tc.wantProvider)
			}
			if r.ProviderStatus != tc.wantStatus {
				t.Errorf("ProviderStatus = %q, want %q", r.ProviderStatus, tc.wantStatus)
			}
			if r.CoverageStatus != tc.wantCoverage {
				t.Errorf("CoverageStatus = %q, want %q", r.CoverageStatus, tc.wantCoverage)
			}
			if got := r.HasUncoveredClass(); got != tc.wantUncovered {
				t.Errorf("HasUncoveredClass = %v, want %v (coverage %+v)", got, tc.wantUncovered, r.Coverage)
			}
			if got := r.MediatesAnyClass(); got != tc.wantMediates {
				t.Errorf("MediatesAnyClass = %v, want %v", got, tc.wantMediates)
			}
			// Mode is always normalized to a canonical value.
			switch r.Mode {
			case EgressModeBroker, EgressModeMITM, EgressModeOff:
			default:
				t.Errorf("Mode = %q, want a canonical egress mode", r.Mode)
			}
		})
	}
}

// TestEgressCaptureReportJSONStable locks the wire field names that AX/MCP/state
// consumers depend on.
func TestEgressCaptureReportJSONStable(t *testing.T) {
	r := NegotiateEgressCapture(BackendLinuxKVM, "user", "broker")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"mode", "provider", "providerStatus", "coverageStatus", "enforcementBoundary", "guestRole", "coverage", "originalDestination", "bypassResistance"} {
		if _, ok := m[key]; !ok {
			t.Errorf("EgressCaptureReport JSON missing %q (got %s)", key, b)
		}
	}
	var cov map[string]string
	if err := json.Unmarshal(m["coverage"], &cov); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tcp", "dns", "udp", "ipv6", "nonTcpUdpIPv4"} {
		if _, ok := cov[key]; !ok {
			t.Errorf("coverage JSON missing %q", key)
		}
	}
}
