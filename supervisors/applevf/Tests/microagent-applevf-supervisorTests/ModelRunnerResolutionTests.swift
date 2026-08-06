import Foundation
import XCTest
@testable import microagent_applevf_supervisor

final class ModelRunnerResolutionTests: XCTestCase {
    func testResolveModelRunnerTracksRestartedPort() throws {
        let stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
        let runnersDir = stateDir.appendingPathComponent("runners")
        try FileManager.default.createDirectory(at: runnersDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: stateDir) }

        let modelRef = "hf.co/example/model@main/model.gguf"
        func writeIndex(port: Int) throws {
            let json: [String: Any] = [
                "runners": [[
                    "model_ref": modelRef,
                    "host": "127.0.0.1",
                    "port": port,
                    "pid": ProcessInfo.processInfo.processIdentifier,
                ]],
            ]
            let data = try JSONSerialization.data(withJSONObject: json)
            try data.write(to: runnersDir.appendingPathComponent("index.json"))
        }

        try writeIndex(port: 31001)
        var target = resolveModelRunnerTarget(stateDir: stateDir.path, modelRef: modelRef)
        XCTAssertEqual(target?.host, "127.0.0.1")
        XCTAssertEqual(target?.port, 31001)

        try writeIndex(port: 31002)
        target = resolveModelRunnerTarget(stateDir: stateDir.path, modelRef: modelRef)
        XCTAssertEqual(target?.port, 31002)
    }

    func testResolveModelRunnerFailsClosedForMissingOrDeadRunner() throws {
        let stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
        let runnersDir = stateDir.appendingPathComponent("runners")
        try FileManager.default.createDirectory(at: runnersDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: stateDir) }

        XCTAssertNil(resolveModelRunnerTarget(stateDir: stateDir.path, modelRef: "missing"))

        let data = try JSONSerialization.data(withJSONObject: [
            "runners": [[
                "model_ref": "model",
                "host": "127.0.0.1",
                "port": 31001,
                "pid": Int32.max,
            ]],
        ])
        try data.write(to: runnersDir.appendingPathComponent("index.json"))
        XCTAssertNil(resolveModelRunnerTarget(stateDir: stateDir.path, modelRef: "model"))
    }
}
