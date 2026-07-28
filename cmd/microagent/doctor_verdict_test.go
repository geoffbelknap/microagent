package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/diagnostics"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// coreHost is a host whose boot path is fully present.
func coreHost() *vmkit.HostSupport {
	return &vmkit.HostSupport{
		Backend:                 vmkit.BackendLinuxKVM,
		Architecture:            "amd64",
		SupervisorAvailable:     true,
		VirtualizationSupported: true,
		KVMAvailable:            true,
		GuestInitAvailable:      true,
		BinaryPath:              "/opt/libexec/firecracker",
	}
}

func presentKernel() *vmkit.KernelSupport { return &vmkit.KernelSupport{Status: "present"} }

// TestDoctorVerdictSplitsDegradedFromFailed pins the three-way rollup. A host
// missing one optional prerequisite reported the same "failed" as a host that
// cannot boot a microVM at all, and a word that means both trains operators
// to ignore it.
func TestDoctorVerdictSplitsDegradedFromFailed(t *testing.T) {
	tests := []struct {
		name string
		resp vmkit.Response
		want string
	}{
		{
			name: "everything passed",
			resp: vmkit.Response{OK: true, Host: coreHost(), Kernel: presentKernel()},
			want: "ok",
		},
		{
			name: "core boots, pasta missing: degraded",
			resp: vmkit.Response{OK: false, Error: "pasta is not installed", Host: coreHost(), Kernel: presentKernel()},
			want: "degraded",
		},
		{
			name: "firecracker missing: failed",
			resp: vmkit.Response{OK: false, Error: "firecracker binary not found", Kernel: presentKernel(),
				Host: func() *vmkit.HostSupport { h := coreHost(); h.BinaryPath = ""; return h }()},
			want: "failed",
		},
		{
			name: "no KVM: failed",
			resp: vmkit.Response{OK: false, Error: "/dev/kvm is not available", Kernel: presentKernel(),
				Host: func() *vmkit.HostSupport { h := coreHost(); h.KVMAvailable = false; return h }()},
			want: "failed",
		},
		{
			name: "kernel absent: failed even with a perfect host",
			resp: vmkit.Response{OK: false, Error: "no kernel", Host: coreHost(),
				Kernel: &vmkit.KernelSupport{Status: "missing"}},
			want: "failed",
		},
		{
			name: "no host data at all: failed",
			resp: vmkit.Response{OK: false, Error: "probe exploded"},
			want: "failed",
		},
		{
			// The verdict speaks for the full advertised contract: a missing
			// safety capability degrades a green host even though enforcement
			// fails closed.
			name: "ok probes but a safety capability is not ready: degraded",
			resp: vmkit.Response{OK: true, Kernel: presentKernel(),
				Host: func() *vmkit.HostSupport {
					h := coreHost()
					h.Capabilities = []vmkit.CapabilityDiagnostic{{
						Capability: vmkit.FeatureCapabilityEgressMediation,
						Tier:       vmkit.CapabilityTierSafety,
						Declared:   true,
						Ready:      false,
						Missing:    []string{"kernel module xt_socket"},
					}}
					return h
				}()},
			want: "degraded",
		},
		{
			name: "a core capability is not ready: failed",
			resp: vmkit.Response{OK: true, Kernel: presentKernel(),
				Host: func() *vmkit.HostSupport {
					h := coreHost()
					h.Capabilities = []vmkit.CapabilityDiagnostic{{
						Capability: vmkit.FeatureCapabilityStructuredExec,
						Tier:       vmkit.CapabilityTierCore,
						Declared:   true,
						Ready:      false,
						Missing:    []string{"vsock"},
					}}
					return h
				}()},
			want: "failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diagnostics.DeriveVerdict(&tt.resp); got != tt.want {
				t.Errorf("DeriveVerdict = %q, want %q", got, tt.want)
			}
		})
	}
}

// renderDoctor runs the text renderer against a pipe so assertions do not
// depend on the test host's actual health.
func renderDoctor(t *testing.T, resp vmkit.Response) string {
	t.Helper()
	// A pipe is not a terminal, and the renderer would rightly pick JSON.
	t.Setenv("MICROAGENT_OUTPUT", "text")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	if err := writeDoctorResponse(w, resp); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	return <-done
}

