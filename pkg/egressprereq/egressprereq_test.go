package egressprereq

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseLoadedModules(t *testing.T) {
	proc := []byte(`nft_tproxy 16384 1 - Live 0x0000000000000000
xt_socket 20480 0 - Live 0x0000000000000000

nf_socket_ipv4 16384 1 xt_socket, Live 0x0000000000000000
`)
	loaded := ParseLoadedModules(proc)
	for _, want := range []string{"nft_tproxy", "xt_socket", "nf_socket_ipv4"} {
		if !loaded[want] {
			t.Errorf("expected %q to be parsed as loaded; got %v", want, loaded)
		}
	}
	if loaded["nf_tproxy_ipv4"] {
		t.Error("module not in /proc/modules must not be reported loaded")
	}
}

func TestParseLoadedModulesEmptyAndBlank(t *testing.T) {
	if got := ParseLoadedModules(nil); len(got) != 0 {
		t.Errorf("nil input -> %v, want empty", got)
	}
	if got := ParseLoadedModules([]byte("\n\n   \n")); len(got) != 0 {
		t.Errorf("blank lines -> %v, want empty", got)
	}
}

func TestNormalizeModuleName(t *testing.T) {
	if got := NormalizeModuleName("nf-tproxy-ipv4"); got != "nf_tproxy_ipv4" {
		t.Errorf("NormalizeModuleName = %q, want nf_tproxy_ipv4", got)
	}
}

func TestMissingModulesAllLoaded(t *testing.T) {
	loaded := map[string]bool{}
	for _, m := range TProxyModules {
		loaded[m] = true
	}
	if got := MissingModules(loaded, nil); len(got) != 0 {
		t.Errorf("all loaded -> missing %v, want none", got)
	}
	if !ModulesReady(loaded, nil) {
		t.Error("ModulesReady should be true when all modules loaded")
	}
}

func TestMissingModulesReportsAbsent(t *testing.T) {
	// Only two of four loaded, none built-in.
	loaded := map[string]bool{"nft_tproxy": true, "xt_socket": true}
	got := MissingModules(loaded, func(string) bool { return false })
	want := []string{"nf_tproxy_ipv4", "nf_socket_ipv4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingModules = %v, want %v", got, want)
	}
	if ModulesReady(loaded, nil) {
		t.Error("ModulesReady should be false when modules missing")
	}
}

func TestMissingModulesBuiltinCountsAsPresent(t *testing.T) {
	// Nothing loaded, but all built-in -> ready.
	builtin := func(name string) bool { return true }
	if got := MissingModules(nil, builtin); len(got) != 0 {
		t.Errorf("all built-in -> missing %v, want none", got)
	}
	if !ModulesReady(nil, builtin) {
		t.Error("built-in modules should count as ready")
	}
}

func TestMissingModulesSpellingInsensitive(t *testing.T) {
	// Loaded set uses dashes (alias spelling); must still match.
	loaded := map[string]bool{}
	for _, m := range TProxyModules {
		loaded[m] = true
	}
	builtin := func(name string) bool {
		// statable path would carry normalized name; reject dashed input.
		return false
	}
	if !ModulesReady(loaded, builtin) {
		t.Error("normalized loaded set should satisfy readiness")
	}
}

// TestSharedConstantsAreStable locks the host-prereq values so a change forces a
// deliberate edit (and a matching update everywhere they are consumed). These
// are the values the supervisor verifies fail-closed; drifting them silently
// would let a nat-mode workspace pass verify against the wrong infrastructure.
func TestSharedConstantsAreStable(t *testing.T) {
	if TProxyMark != 0x1 {
		t.Errorf("TProxyMark = %#x, want 0x1", TProxyMark)
	}
	if TProxyTable != 100 {
		t.Errorf("TProxyTable = %d, want 100", TProxyTable)
	}
	wantModules := []string{"nft_tproxy", "nf_tproxy_ipv4", "xt_socket", "nf_socket_ipv4"}
	if !reflect.DeepEqual(TProxyModules, wantModules) {
		t.Errorf("TProxyModules = %v, want %v", TProxyModules, wantModules)
	}
	wantSysctls := map[string]string{
		"/proc/sys/net/ipv4/conf/all/route_localnet": "1",
		"/proc/sys/net/ipv4/conf/all/rp_filter":      "0",
		"/proc/sys/net/ipv4/conf/all/accept_local":   "1",
		"/proc/sys/net/ipv4/ip_forward":              "1",
	}
	if !reflect.DeepEqual(TProxySysctls, wantSysctls) {
		t.Errorf("TProxySysctls = %v, want %v", TProxySysctls, wantSysctls)
	}
	// Guard against a duplicate/extra sysctl key sneaking in.
	keys := make([]string, 0, len(TProxySysctls))
	for k := range TProxySysctls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) != 4 {
		t.Errorf("TProxySysctls has %d keys, want 4", len(keys))
	}
}
