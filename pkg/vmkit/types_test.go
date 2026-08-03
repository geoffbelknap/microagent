package vmkit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSafeSnapshotTag(t *testing.T) {
	for _, tag := range []string{"base", "pre-upgrade", "snap_1.2", strings.Repeat("a", 63)} {
		if !SafeSnapshotTag(tag) {
			t.Errorf("SafeSnapshotTag(%q) = false, want true", tag)
		}
	}
	for _, tag := range []string{"", ".", "..", "../victim", `..\victim`, "/absolute", "has space", "-leading", strings.Repeat("a", 64)} {
		if SafeSnapshotTag(tag) {
			t.Errorf("SafeSnapshotTag(%q) = true, want false", tag)
		}
	}
}

func TestValidateLifecycleAuditRejectsUnboundedOrFalseAssurance(t *testing.T) {
	audit := &LifecycleAudit{
		Initiator:    CallerAttribution{Channel: "mcp", Assurance: "verified"},
		WorkInFlight: WorkInFlight{CaptureStatus: "captured"},
		Notification: NotificationRecord{Status: "not_performed", Owner: "caller"},
	}
	if err := ValidateLifecycleAudit(audit); err == nil || !strings.Contains(err.Error(), "assurance") {
		t.Fatalf("false assurance err = %v", err)
	}
	audit.Initiator.Assurance = "caller_asserted"
	audit.WorkInFlight.GuestReported = make([]GuestProcess, MaxLifecycleProcesses+1)
	if err := ValidateLifecycleAudit(audit); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("unbounded process list err = %v", err)
	}
}

func TestValidateRequestRejectsUnsafeSnapshotTagOnLifecycleCommands(t *testing.T) {
	for _, command := range []string{"check", "prepare", "start", "run", "console", "apply"} {
		t.Run(command, func(t *testing.T) {
			req := Request{
				Command: command,
				Identity: &Identity{
					RequestID: "req-1",
					RuntimeID: "agent-1",
					Role:      RoleWorkload,
					Backend:   BackendLinuxKVM,
				},
				Config: &Config{
					KernelPath: "/tmp/kernel",
					RootfsPath: "/tmp/rootfs.ext4",
					StateDir:   "/tmp/state",
				},
				Tag: "../../../planted",
			}
			if err := ValidateRequest(req); err == nil {
				t.Fatalf("ValidateRequest accepted unsafe tag for %s", command)
			}
		})
	}
}

func TestValidateRequestRejectsUnsafeSnapshotCaptureTag(t *testing.T) {
	req := Request{
		Command:  "snapshot",
		Identity: &Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: RoleWorkload, Backend: BackendLinuxKVM},
		Config:   &Config{StateDir: "/tmp/state"},
		Tag:      "../../../planted",
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("ValidateRequest accepted unsafe snapshot capture tag")
	}
}

func TestConfigSecretsControlPortJSONRoundTrip(t *testing.T) {
	in := Config{SecretsControlPort: 1028}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.SecretsControlPort != 1028 {
		t.Fatalf("SecretsControlPort = %d, want 1028", out.SecretsControlPort)
	}
}

func TestConfigOnDemandSecretsJSONRoundTrip(t *testing.T) {
	in := Config{
		OnDemandSecrets: []SecretRef{{Name: "DB", Ref: "vault:secret/data/app#db"}},
		SecretsAudit:    true,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.OnDemandSecrets) != 1 || out.OnDemandSecrets[0].Name != "DB" || !out.SecretsAudit {
		t.Fatalf("on-demand/audit did not round-trip: %+v", out)
	}
}

