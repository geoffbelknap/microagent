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
// regression cannot recur silently.
final class HostFDEgressTests: XCTestCase {
    private func userConfig(egressMode: String?) -> Config {
        var config = Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
        config.network = NetworkConfig(mode: "user")
        config.egressMode = egressMode
        return config
    }

    private func identity() -> Identity {
        Identity(requestID: "request", runtimeID: "workspace", role: .workload, backend: "apple-vf", homeHash: nil)
    }

    private func writeExecutable(_ body: String, in directory: URL, name: String = "datapath") throws -> String {
        let path = directory.appendingPathComponent(name)
        try Data(body.utf8).write(to: path)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: path.path)
        return path.path
    }

    private func withDatapathBinary(_ path: String, _ body: () throws -> Void) rethrows {
        let key = "MICROAGENT_EGRESS_DATAPATH_BIN"
        let previous = ProcessInfo.processInfo.environment[key]
        setenv(key, path, 1)
        defer {
            if let previous {
                setenv(key, previous, 1)
            } else {
                unsetenv(key)
            }
            _ = closeHostFDEgress()
        }
        try body()
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

    func testCACertMaterialRejectsMissingEmptyUnreadableAndOversized() throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("microagent-ca-validation-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let path = directory.appendingPathComponent("egress-ca.pem")

        XCTAssertThrowsError(try loadValidatedCACert(path: path)) { error in
            XCTAssertTrue("\(error)".contains("missing"), "\(error)")
        }
        FileManager.default.createFile(atPath: path.path, contents: nil)
        XCTAssertThrowsError(try loadValidatedCACert(path: path)) { error in
            XCTAssertTrue("\(error)".contains("empty"), "\(error)")
        }
        try Data(repeating: 65, count: maxCACertBytes + 1).write(to: path)
        XCTAssertThrowsError(try loadValidatedCACert(path: path)) { error in
            XCTAssertTrue("\(error)".contains("maximum"), "\(error)")
        }
        try Data("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----\n".utf8).write(to: path)
        try FileManager.default.setAttributes([.posixPermissions: 0o000], ofItemAtPath: path.path)
        defer { try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path) }
        XCTAssertThrowsError(try loadValidatedCACert(path: path)) { error in
            XCTAssertTrue("\(error)".contains("unreadable"), "\(error)")
        }
    }

    func testBadDatapathExecutableFailsBeforePreparation() throws {
        var config = userConfig(egressMode: "mitm")
        config.stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("microagent-bad-datapath-\(UUID().uuidString)").path
        config.caCertPort = 1030
        let missing = URL(fileURLWithPath: config.stateDir).appendingPathComponent("missing-microagent").path
        try withDatapathBinary(missing) {
            XCTAssertThrowsError(try prepareHostFDEgressBeforeConfinement(config: config, identity: identity())) { error in
                guard let startup = error as? DatapathStartupError else {
                    return XCTFail("error = \(error), want DatapathStartupError")
                }
                XCTAssertEqual(startup.failure.boundary, "apple-vf.host-fd.datapath")
                XCTAssertEqual(startup.failure.executablePath, missing)
                XCTAssertNil(startup.failure.exitStatus)
                XCTAssertTrue(startup.failure.reason.contains("missing"))
            }
        }
    }

    func testMissingExplicitDatapathSelectionFailsWithTypedRemedy() throws {
        var config = userConfig(egressMode: "mitm")
        config.stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("microagent-unselected-datapath-\(UUID().uuidString)").path
        config.caCertPort = 1030
        try withDatapathBinary("") {
            XCTAssertThrowsError(try prepareHostFDEgressBeforeConfinement(config: config, identity: identity())) { error in
                guard let startup = error as? DatapathStartupError else {
                    return XCTFail("error = \(error), want DatapathStartupError")
                }
                XCTAssertEqual(startup.failure.boundary, "apple-vf.host-fd.datapath")
                XCTAssertEqual(startup.failure.executablePath, "")
                XCTAssertTrue(startup.failure.reason.contains("explicit MICROAGENT_EGRESS_DATAPATH_BIN"))
            }
        }
    }

    func testEarlyExitIsStructuredAndDiagnosticsAreBoundedAndRedacted() throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("microagent-early-datapath-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let sentinel = "diagnostic-secret-\(UUID().uuidString)"
        setenv("MICROAGENT_DIAGNOSTIC_SENTINEL", sentinel, 1)
        defer { unsetenv("MICROAGENT_DIAGNOSTIC_SENTINEL") }
        let script = try writeExecutable(
            "#!/bin/sh\nprintf '%s %s\\n' \"$MICROAGENT_DIAGNOSTIC_SENTINEL\" \"$*\" >&2\nexit 23\n",
            in: directory
        )
        var config = userConfig(egressMode: "mitm")
        config.stateDir = directory.path
        config.caCertPort = 1030

        try withDatapathBinary(script) {
            XCTAssertThrowsError(try prepareHostFDEgressBeforeConfinement(config: config, identity: identity())) { error in
                guard let startup = error as? DatapathStartupError else {
                    return XCTFail("error = \(error), want DatapathStartupError")
                }
                XCTAssertEqual(startup.failure.exitStatus, 23)
                XCTAssertEqual(startup.failure.executablePath, script)
                let path = URL(fileURLWithPath: startup.failure.diagnosticsPath)
                let data = (try? Data(contentsOf: path)) ?? Data()
                XCTAssertLessThanOrEqual(data.count, maxDatapathDiagnosticBytes)
                let text = String(decoding: data, as: UTF8.self)
                XCTAssertFalse(text.contains(sentinel), text)
                XCTAssertFalse(text.contains(directory.path), text)
                let attributes = try? FileManager.default.attributesOfItem(atPath: path.path)
                let mode = (attributes?[.posixPermissions] as? NSNumber)?.intValue
                XCTAssertEqual(mode, 0o600)
            }
        }
    }

    func testSuccessfulDatapathProducesCAAndRemainsLive() throws {
        let directory = FileManager.default.temporaryDirectory
            .appendingPathComponent("microagent-ready-datapath-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let script = try writeExecutable(
            """
            #!/bin/sh
            state=''
            name=''
            while [ "$#" -gt 0 ]; do
              case "$1" in
                --state-dir) state="$2"; shift 2 ;;
                --name) name="$2"; shift 2 ;;
                *) shift ;;
              esac
            done
            mkdir -p "$state/$name"
            printf '%s\n' '-----BEGIN CERTIFICATE-----' 'test' '-----END CERTIFICATE-----' > "$state/$name/egress-ca.pem"
            sleep 30
            """,
            in: directory
        )
        var config = userConfig(egressMode: "mitm")
        config.stateDir = directory.path
        config.caCertPort = 1030

        try withDatapathBinary(script) {
            XCTAssertNoThrow(try prepareHostFDEgressBeforeConfinement(config: config, identity: identity()))
            XCTAssertTrue(hostFDDatapath?.isRunning == true)
            XCTAssertNoThrow(try loadValidatedCACert(
                path: directory.appendingPathComponent("workspace/egress-ca.pem")
            ))
        }
    }
}
