import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// brokerServeArgs builds the argv for the Go `--broker-serve` companion.
// Every endpoint field the companion consumes must be forwarded — a field
// this mapping drops is silently unenforced on apple-vf (the
// HostFDDatapathArgsTests failure class, applied to broker endpoints).
final class BrokerCompanionTests: XCTestCase {
    private func config() -> Config {
        Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
    }

    private func identity() -> Identity {
        Identity(requestID: "r", runtimeID: "ws", role: .workload, backend: "apple-vf", homeHash: nil)
    }

    private func endpoint() -> BrokerConfig {
        BrokerConfig(
            upstream: "https://api.example.com",
            secret: BrokerSecretRef(name: "tok", ref: "env:TOK"),
            guestListen: nil,
            vsockPort: 1032,
            proxy: nil,
            baseURLEnv: nil,
            capture: nil,
            upstreamCAFile: nil,
            connectAllowlist: nil
        )
    }

    func testArgsForwardEndpointFields() {
        var ep = endpoint()
        ep.proxy = true
        ep.connectAllowlist = ["a.example.com", "b.example.com"]
        ep.upstreamCAFile = "/state/ca.pem"
        ep.capture = true
        let args = brokerServeArgs(endpoint: ep, config: config(), identity: identity(), listenPath: "/state/ws/broker-1032.sock")
        XCTAssertEqual(args.first, "--broker-serve")
        for expected: [String] in [
            ["--state-dir", "/state"],
            ["--name", "ws"],
            ["--listen", "/state/ws/broker-1032.sock"],
            ["--upstream", "https://api.example.com"],
            ["--secret", "tok=env:TOK"],
            ["--connect-allow", "a.example.com"],
            ["--connect-allow", "b.example.com"],
            ["--upstream-ca", "/state/ca.pem"],
        ] {
            guard let index = args.firstIndex(of: expected[0]) else {
                return XCTFail("missing \(expected[0]) in \(args)")
            }
            var found = false
            var search = index
            while let next = args[search...].firstIndex(of: expected[0]) {
                if next + 1 < args.count && args[next + 1] == expected[1] {
                    found = true
                    break
                }
                search = next + 1
            }
            XCTAssertTrue(found, "missing \(expected) in \(args)")
        }
        XCTAssertTrue(args.contains("--proxy"))
        XCTAssertTrue(args.contains("--capture"))
    }

    func testArgsOmitOptionalFlagsWhenUnset() {
        let args = brokerServeArgs(endpoint: endpoint(), config: config(), identity: identity(), listenPath: "/p.sock")
        XCTAssertFalse(args.contains("--proxy"))
        XCTAssertFalse(args.contains("--capture"))
        XCTAssertFalse(args.contains("--upstream-ca"))
        XCTAssertFalse(args.contains("--connect-allow"))
    }

    // The 104-byte sun_path limit must surface as a clear upfront error
    // naming the cause, not a bind failure with an unrelated message.
    func testSocketPathLengthChecked() {
        let short = URL(fileURLWithPath: "/tmp/ws/broker-1032.sock")
        XCTAssertNoThrow(try validateBrokerSocketPath(short, port: 1032))
        let long = URL(fileURLWithPath: "/" + String(repeating: "d", count: 110) + "/broker-1032.sock")
        XCTAssertThrowsError(try validateBrokerSocketPath(long, port: 1032)) { error in
            XCTAssertTrue("\(error)".contains("unix sockets cap at"), "\(error)")
        }
    }

    func testEndpointResolutionByPortWithLegacyFallback() {
        var cfg = config()
        var a = endpoint()
        a.vsockPort = 1032
        var b = endpoint()
        b.vsockPort = 1033
        b.upstream = "https://b.example.com"
        cfg.brokers = [a, b]
        XCTAssertEqual(brokerEndpoint(config: cfg, port: 1033)?.upstream, "https://b.example.com")
        XCTAssertNil(brokerEndpoint(config: cfg, port: 9999))

        // Legacy single-endpoint config serves whatever port the listener has.
        var legacy = config()
        legacy.broker = endpoint()
        XCTAssertEqual(brokerEndpoint(config: legacy, port: 1500)?.upstream, "https://api.example.com")
    }
}