// TestDoctorPageGrammar pins the page shape: an identity header that carries
// no checks, one glyphed line per check with failure rendered as presence,
// and a verdict sentence last, gated on the whole page.
func TestDoctorPageGrammar(t *testing.T) {
	h := coreHost()
	h.FrameworkAvailable = true
	h.BinaryVersion = "Firecracker v1.15.1"
	h.VsockAvailable = true
	h.IsolatedNetworkReady = true
	h.UserNetworkReady = true
	h.ConfinementMode = "rootless"
	h.ConfinementActive = true
	h.Capabilities = []vmkit.CapabilityDiagnostic{
		{Capability: vmkit.FeatureCapabilityStructuredExec, Tier: vmkit.CapabilityTierCore, Declared: true, Ready: true},
		{Capability: vmkit.FeatureCapabilityEgressMediation, Tier: vmkit.CapabilityTierSafety, Declared: true, Ready: true},
	}
	resp := vmkit.Response{OK: true, Host: h, Kernel: presentKernel()}
	resp.Verdict = diagnostics.DeriveVerdict(&resp)
	out := renderDoctor(t, resp)

	if !strings.HasPrefix(out, "Host: linux-kvm on amd64\n") {
		t.Errorf("identity header missing or polluted:\n%s", out)
	}
	for _, want := range []string{"virtualization", "vmm", "supervisor", "guest init", "kernel", "vsock", "networking", "confinement", "structured exec", "egress mediation"} {
		if !strings.Contains(out, want) {
			t.Errorf("check line %q missing:\n%s", want, out)
		}
	}
	// The healthy page carries versions, not paths.
	if strings.Contains(out, "/opt/libexec/firecracker") {
		t.Errorf("healthy page leaks a path:\n%s", out)
	}
	if !strings.Contains(out, "Firecracker v1.15.1") {
		t.Errorf("vmm version missing:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "Workspaces will boot and run on this host. Everything this backend advertises is ready.") {
		t.Errorf("verdict sentence not last:\n%s", out)
	}
	if strings.Contains(out, "✗") || strings.Contains(out, "⚠") {
		t.Errorf("healthy page shows failure glyphs:\n%s", out)
	}
}

// TestDoctorFailureIsPresence pins the rule that a broken check prints its
// own line saying so, instead of a phrase going missing from a healthy list.
func TestDoctorFailureIsPresence(t *testing.T) {
	h := coreHost()
	h.BinaryPath = ""
	resp := vmkit.Response{OK: false, Error: "firecracker binary not found", Host: h, Kernel: presentKernel()}
	resp.Verdict = diagnostics.DeriveVerdict(&resp)
	out := renderDoctor(t, resp)

	if !strings.Contains(out, "✗ firecracker binary not found") {
		t.Errorf("missing vmm does not render as a failing check line:\n%s", out)
	}
	if !strings.Contains(out, "Problems: firecracker binary not found") {
		t.Errorf("probe issue text missing:\n%s", out)
	}
	if !strings.Contains(out, "This host cannot boot workspaces.") {
		t.Errorf("failed verdict sentence missing:\n%s", out)
	}
}

// TestDoctorDegradedSentence keeps the rollup honest for a usable host: the
// sentence says runs work today and names what is not ready, and no "Error"
// label overstates a warning.
func TestDoctorDegradedSentence(t *testing.T) {
	h := coreHost()
	h.FrameworkAvailable = true
	h.VsockAvailable = true
	h.IsolatedNetworkReady = true
	h.UserNetworkReady = true
	h.ConfinementActive = true
	h.ConfinementMode = "rootless"
	h.Capabilities = []vmkit.CapabilityDiagnostic{{
		Capability: vmkit.FeatureCapabilityEgressMediation,
		Tier:       vmkit.CapabilityTierSafety,
		Declared:   true,
		Ready:      false,
		Missing:    []string{"kernel module xt_socket", "kernel module nf_socket_ipv4"},
	}}
	resp := vmkit.Response{OK: true, Host: h, Kernel: presentKernel()}
	resp.Verdict = diagnostics.DeriveVerdict(&resp)
	out := renderDoctor(t, resp)

	if !strings.Contains(out, "egress mediation") || !strings.Contains(out, "⚠ missing: kernel module xt_socket, kernel module nf_socket_ipv4") {
		t.Errorf("degraded capability line missing:\n%s", out)
	}
	if !strings.Contains(out, "Workspaces will boot and run on this host, but not everything is ready: egress mediation.") {
		t.Errorf("degraded verdict sentence missing or unspecific:\n%s", out)
	}
	if strings.Contains(out, "Error:") {
		t.Errorf("a usable host reports an Error:\n%s", out)
	}
}
