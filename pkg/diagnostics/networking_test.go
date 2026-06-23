package diagnostics

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestDeriveNetworkReadiness(t *testing.T) {
	cases := []struct {
		name          string
		passt, userns bool
		wantUser      bool
	}{
		{"nothing", false, false, false},
		{"passt only", true, false, false},
		{"userns only", false, true, false},
		{"passt+userns", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &vmkit.HostSupport{
				UserNetworkingAvailable: c.passt,
				UserNamespacesAvailable: c.userns,
			}
			deriveNetworkReadiness(h)
			if !h.IsolatedNetworkReady {
				t.Error("isolated must always be ready")
			}
			if h.UserNetworkReady != c.wantUser {
				t.Errorf("user ready = %v, want %v", h.UserNetworkReady, c.wantUser)
			}
		})
	}
}
