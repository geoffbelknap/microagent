package vmkit

import (
	"strings"
	"testing"
)

func TestNormalizeEgressPolicyModeDefaults(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "guarded"},
		{"guarded", "guarded"},
		{"  MEDIATED ", "mediated"},
		{"strict", "strict"},
		{"off", "off"},
		{"bogus", "guarded"},
	}
	for _, tc := range cases {
		p := NormalizeEgressPolicy(EgressPolicy{Mode: tc.in})
		if p.Mode != tc.want {
			t.Errorf("NormalizeEgressPolicy(%q).Mode = %q, want %q", tc.in, p.Mode, tc.want)
		}
	}
}

func TestNormalizeEgressPolicyCleansLists(t *testing.T) {
	in := EgressPolicy{
		Allow:       []string{"  api.example.com ", "", "api.example.com", "x.io"},
		Passthrough: []string{"  api.example.com ", "", "api.example.com", "x.io"},
		DNS:         []string{"  api.example.com ", "", "api.example.com", "x.io"},
	}
	want := []string{"api.example.com", "x.io"}

	p := NormalizeEgressPolicy(in)

	check := func(name string, got []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s[%d]: got %q, want %q", name, i, got[i], want[i])
			}
		}
	}

	check("Allow", p.Allow)
	check("Passthrough", p.Passthrough)
	check("DNS", p.DNS)
}

func TestEgressPolicyValidateOK(t *testing.T) {
	p := NormalizeEgressPolicy(EgressPolicy{
		Mode:  "mediated",
		Allow: []string{"api.example.com"},
		Caps: EgressCaps{
			MaxBytesPerSec:     1024,
			MaxTotalBytes:      1024 * 1024,
			MaxConcurrentConns: 10,
			AuditMaxBytes:      512,
			AuditMaxBackups:    3,
		},
	})
	if err := p.Validate(); err != nil {
		t.Errorf("Validate() returned unexpected error: %v", err)
	}
}

func TestEgressPolicyValidateRejectsNegativeCap(t *testing.T) {
	p := NormalizeEgressPolicy(EgressPolicy{
		Mode: "mediated",
		Caps: EgressCaps{
			MaxTotalBytes: -1,
		},
	})
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want error for negative cap")
	}
	if !strings.Contains(err.Error(), "MaxTotalBytes") {
		t.Errorf("error %q does not mention offending cap MaxTotalBytes", err.Error())
	}
}

func TestEgressPolicyValidateRejectsBadMode(t *testing.T) {
	p := EgressPolicy{Mode: "weird"}
	err := p.Validate()
	if err == nil {
		t.Fatal("Validate() returned nil, want error for unknown mode")
	}
}

func TestEgressPolicyValidateForNetworkMode(t *testing.T) {
	cases := []struct {
		mode        string
		networkMode string
		wantErr     bool
	}{
		{"guarded", "bridged", true},
		{"guarded", "isolated", false},
		{"guarded", "user", false},
		{"guarded", "", false},
		{"mediated", "bridged", true},
		{"mediated", "isolated", false},
		{"mediated", "user", false},
		{"mediated", "", false},
		{"strict", "nat", false},
		{"off", "bridged", false},
		{"mediated", "Isolated", false}, // case-insensitive isolated check (defense-in-depth)
	}
	for _, tc := range cases {
		p := EgressPolicy{Mode: tc.mode}
		err := p.ValidateForNetworkMode(tc.networkMode)
		if tc.wantErr && err == nil {
			t.Errorf("ValidateForNetworkMode(mode=%q, network=%q) = nil, want error", tc.mode, tc.networkMode)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("ValidateForNetworkMode(mode=%q, network=%q) = %v, want nil", tc.mode, tc.networkMode, err)
		}
	}
}