func TestConfigSecretsJSONRoundTrip(t *testing.T) {
	in := Config{
		SecretsPort:    1026,
		Secrets:        []SecretRef{{Name: "API_KEY", Ref: "vault:secret/data/app#api_key"}},
		SecretEnvFiles: []string{"/etc/app.env"},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.SecretsPort != 1026 || len(out.Secrets) != 1 || out.Secrets[0].Name != "API_KEY" {
		t.Fatalf("secrets did not round-trip: %+v", out)
	}
	if out.Secrets[0].Ref != "vault:secret/data/app#api_key" || len(out.SecretEnvFiles) != 1 {
		t.Fatalf("secret ref/env-files did not round-trip: %+v", out)
	}
}

func TestValidateConfigAppliesDefaults(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryMiB != 512 || cfg.CPUCount != 2 {
		t.Fatalf("defaults = memory %d cpu %d", cfg.MemoryMiB, cfg.CPUCount)
	}
}

func TestValidateRequestAcceptsHaltWithStateDir(t *testing.T) {
	req := Request{
		Command: "halt",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      RoleWorkload,
			Backend:   BackendLinuxKVM,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err != nil {
		t.Fatalf("ValidateRequest rejected halt: %v", err)
	}
}

func TestValidateIdentityBoundsOpaqueCallerContext(t *testing.T) {
	identity := &Identity{RequestID: "req-1", RuntimeID: "agent-1", Role: RoleWorkload, Backend: BackendLinuxKVM}
	identity.Purpose = string(make([]byte, MaxPurposeBytes+1))
	if err := ValidateIdentity(identity); err == nil {
		t.Fatal("oversized purpose accepted")
	}
	identity.Purpose = ""
	identity.CorrelationID = string([]byte{0xff})
	if err := ValidateIdentity(identity); err == nil {
		t.Fatal("invalid UTF-8 correlation ID accepted")
	}
}

func TestValidateRequestAcceptsQuarantineWithStateDir(t *testing.T) {
	req := Request{
		Command: "quarantine",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "agent-1",
			Role:      RoleWorkload,
			Backend:   BackendLinuxKVM,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err != nil {
		t.Fatalf("ValidateRequest rejected quarantine: %v", err)
	}
}

func TestValidateRequestAcceptsPauseAndResumeWithStateDir(t *testing.T) {
	for _, command := range []string{"pause", "resume"} {
		req := Request{
			Command: command,
			Identity: &Identity{
				RequestID: "req-1",
				RuntimeID: "agent-1",
				Role:      RoleWorkload,
				Backend:   BackendLinuxKVM,
			},
			Config: &Config{StateDir: "/tmp/state"},
		}
		if err := ValidateRequest(req); err != nil {
			t.Fatalf("ValidateRequest rejected %s: %v", command, err)
		}
	}
}

func TestValidateRequestRejectsRuntimeIDTraversal(t *testing.T) {
	req := Request{
		Command: "delete",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "../victim",
			Role:      RoleWorkload,
			Backend:   BackendAppleVF,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("ValidateRequest accepted runtimeID traversal")
	}
}

func TestValidateRequestRejectsRuntimeIDPathSeparator(t *testing.T) {
	req := Request{
		Command: "delete",
		Identity: &Identity{
			RequestID: "req-1",
			RuntimeID: "parent/child",
			Role:      RoleWorkload,
			Backend:   BackendAppleVF,
		},
		Config: &Config{StateDir: "/tmp/state"},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("ValidateRequest accepted runtimeID path separator")
	}
}

func TestValidateIdentityRejectsUnsafeSessionLineage(t *testing.T) {
	for _, identity := range []*Identity{
		{RequestID: "req-1", RuntimeID: "agent", SessionID: "../session", Role: RoleWorkload, Backend: BackendLinuxKVM},
		{RequestID: "req-1", RuntimeID: "agent", SessionID: "session-1", SourceSessionID: "parent/session", Role: RoleWorkload, Backend: BackendLinuxKVM},
	} {
		if err := ValidateIdentity(identity); err == nil {
			t.Fatalf("ValidateIdentity(%#v) succeeded", identity)
		}
	}
}

func TestValidateConfigRejectsDuplicateVsockPorts(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		MemoryMiB:  512,
		CPUCount:   2,
		VsockListeners: []VsockListener{
			{Port: 1024, Target: "127.0.0.1:8200"},
			{Port: 1024, Target: "127.0.0.1:8300"},
		},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("expected duplicate port error")
	}
}

// TestValidateConfigHostnameCmdlineSafety: the hostname is placed on the
// space-delimited kernel command line, so a value that could split into
// extra parameters or carry control characters must fail the config closed.
func TestValidateConfigHostnameCmdlineSafety(t *testing.T) {
	base := func() *Config {
		return &Config{
			KernelPath: "/tmp/kernel",
			RootfsPath: "/tmp/rootfs.ext4",
			StateDir:   "/tmp/state",
			MemoryMiB:  512,
			CPUCount:   2,
		}
	}
	ok := base()
	ok.Hostname = "research-vm"
	if err := ValidateConfig(ok); err != nil {
		t.Fatalf("valid hostname rejected: %v", err)
	}
	for _, bad := range []string{"a b", "a\tb", "a\nb", strings.Repeat("a", 254)} {
		cfg := base()
		cfg.Hostname = bad
		if err := ValidateConfig(cfg); err == nil {
			t.Errorf("hostname %q accepted; it could split the kernel command line", bad)
		}
	}
}

func TestValidateConfigAcceptsMediation(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		Mediation: &MediationConfig{
			Enabled:    true,
			Required:   true,
			Port:       2048,
			Target:     "127.0.0.1:9900",
			FailClosed: true,
		},
	}
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("ValidateConfig rejected mediation: %v", err)
	}
}

