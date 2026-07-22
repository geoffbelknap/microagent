package vmkit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appleVFSwiftRegistryPath is the generated Swift copy of EgressDatapathFields
// embedded in the Apple VF supervisor package. The Swift parity test
// (EgressFieldRegistryParityTests) asserts hostFDDatapathArgs forwards every
// control listed there; this sync test keeps the copy identical to the Go
// registry, so together they extend the datapath parity guard across the
// language boundary: register a field here and Swift CI fails until the
// supervisor forwards it.
const appleVFSwiftRegistryPath = "../../supervisors/applevf/Sources/microagent-applevf-supervisor/EgressDatapathFieldSpecs.generated.swift"

func swiftEgressFieldsSource() string {
	var b strings.Builder
	b.WriteString(`// Code generated from pkg/vmkit/egressfields.go; DO NOT EDIT.
// Regenerate: MICROAGENT_REGEN_SWIFT_EGRESS_FIELDS=1 go test ./pkg/vmkit -run TestAppleVFSwiftEgressFieldRegistryInSync
//
// Swift copy of vmkit.EgressDatapathFields — the canonical set of egress-policy
// controls the host-fd datapath must forward. EgressFieldRegistryParityTests
// asserts hostFDDatapathArgs emits every flag listed here; the Go sync test
// keeps this copy identical to the registry. A control listed here but not
// forwarded is a silent fail-open on apple-vf (the B1/B22/B23 class).

struct EgressDatapathFieldSpec {
    let configField: String
    let datapathFlag: String
    let security: Bool
}

let egressDatapathFieldSpecs: [EgressDatapathFieldSpec] = [
`)
	for _, f := range EgressDatapathFields() {
		fmt.Fprintf(&b, "    EgressDatapathFieldSpec(configField: %q, datapathFlag: %q, security: %v),\n",
			f.ConfigField, f.DatapathFlag, f.Security)
	}
	b.WriteString("]\n")
	return b.String()
}

// TestAppleVFSwiftEgressFieldRegistryInSync fails when the generated Swift copy
// of the registry drifts from EgressDatapathFields(). It runs on every platform
// (plain file comparison), so adding a registry entry without regenerating the
// Swift copy fails Linux CI — and once regenerated, the Swift parity test fails
// until hostFDDatapathArgs actually forwards the new control.
func TestAppleVFSwiftEgressFieldRegistryInSync(t *testing.T) {
	want := swiftEgressFieldsSource()
	path := filepath.Clean(appleVFSwiftRegistryPath)
	if os.Getenv("MICROAGENT_REGEN_SWIFT_EGRESS_FIELDS") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatalf("regenerate %s: %v", path, err)
		}
		t.Logf("regenerated %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with MICROAGENT_REGEN_SWIFT_EGRESS_FIELDS=1 go test ./pkg/vmkit -run TestAppleVFSwiftEgressFieldRegistryInSync)", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s is out of sync with vmkit.EgressDatapathFields(); regenerate with MICROAGENT_REGEN_SWIFT_EGRESS_FIELDS=1 go test ./pkg/vmkit -run TestAppleVFSwiftEgressFieldRegistryInSync", path)
	}
}
