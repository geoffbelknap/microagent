import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// The contract defines mediationReady as "declared mediation channel target is
// live reachable for a running workspace" (pkg/vmkit/contract.go). These tests
// pin that a running state alone never reports the mediation channel ready:
// the supervisor must probe the declared target and report what it verified.
final class MediationReadinessTests: XCTestCase {
    func testTargetDownIsNotReady() throws {
        let port = try reservedClosedPort()
        let (event, config) = fixture(state: .running, target: "127.0.0.1:\(port)", required: true)
        let signal = readiness(event: event, config: config).mediationReady
        XCTAssertEqual(signal.ready, false, "mediationReady must be false when nothing listens on the mediation target")
        XCTAssertEqual(signal.error, "required mediation target is unreachable")
        XCTAssertTrue(signal.detail?.contains("unreachable") == true, "detail = \(signal.detail ?? "nil")")
    }

    func testTargetListeningIsReady() throws {
        let listener = try loopbackListener()
        defer { close(listener.fd) }
        let (event, config) = fixture(state: .running, target: "127.0.0.1:\(listener.port)", required: true)
        let signal = readiness(event: event, config: config).mediationReady
        XCTAssertEqual(signal.ready, true, "detail = \(signal.detail ?? "nil")")
        XCTAssertNil(signal.error)
        XCTAssertTrue(signal.detail?.contains("reachable") == true, "detail = \(signal.detail ?? "nil")")
    }

    func testNotRunningDoesNotProbeAndIsNotReady() throws {
        let listener = try loopbackListener()
        defer { close(listener.fd) }
        let (event, config) = fixture(state: .prepared, target: "127.0.0.1:\(listener.port)", required: true)
        let signal = readiness(event: event, config: config).mediationReady
        XCTAssertEqual(signal.ready ?? false, false, "a live target must not make a non-running workspace's mediation ready")
        XCTAssertEqual(signal.error, "required mediation is not ready")
    }

    func testInvalidTargetIsNotReady() {
        let (event, config) = fixture(state: .running, target: "not-a-host-port", required: true)
        let signal = readiness(event: event, config: config).mediationReady
        XCTAssertEqual(signal.ready, false)
        XCTAssertEqual(signal.error, "required mediation target is invalid")
    }

    func testOptionalMediationDownSetsNoError() throws {
        let port = try reservedClosedPort()
        let (event, config) = fixture(state: .running, target: "127.0.0.1:\(port)", required: false)
        let signal = readiness(event: event, config: config).mediationReady
        XCTAssertEqual(signal.ready, false)
        XCTAssertNil(signal.error, "optional mediation being down is not an error condition")
    }

    private func fixture(state: VMState, target: String, required: Bool) -> (Event, Config) {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("microagent-applevf-mediation-\(UUID().uuidString)", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let identity = Identity(requestID: "r", runtimeID: "web", role: .workload, backend: "apple-vf", homeHash: nil)
        var config = Config(kernelPath: "/kernel", rootfsPath: "/rootfs", stateDir: dir.path)
        config.mediation = MediationConfig(enabled: true, required: required, port: 1027, target: target, failClosed: true)
        let event = Event(identity: identity, state: state, detail: nil, observedAt: Date())
        return (event, config)
    }

    // loopbackListener binds an OS-assigned loopback port and listens on it.
    private func loopbackListener() throws -> (fd: Int32, port: UInt16) {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { throw NSError(domain: "test", code: Int(errno)) }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        inet_pton(AF_INET, "127.0.0.1", &addr.sin_addr)
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bound == 0, listen(fd, 8) == 0 else {
            close(fd)
            throw NSError(domain: "test", code: Int(errno))
        }
        var boundAddr = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &boundAddr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(fd, $0, &len)
            }
        }
        return (fd, UInt16(bigEndian: boundAddr.sin_port))
    }

    // reservedClosedPort returns a loopback port that was just bound and
    // released, so nothing is listening on it.
    private func reservedClosedPort() throws -> UInt16 {
        let listener = try loopbackListener()
        close(listener.fd)
        return listener.port
    }
}