func TestValidateMediationConfigRejectsIncompleteRequiredChannel(t *testing.T) {
	tests := []MediationConfig{
		{Required: true, Port: 2048, Target: "127.0.0.1:9900", FailClosed: true},
		{Enabled: true, Required: true, Target: "127.0.0.1:9900", FailClosed: true},
		{Enabled: true, Required: true, Port: 2048, FailClosed: true},
		{Enabled: true, Required: true, Port: 2048, Target: "127.0.0.1:9900"},
	}
	for _, mediation := range tests {
		if err := ValidateMediationConfig(mediation); err == nil {
			t.Fatalf("ValidateMediationConfig accepted %#v", mediation)
		}
	}
}

func TestValidateConfigRejectsBadDiskMode(t *testing.T) {
	cfg := &Config{
		KernelPath: "/tmp/kernel",
		RootfsPath: "/tmp/rootfs.ext4",
		StateDir:   "/tmp/state",
		Disks: []Disk{{
			Name:       "constraints",
			Path:       "/tmp/constraints.ext4",
			Mountpoint: "/config",
			Mode:       "writeable",
		}},
	}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("ValidateConfig accepted bad disk mode")
	}
}

func TestValidateNetworkConfigRejectsInvalidMode(t *testing.T) {
	if err := ValidateNetworkConfig(NetworkConfig{Mode: "open"}); err == nil {
		t.Fatal("ValidateNetworkConfig accepted invalid mode")
	}
}

func TestValidateNetworkConfigAcceptsUserMode(t *testing.T) {
	if err := ValidateNetworkConfig(NetworkConfig{Mode: "user"}); err != nil {
		t.Fatalf("ValidateNetworkConfig user: %v", err)
	}
}

func TestValidateNetworkConfigRejectsUnsupportedModes(t *testing.T) {
	for _, mode := range []string{"bridged", "nat", "named"} {
		if err := ValidateNetworkConfig(NetworkConfig{Mode: mode}); err == nil {
			t.Fatalf("ValidateNetworkConfig accepted unsupported mode %q", mode)
		}
	}
}

func TestValidateNetworkConfigRejectsIsolatedPortForwards(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "isolated",
		PortForwards: []PortForward{
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted isolated port forward")
	}
}

func TestValidateNetworkConfigRejectsUnsupportedPortForwardProtocol(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "user",
		PortForwards: []PortForward{
			{Protocol: "udp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted udp port forward")
	}
}

func TestValidateNetworkConfigRejectsDuplicateHostPorts(t *testing.T) {
	cfg := NetworkConfig{
		Mode: "user",
		PortForwards: []PortForward{
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 80},
			{Protocol: "tcp", Host: "127.0.0.1", HostPort: 8080, GuestPort: 8080},
		},
	}
	if err := ValidateNetworkConfig(cfg); err == nil {
		t.Fatal("ValidateNetworkConfig accepted duplicate host ports")
	}
}

