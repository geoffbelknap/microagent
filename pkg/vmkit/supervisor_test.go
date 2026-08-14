package vmkit

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecutableSupervisorEnvDoesNotAssumeEmbedderIsDatapathBinary(t *testing.T) {
	t.Setenv(EgressDatapathBinEnv, "")
	env := executableSupervisorEnv(Request{Identity: &Identity{Backend: BackendAppleVF}})
	for _, entry := range env {
		if strings.HasPrefix(entry, EgressDatapathBinEnv+"=") && entry != EgressDatapathBinEnv+"=" {
			t.Fatalf("embedder executable was implicitly selected as datapath: %#v", env)
		}
	}
	if got := ResolveEgressDatapathBin(); got != "" {
		t.Fatalf("ResolveEgressDatapathBin() = %q, want empty without explicit selection", got)
	}
}

func TestDatapathStartupFailureResponseRoundTrip(t *testing.T) {
	status := int32(23)
	want := Response{
		OK:      false,
		Backend: BackendAppleVF,
		DatapathStartupFailure: &DatapathStartupFailure{
			Boundary:        "apple-vf.host-fd.datapath",
			ExecutablePath:  "/opt/microagent",
			ExitStatus:      &status,
			DiagnosticsPath: "/state/ws/datapath.log",
			Reason:          "datapath exited before preboot readiness",
		},
		Error: "startup failed",
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.DatapathStartupFailure == nil ||
		got.DatapathStartupFailure.Boundary != want.DatapathStartupFailure.Boundary ||
		got.DatapathStartupFailure.ExecutablePath != want.DatapathStartupFailure.ExecutablePath ||
		got.DatapathStartupFailure.ExitStatus == nil || *got.DatapathStartupFailure.ExitStatus != status ||
		got.DatapathStartupFailure.DiagnosticsPath != want.DatapathStartupFailure.DiagnosticsPath {
		t.Fatalf("datapath startup failure round trip = %#v, want %#v", got.DatapathStartupFailure, want.DatapathStartupFailure)
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

func envHasKey(env []string, key string) bool {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
