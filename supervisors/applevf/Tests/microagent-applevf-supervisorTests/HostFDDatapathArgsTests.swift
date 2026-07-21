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
