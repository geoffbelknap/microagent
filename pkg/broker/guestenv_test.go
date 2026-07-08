package broker

import (
	"sort"
	"strings"
	"testing"
)

func TestGuestEnv(t *testing.T) {
	cfg := GuestConfig{
		GuestListen: "127.0.0.1:8888",
		VsockPort:   9000,
		Proxy:       true,
		BaseURL:     map[string]string{"ANTHROPIC_BASE_URL": ""},
	}
	env, err := cfg.GuestEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"MICROAGENT_VSOCK_TCP_LISTENERS": "127.0.0.1:8888=9000",
		"HTTPS_PROXY":                    "http://127.0.0.1:8888",
		"HTTP_PROXY":                     "http://127.0.0.1:8888",
		"ANTHROPIC_BASE_URL":             "http://127.0.0.1:8888",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%q] = %q, want %q", k, env[k], v)
		}
	}
	if len(env) != len(want) {
		t.Errorf("env has %d keys, want %d: %v", len(env), len(want), env)
	}
	// The vsock listener format must match the guestinit parser exactly.
	if !strings.Contains(env["MICROAGENT_VSOCK_TCP_LISTENERS"], "=9000") {
		t.Error("vsock listener env malformed")
	}
	// A verbatim base URL (with a path) is preserved, not overwritten.
	cfg.BaseURL = map[string]string{"OPENAI_BASE_URL": "http://127.0.0.1:8888/v1"}
	env, _ = cfg.GuestEnv()
	if env["OPENAI_BASE_URL"] != "http://127.0.0.1:8888/v1" {
		t.Errorf("verbatim base URL not preserved: %q", env["OPENAI_BASE_URL"])
	}
}

func TestGuestEnvNoProxyKeepsMinimal(t *testing.T) {
	env, err := GuestConfig{GuestListen: "127.0.0.1:7000", VsockPort: 42}.GuestEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(env) != 1 || env["MICROAGENT_VSOCK_TCP_LISTENERS"] != "127.0.0.1:7000=42" {
		t.Fatalf("no-proxy env = %v", env)
	}
}

func TestGuestEnvValidation(t *testing.T) {
	if _, err := (GuestConfig{VsockPort: 1}).GuestEnv(); err == nil {
		t.Error("empty GuestListen must error")
	}
	if _, err := (GuestConfig{GuestListen: "x:1"}).GuestEnv(); err == nil {
		t.Error("zero VsockPort must error")
	}
	if _, err := (GuestConfig{GuestListen: "x:1", VsockPort: 1, BaseURL: map[string]string{"": "y"}}).GuestEnv(); err == nil {
		t.Error("empty BaseURL key must error")
	}
}

// TestMergeGuestEnvOverridesOwnedKeys: a workload cannot pre-set a bridge or
// base URL to escape mediation — broker-owned keys are always replaced.
func TestMergeGuestEnvOverridesOwnedKeys(t *testing.T) {
	cfg := GuestConfig{GuestListen: "127.0.0.1:8888", VsockPort: 9000, Proxy: true, BaseURL: map[string]string{"ANTHROPIC_BASE_URL": ""}}
	existing := []string{
		"PATH=/usr/bin",
		"HTTPS_PROXY=http://evil:1", // attempt to escape
		"ANTHROPIC_BASE_URL=http://evil:2",
		"MICROAGENT_VSOCK_TCP_LISTENERS=127.0.0.1:1=1",
	}
	merged, err := cfg.MergeGuestEnv(existing)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, kv := range merged {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Fatalf("duplicate key %q in merged env: %v", k, merged)
		}
		got[k] = v
	}
	if got["PATH"] != "/usr/bin" {
		t.Error("unrelated env not preserved")
	}
	for k, want := range map[string]string{
		"HTTPS_PROXY":        "http://127.0.0.1:8888",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:8888",
		// Existing bridge entries are preserved (they only work if the host
		// serves that vsock port anyway); the broker's entry is appended.
		"MICROAGENT_VSOCK_TCP_LISTENERS": "127.0.0.1:1=1,127.0.0.1:8888=9000",
	} {
		if got[k] != want {
			t.Errorf("broker-owned %q = %q, want %q (workload override not stripped)", k, got[k], want)
		}
	}
	if !sort.StringsAreSorted(merged) {
		t.Error("merged env not sorted")
	}
}

func TestMergeGuestEnvMap(t *testing.T) {
	cfg := GuestConfig{GuestListen: "127.0.0.1:8888", VsockPort: 9000, Proxy: true, BaseURL: map[string]string{"ANTHROPIC_BASE_URL": ""}}
	existing := map[string]string{
		"PATH":                           "/usr/bin",
		"HTTPS_PROXY":                    "http://evil:1",
		"MICROAGENT_VSOCK_TCP_LISTENERS": "127.0.0.1:1=1,127.0.0.1:8888=9000",
	}
	merged, err := cfg.MergeGuestEnvMap(existing)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PATH":               "/usr/bin",
		"HTTPS_PROXY":        "http://127.0.0.1:8888",
		"HTTP_PROXY":         "http://127.0.0.1:8888",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:8888",
		// The broker's own entry was already present: merged, not duplicated.
		"MICROAGENT_VSOCK_TCP_LISTENERS": "127.0.0.1:1=1,127.0.0.1:8888=9000",
	}
	for k, v := range want {
		if merged[k] != v {
			t.Errorf("merged[%q] = %q, want %q", k, merged[k], v)
		}
	}
	if len(merged) != len(want) {
		t.Errorf("merged has %d keys, want %d: %v", len(merged), len(want), merged)
	}
	if existing["HTTPS_PROXY"] != "http://evil:1" {
		t.Error("input map mutated")
	}
	// Nil input works and yields exactly the broker env.
	solo, err := cfg.MergeGuestEnvMap(nil)
	if err != nil {
		t.Fatal(err)
	}
	if solo["MICROAGENT_VSOCK_TCP_LISTENERS"] != "127.0.0.1:8888=9000" {
		t.Errorf("nil-merge listeners = %q", solo["MICROAGENT_VSOCK_TCP_LISTENERS"])
	}
	// Invalid config fails closed.
	if _, err := (GuestConfig{}).MergeGuestEnvMap(nil); err == nil {
		t.Error("invalid GuestConfig must error")
	}
}
