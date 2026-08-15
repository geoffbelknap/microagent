import Foundation
@testable import microagent_applevf_supervisor
import XCTest

final class RuntimeControlTests: XCTestCase {
    func testLifecycleAuditRequestRoundTrip() throws {
        let (identity, config) = try fixture()
        let audit = LifecycleAudit(
            initiator: CallerAttribution(channel: "mcp", subject: "operator-7", delegatedAuthority: "workspace:control", assurance: "caller_asserted"),
            reason: "incident response",
            workInFlight: WorkInFlight(
                declared: [DeclaredWork(kind: "service", command: "serve")],
                guestReported: [GuestProcess(pid: 42, ppid: 1, command: "run-task")],
                captureStatus: "captured",
                captureError: nil,
                capturedAt: Date(),
                evidenceRef: "snapshot:forensic-1"
            ),
            notification: NotificationRecord(status: "not_performed", owner: "caller", reason: "owned by caller")
        )
        let request = Request(command: "halt", identity: identity, config: config, lifecycle: audit)
        let data = try JSONEncoder().encode(request)
        let decoded = try JSONDecoder().decode(Request.self, from: data)

        XCTAssertEqual(decoded.lifecycle?.initiator.subject, "operator-7")
        XCTAssertEqual(decoded.lifecycle?.workInFlight.guestReported?.first?.command, "run-task")
        XCTAssertEqual(decoded.lifecycle?.workInFlight.evidenceRef, "snapshot:forensic-1")
    }

    func testEnsureCanStartRejectsPausedWorkspace() throws {
        let (identity, config) = try fixture()
        let event = Event(identity: identity, state: .paused, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: Int32(getpid()), error: nil)

        XCTAssertThrowsError(try ensureCanStart(identity: identity, stateDir: config.stateDir)) { error in
            XCTAssertTrue(String(describing: error).contains("is paused"))
        }
    }

    func testReadinessModelsPausedAsGuestReadyButNotExecReady() throws {
        let (identity, config) = try fixture(execPort: 45123)
        let event = Event(identity: identity, state: .paused, detail: nil, observedAt: Date())

        let got = readiness(event: event, config: config)

        XCTAssertEqual(got.guestReady.ready, true)
        XCTAssertNil(got.shellReady.ready)
        XCTAssertNil(got.execReady.ready)
    }

    func testPauseRequiresRunningStateBeforeSignaling() throws {
        let (identity, config) = try fixture()
        let event = Event(identity: identity, state: .paused, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: Int32(getpid()), error: nil)
        let request = Request(command: "pause", identity: identity, config: Config(kernelPath: "", rootfsPath: "", stateDir: config.stateDir))

        XCTAssertThrowsError(try pauseLive(request)) { error in
            XCTAssertTrue(String(describing: error).contains("pause requires state running"))
        }
    }

    func testResumeRequiresPausedStateBeforeSignaling() throws {
        let (identity, config) = try fixture()
        let event = Event(identity: identity, state: .running, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: Int32(getpid()), error: nil)
        let request = Request(command: "resume", identity: identity, config: Config(kernelPath: "", rootfsPath: "", stateDir: config.stateDir))

        XCTAssertThrowsError(try resumeLive(request)) { error in
            XCTAssertTrue(String(describing: error).contains("resume requires state paused"))
        }
    }

    func testResumeFailsClosedWhenContainmentMarkerExists() throws {
        let (identity, config) = try fixture()
        let event = Event(identity: identity, state: .paused, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: Int32(getpid()), error: nil)
        try FileManager.default.createDirectory(
            at: containmentMarkerDir(identity: identity, stateDir: config.stateDir),
            withIntermediateDirectories: true
        )
        let request = Request(command: "resume", identity: identity, config: Config(kernelPath: "", rootfsPath: "", stateDir: config.stateDir))

        XCTAssertThrowsError(try resumeLive(request)) { error in
            XCTAssertTrue(String(describing: error).contains("containment marker"))
        }
    }

    func testMissingRuntimeControlRequestIsIdleNotAnError() throws {
        let path = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("microagent-runtime-control-missing-\(UUID().uuidString)")

        XCTAssertNil(try runtimeControlRequestData(path: path))
    }

    func testRuntimeControlRequestDataStillSurfacesOtherReadFailures() throws {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("microagent-runtime-control-directory-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }

        XCTAssertThrowsError(try runtimeControlRequestData(path: dir))
    }

    func testProcessStatusTreatsZombieAsExited() {
        XCTAssertFalse(processStatusIsAlive(UInt32(SZOMB)))
        XCTAssertTrue(processStatusIsAlive(UInt32(SRUN)))
        XCTAssertTrue(processInfoIndicatesExited(result: 0, error: ESRCH))
        XCTAssertFalse(processInfoIndicatesExited(result: 0, error: EPERM))
    }

    func testTCPPublishForwardUsesGuestPortForGuestConnection() {
        let forward = PortForward(protocolName: "tcp", host: "127.0.0.1", hostPort: 41000, guestPort: 51000)

        XCTAssertEqual(guestVsockPort(forward), 51000)
    }

    func testPublishedPortClosureCountExcludesInternalControlPorts() {
        var config = Config(kernelPath: "/kernel", rootfsPath: "/rootfs", stateDir: "/state")
        config.network = NetworkConfig(mode: "user")
        config.network?.portForwards = [
            PortForward(protocolName: "tcp", host: "127.0.0.1", hostPort: 41000, guestPort: 8080),
        ]
        config.shellPort = 41001
        config.execPort = 41002

        XCTAssertEqual(tcpPublishForwards(config: config).count, 3)
        XCTAssertEqual(publishedPortClosureCount(config: config), 1)
    }

    func testLivePortForwardChangeIncludesGuestShellPort() {
        var oldConfig = Config(kernelPath: "/kernel", rootfsPath: "/rootfs", stateDir: "/state")
        oldConfig.shellPort = 41000
        oldConfig.guestShellPort = 51000
        var newConfig = oldConfig
        newConfig.guestShellPort = 51001

        XCTAssertFalse(livePortForwardHostOnlyChange(oldConfig: oldConfig, newConfig: newConfig))
    }

    private func fixture(execPort: UInt16? = nil) throws -> (Identity, Config) {
        let dir = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent("microagent-applevf-runtime-control-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let identity = Identity(requestID: "r", runtimeID: "web", role: .workload, backend: "apple-vf", homeHash: nil)
        var config = Config(kernelPath: "/kernel", rootfsPath: "/rootfs", stateDir: dir.path)
        config.execPort = execPort
        config.serialInput = true
        return (identity, config)
    }
}
