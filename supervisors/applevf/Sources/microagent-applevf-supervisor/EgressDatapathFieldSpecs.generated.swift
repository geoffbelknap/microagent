// Code generated from pkg/vmkit/egressfields.go; DO NOT EDIT.
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
    EgressDatapathFieldSpec(configField: "EgressMode", datapathFlag: "egress-mode", security: true),
    EgressDatapathFieldSpec(configField: "EgressAllow", datapathFlag: "allow", security: true),
    EgressDatapathFieldSpec(configField: "EgressPassthrough", datapathFlag: "passthrough", security: true),
    EgressDatapathFieldSpec(configField: "EgressAllowlistLocked", datapathFlag: "lock-allowlist", security: true),
    EgressDatapathFieldSpec(configField: "EgressSwapConfigPath", datapathFlag: "swap-config", security: true),
    EgressDatapathFieldSpec(configField: "EgressMaxBytesPerSec", datapathFlag: "max-bps", security: true),
    EgressDatapathFieldSpec(configField: "EgressMaxTotalBytes", datapathFlag: "max-bytes", security: true),
    EgressDatapathFieldSpec(configField: "EgressMaxConcurrentConns", datapathFlag: "max-conns", security: true),
    EgressDatapathFieldSpec(configField: "EgressAuditMaxBytes", datapathFlag: "audit-max-bytes", security: false),
    EgressDatapathFieldSpec(configField: "EgressAuditMaxBackups", datapathFlag: "audit-max-backups", security: false),
    EgressDatapathFieldSpec(configField: "", datapathFlag: "resolver", security: true),
]