func TestValidateEgressMode(t *testing.T) {
	// The final vocabulary: broker (default) / mitm / off. Empty resolves to
	// the broker default; the canonical modes pass through (case/space
	// insensitive).
	valid := map[string]string{
		"":         EgressModeBroker,
		"  ":       EgressModeBroker,
		"broker":   EgressModeBroker,
		"BROKER":   EgressModeBroker,
		" broker ": EgressModeBroker,
		"mitm":     EgressModeMITM,
		"MITM":     EgressModeMITM,
		"off":      EgressModeOff,
		"OFF":      EgressModeOff,
	}
	for in, want := range valid {
		got, err := ValidateEgressMode(in)
		if err != nil {
			t.Errorf("ValidateEgressMode(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ValidateEgressMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateEgressModeRetiredAndUnknown(t *testing.T) {
	// The retired names and any junk are hard errors — NEVER silently
	// reinterpreted (tenet 9). The retired-name errors must name a successor
	// so an operator knows what to do.
	for _, retired := range []string{"guarded", "GUARDED", " strict ", "strict"} {
		_, err := ValidateEgressMode(retired)
		if err == nil {
			t.Errorf("ValidateEgressMode(%q) = nil error, want a retirement error", retired)
			continue
		}
		if !strings.Contains(err.Error(), "broker") {
			t.Errorf("ValidateEgressMode(%q) error %q must name the broker successor", retired, err)
		}
	}
	for _, junk := range []string{"open", "disabled", "brokerish", "guardedx"} {
		if _, err := ValidateEgressMode(junk); err == nil {
			t.Errorf("ValidateEgressMode(%q) = nil error, want an unknown-mode error", junk)
		}
	}
}

func TestEgressMediationOn(t *testing.T) {
	// The mediating modes both provision the mediator.
	on := []string{"broker", "BROKER", " broker ", "mitm", " mitm "}
	for _, m := range on {
		if !EgressMediationOn(m) {
			t.Errorf("EgressMediationOn(%q) = false, want true", m)
		}
	}
	// Empty/whitespace is OFF here (no provisioning) — it is the raw
	// primitive's state; high-level callers resolve the default via
	// ValidateEgressMode first. "off" and any retired/unknown value are OFF.
	off := []string{"", "  ", "off", "OFF", " off ", "guarded", "strict"}
	for _, m := range off {
		if EgressMediationOn(m) {
			t.Errorf("EgressMediationOn(%q) = true, want false", m)
		}
	}
}

func TestEgressModeForgesCertsOnlyMITM(t *testing.T) {
	// mitm inherits the cert-forging datapath; broker splices (no CA); off
	// runs no mediator. The retired names forge nothing (they are errors now).
	if !EgressModeForgesCerts(EgressModeMITM) {
		t.Errorf("EgressModeForgesCerts(mitm) = false, want true")
	}
	for _, m := range []string{EgressModeBroker, EgressModeOff, "guarded", "strict", ""} {
		if EgressModeForgesCerts(m) {
			t.Errorf("EgressModeForgesCerts(%q) = true, want false", m)
		}
	}
}

func TestNetworkModeMediates(t *testing.T) {
	// Modes that route guest egress through the mediator (plus the empty default,
	// which resolves to "user").
	mediates := []string{"", "  ", "user", "USER"}
	for _, m := range mediates {
		if !NetworkModeMediates(m) {
			t.Errorf("NetworkModeMediates(%q) = false, want true", m)
		}
	}
	// "isolated" (no egress) never runs a mediator, so it must NOT be considered
	// mediatable.
	notMediates := []string{"isolated", "ISOLATED", " isolated "}
	for _, m := range notMediates {
		if NetworkModeMediates(m) {
			t.Errorf("NetworkModeMediates(%q) = true, want false", m)
		}
	}
}
