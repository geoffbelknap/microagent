package main

import (
	"io"
	"os"
	"strings"
	"testing"

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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := doctorVerdict(tt.resp); got != tt.want {
				t.Errorf("doctorVerdict = %q, want %q", got, tt.want)
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

// TestDoctorRootCauseLeads pins the ordering. The blocking error used to
// print last, so the reader met six capability symptoms before the one
// missing binary that explained them.
func TestDoctorRootCauseLeads(t *testing.T) {
	h := coreHost()
	h.BinaryPath = ""
	out := renderDoctor(t, vmkit.Response{OK: false, Error: "firecracker binary not found", Host: h, Kernel: presentKernel()})

	errIdx := strings.Index(out, "Error: firecracker binary not found")
	hostIdx := strings.Index(out, "Host: ")
	if errIdx < 0 {
		t.Fatalf("root cause missing:\n%s", out)
	}
	if hostIdx >= 0 && errIdx > hostIdx {
		t.Errorf("root cause prints below the details it explains:\n%s", out)
	}
	if strings.Count(out, "firecracker binary not found") != 1 {
		t.Errorf("root cause printed more than once:\n%s", out)
	}
}

// TestDoctorDegradedSaysWarnings keeps the label honest: for a host whose
// runs work today, "Error:" overstates and gets ignored tomorrow.
func TestDoctorDegradedSaysWarnings(t *testing.T) {
	out := renderDoctor(t, vmkit.Response{OK: false, Error: "pasta is not installed", Host: coreHost(), Kernel: presentKernel()})

	if !strings.Contains(out, "Status: degraded") {
		t.Errorf("degraded host not labelled degraded:\n%s", out)
	}
	if !strings.Contains(out, "Warnings: pasta is not installed") {
		t.Errorf("degraded issues not labelled Warnings:\n%s", out)
	}
	if strings.Contains(out, "Error:") {
		t.Errorf("a usable host reports an Error:\n%s", out)
	}
}
