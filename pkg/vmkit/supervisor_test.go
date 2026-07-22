package vmkit

import (
	"os"
	"strings"
	"testing"
)

func TestExecutableSupervisorEnvIncludesAppleVFDatapathBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := executableSupervisorEnv(Request{Identity: &Identity{Backend: BackendAppleVF}})
	if !envContains(env, "MICROAGENT_EGRESS_DATAPATH_BIN", exe) {
		t.Fatalf("MICROAGENT_EGRESS_DATAPATH_BIN not set to current executable in %#v", env)
	}
}

func TestExecutableSupervisorEnvRespectsPresetDatapathBinary(t *testing.T) {
	// Embedders (go test, custom hosts) are not the microagent CLI; a pre-set
	// datapath binary must not be shadowed by a later duplicate entry pointing
	// at os.Executable (the child keeps the last duplicate).
	t.Setenv("MICROAGENT_EGRESS_DATAPATH_BIN", "/opt/custom/microagent")
	env := executableSupervisorEnv(Request{Identity: &Identity{Backend: BackendAppleVF}})
	var entries []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "MICROAGENT_EGRESS_DATAPATH_BIN=") {
			entries = append(entries, entry)
		}
	}
	if len(entries) != 1 || entries[0] != "MICROAGENT_EGRESS_DATAPATH_BIN=/opt/custom/microagent" {
		t.Fatalf("pre-set MICROAGENT_EGRESS_DATAPATH_BIN not preserved as the only entry: %#v", entries)
	}
}

func TestExecutableSupervisorEnvSkipsNonAppleVFDatapathBinary(t *testing.T) {
	env := executableSupervisorEnv(Request{Identity: &Identity{Backend: BackendLinuxKVM}})
	if envHasKey(env, "MICROAGENT_EGRESS_DATAPATH_BIN") {
		t.Fatalf("non-apple-vf env unexpectedly contains MICROAGENT_EGRESS_DATAPATH_BIN: %#v", env)
	}
}

func envContains(env []string, key, value string) bool {
	want := key + "=" + value
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
