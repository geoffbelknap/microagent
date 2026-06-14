package diagnostics

import (
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestDeriveNetworkReadiness(t *testing.T) {
	cases := []struct {
		name                          string
		ipForward, cap, passt, userns bool
		wantUser, wantPrivileged      bool
	}{
		{"nothing", false, false, false, false, false, false},
		{"passt only", false, false, true, false, false, false},
		{"userns only", false, false, false, true, false, false},
		{"passt+userns", false, false, true, true, true, false},
		{"forward only", true, false, false, false, false, false},
		{"cap only", false, true, false, false, false, false},
		{"forward+cap", true, true, false, false, false, true},
		{"all", true, true, true, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &vmkit.HostSupport{
				IPForwardEnabled:          c.ipForward,
				SupervisorNetAdminCapable: c.cap,
				UserNetworkingAvailable:   c.passt,
				UserNamespacesAvailable:   c.userns,
			}
			DeriveNetworkReadiness(h)
			if !h.IsolatedNetworkReady {
				t.Error("isolated must always be ready")
			}
			if h.UserNetworkReady != c.wantUser {
				t.Errorf("user ready = %v, want %v", h.UserNetworkReady, c.wantUser)
			}
			if h.PrivilegedNetworkReady != c.wantPrivileged {
				t.Errorf("privileged ready = %v, want %v", h.PrivilegedNetworkReady, c.wantPrivileged)
			}
		})
	}
}

func TestNetworkRemediation(t *testing.T) {
	// Ready: no remediation.
	ready := &vmkit.HostSupport{PrivilegedNetworkReady: true}
	if got := NetworkRemediation(ready); got != "" {
		t.Errorf("ready host should have no remediation, got %q", got)
	}
	// Forwarding on but cap missing -> post-upgrade phrasing.
	upgraded := &vmkit.HostSupport{IPForwardEnabled: true, SupervisorNetAdminCapable: false}
	if got := NetworkRemediation(upgraded); !strings.Contains(got, "CAP_NET_ADMIN") || !strings.Contains(got, "setup-networking") {
		t.Errorf("missing-cap remediation = %q, want CAP_NET_ADMIN + setup-networking hint", got)
	}
	// Nothing set -> generic remediation (no broken sudo prefix).
	none := &vmkit.HostSupport{}
	got := NetworkRemediation(none)
	if !strings.Contains(got, "microagent host setup-networking") || strings.Contains(got, "sudo microagent") {
		t.Errorf("generic remediation = %q, want no-sudo setup-networking command", got)
	}
}
