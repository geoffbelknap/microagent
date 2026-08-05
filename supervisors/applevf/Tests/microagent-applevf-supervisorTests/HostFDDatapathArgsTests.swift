import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// hostFDDatapathArgs builds the argv for the Go `microagent --egress-datapath`
// subprocess. Every egress-relevant Config field must be forwarded: a field
// the supervisor silently drops becomes a silent fail-open on apple-vf (the
// egress-mode vocabulary drift did exactly that, and egressAllowlistLocked
// was dropped the same way — a locked workspace still egressed anywhere).
final class HostFDDatapathArgsTests: XCTestCase {
    private func config(mode: String? = "broker") -> Config {
        var config = Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
        config.network = NetworkConfig(mode: "user")
        config.egressMode = mode
        return config
    }

    private func identity() -> Identity {
        Identity(requestID: "r", runtimeID: "ws", role: .workload, backend: "apple-vf", homeHash: nil)
    }

    func testModePassedVerbatimAndOffWhenUnset() {
        var args = hostFDDatapathArgs(config: config(mode: "broker"), identity: identity())
        XCTAssertEqual(valueAfter("--egress-mode", in: args), "broker")
        args = hostFDDatapathArgs(config: config(mode: "mitm"), identity: identity())
        XCTAssertEqual(valueAfter("--egress-mode", in: args), "mitm")
        // The smoke-test override with no mode runs the datapath unmediated.
        args = hostFDDatapathArgs(config: config(mode: nil), identity: identity())
        XCTAssertEqual(valueAfter("--egress-mode", in: args), "off")
    }

    func testDualStackGatewayPassedToDatapath() {
        let args = hostFDDatapathArgs(config: config(), identity: identity())
        XCTAssertEqual(valueAfter("--gateway-ip", in: args), hostFDGatewayIP)
        XCTAssertEqual(valueAfter("--gateway-ipv6", in: args), hostFDGatewayIPv6)
    }

    func testAllowlistLockForwarded() {
        var locked = config()
        locked.egressAllowlistLocked = true
        XCTAssertTrue(
            hostFDDatapathArgs(config: locked, identity: identity()).contains("--lock-allowlist"),
            "egressAllowlistLocked must reach the datapath or the lock silently fails open"
        )
        XCTAssertFalse(hostFDDatapathArgs(config: config(), identity: identity()).contains("--lock-allowlist"))
        var explicitOff = config()
        explicitOff.egressAllowlistLocked = false
        XCTAssertFalse(hostFDDatapathArgs(config: explicitOff, identity: identity()).contains("--lock-allowlist"))
    }

    func testAllowPassthroughAndSwapForwarded() {
        var cfg = config()
        cfg.egressAllow = ["example.com", "api.example.com"]
        cfg.egressPassthrough = ["passthru.example.com"]
        cfg.egressSwapConfigPath = "/state/swap.yaml"
        let args = hostFDDatapathArgs(config: cfg, identity: identity())
        XCTAssertEqual(valuesAfter("--allow", in: args), ["example.com", "api.example.com"])
        XCTAssertEqual(valuesAfter("--passthrough", in: args), ["passthru.example.com"])
        XCTAssertEqual(valueAfter("--swap-config", in: args), "/state/swap.yaml")
        XCTAssertEqual(valueAfter("--name", in: args), "ws")
        XCTAssertEqual(valueAfter("--state-dir", in: args), "/state")
    }

    func testCapsForwardedWhenSetOmittedWhenNot() {
        var cfg = config()
        cfg.egressMaxBytesPerSec = 1_048_576
        cfg.egressMaxTotalBytes = 10_485_760
        cfg.egressMaxConcurrentConns = 16
        let args = hostFDDatapathArgs(config: cfg, identity: identity())
        XCTAssertEqual(valueAfter("--max-bps", in: args), "1048576")
        XCTAssertEqual(valueAfter("--max-bytes", in: args), "10485760")
        XCTAssertEqual(valueAfter("--max-conns", in: args), "16")

        // Unset (and explicit-zero) caps are omitted so an uncapped workspace's
        // argv is byte-identical to the pre-caps one, matching the Firecracker
        // mediator argv shape.
        for uncapped in [config(), zeroCaps()] {
            let plain = hostFDDatapathArgs(config: uncapped, identity: identity())
            XCTAssertFalse(plain.contains("--max-bps"))
            XCTAssertFalse(plain.contains("--max-bytes"))
            XCTAssertFalse(plain.contains("--max-conns"))
        }
    }

