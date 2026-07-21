import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// hostFDEgressEnabled decides whether an Apple VF "user" workspace routes egress
// through the mediated host-fd datapath (allowlist + audit + credential swap) or
// falls back to the framework's unmediated native NAT. It MUST mirror Go's
// pkg/vmkit/types.go:EgressMediationOn: mediate for the final egress-mode
// vocabulary broker/mitm, native NAT for off/unset. When this drifted — it still
// gated on the retired "guarded"/"strict" names after commit 452c510 renamed the
// modes — every default (broker) workspace silently ran unmediated while inspect
// reported host-enforced mediation. This test pins the accepted mode set so that
// regression cannot recur silently. See micro-workspace#1.
final class HostFDEgressTests: XCTestCase {
    private func userConfig(egressMode: String?) -> Config {
        var config = Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
        config.network = NetworkConfig(mode: "user")
        config.egressMode = egressMode
        return config
    }

    func testMediatedModesEnableHostFDDatapath() {
        XCTAssertTrue(
            hostFDEgressEnabled(config: userConfig(egressMode: "broker")),
            "broker (the default) must run the mediated datapath, not native NAT"
        )
        XCTAssertTrue(
            hostFDEgressEnabled(config: userConfig(egressMode: "mitm")),
            "mitm must run the mediated datapath"
        )
        // Case/whitespace tolerance mirrors EgressMediationOn's trim+lowercase.
        XCTAssertTrue(hostFDEgressEnabled(config: userConfig(egressMode: " BROKER ")))
    }

    func testUnmediatedAndRetiredModesUseNativeNAT() throws {
        try XCTSkipIf(
            ProcessInfo.processInfo.environment["MICROAGENT_APPLEVF_HOSTFD"] == "1",
            "the smoke-test override forces the datapath on regardless of mode"
        )
        XCTAssertFalse(
            hostFDEgressEnabled(config: userConfig(egressMode: "off")),
            "explicit egress=off keeps native NAT"
        )
        XCTAssertFalse(
            hostFDEgressEnabled(config: userConfig(egressMode: nil)),
            "an unset mode (low-level raw primitive) must not force mediation"
        )
        XCTAssertFalse(
            hostFDEgressEnabled(config: userConfig(egressMode: "")),
            "an empty mode must behave exactly like unset, matching EgressMediationOn(\"\")"
        )
        XCTAssertFalse(hostFDEgressEnabled(config: userConfig(egressMode: "   ")))
        // The retired names must never re-enable the datapath: they are rejected
        // upstream and must not be silently reinterpreted here.
        XCTAssertFalse(hostFDEgressEnabled(config: userConfig(egressMode: "guarded")))
        XCTAssertFalse(hostFDEgressEnabled(config: userConfig(egressMode: "strict")))
    }

    func testIsolatedNetworkNeverRunsDatapath() throws {
        try XCTSkipIf(
            ProcessInfo.processInfo.environment["MICROAGENT_APPLEVF_HOSTFD"] == "1",
            "the smoke-test override bypasses the network-mode guard"
        )
        var config = userConfig(egressMode: "broker")
        config.network = NetworkConfig(mode: "isolated")
        XCTAssertFalse(
            hostFDEgressEnabled(config: config),
            "isolated networking has no egress to mediate"
        )
    }
}