    func testAuditRotationForwardedOnlyWithSizeCap() {
        var cfg = config()
        cfg.egressAuditMaxBytes = 4096
        cfg.egressAuditMaxBackups = 3
        let args = hostFDDatapathArgs(config: cfg, identity: identity())
        XCTAssertEqual(valueAfter("--audit-max-bytes", in: args), "4096")
        XCTAssertEqual(valueAfter("--audit-max-backups", in: args), "3")

        // Backups without a size cap are meaningless (nothing rotates); mirror
        // the Firecracker mediator and omit both.
        var backupsOnly = config()
        backupsOnly.egressAuditMaxBackups = 3
        let plain = hostFDDatapathArgs(config: backupsOnly, identity: identity())
        XCTAssertFalse(plain.contains("--audit-max-bytes"))
        XCTAssertFalse(plain.contains("--audit-max-backups"))
    }

    func testResolversForwardedFromWorkspaceNameservers() {
        var cfg = config()
        cfg.network?.dns = ["9.9.9.9", " 1.1.1.1 ", "", "   "]
        let args = hostFDDatapathArgs(config: cfg, identity: identity())
        XCTAssertEqual(
            valuesAfter("--resolver", in: args), ["9.9.9.9", "1.1.1.1"],
            "workspace nameservers must reach the datapath's resolver allowlist or off-list DNS silently resolves"
        )
        // No configured nameservers → no --resolver flags, leaving the
        // datapath's internal-address floor in force.
        XCTAssertFalse(hostFDDatapathArgs(config: config(), identity: identity()).contains("--resolver"))
    }

    // The cross-language half of the egress datapath parity guard.
    // egressDatapathFieldSpecs is generated from vmkit.EgressDatapathFields()
    // (a Go sync test keeps the copy identical), so a control registered in Go
    // fails this test until hostFDDatapathArgs forwards it — the next dropped
    // field is caught here instead of by a live security repro.
    func testArgsCoverEgressFieldRegistry() {
        var cfg = config(mode: "broker")
        cfg.egressAllow = ["example.com"]
        cfg.egressPassthrough = ["passthru.example.com"]
        cfg.egressAllowlistLocked = true
        cfg.egressSwapConfigPath = "/state/swap.yaml"
        cfg.egressMaxBytesPerSec = 1
        cfg.egressMaxTotalBytes = 2
        cfg.egressMaxConcurrentConns = 3
        cfg.egressAuditMaxBytes = 4
        cfg.egressAuditMaxBackups = 5
        cfg.network?.dns = ["1.1.1.1"]
        let args = hostFDDatapathArgs(config: cfg, identity: identity())
        for spec in egressDatapathFieldSpecs {
            XCTAssertTrue(
                args.contains("--\(spec.datapathFlag)"),
                "registered egress control --\(spec.datapathFlag) (config \(spec.configField.isEmpty ? "Network.DNS" : spec.configField)) is not forwarded by hostFDDatapathArgs: "
                    + "either forward it, or — if it derives from a new Config field — also populate that field in this test's config"
            )
        }
    }

    private func zeroCaps() -> Config {
        var cfg = config()
        cfg.egressMaxBytesPerSec = 0
        cfg.egressMaxTotalBytes = 0
        cfg.egressMaxConcurrentConns = 0
        cfg.egressAuditMaxBytes = 0
        cfg.egressAuditMaxBackups = 0
        return cfg
    }

    private func valueAfter(_ flag: String, in args: [String]) -> String? {
        guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
        return args[i + 1]
    }

    private func valuesAfter(_ flag: String, in args: [String]) -> [String] {
        var out: [String] = []
        var i = 0
        while i < args.count {
            if args[i] == flag, i + 1 < args.count {
                out.append(args[i + 1])
                i += 2
            } else {
                i += 1
            }
        }
        return out
    }
}
