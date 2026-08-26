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
                    "key": "runner-key",
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
        var resolution = resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: "runner-key", modelRef: modelRef)
        XCTAssertEqual(resolution?.target.host, "127.0.0.1")
        XCTAssertEqual(resolution?.target.port, 31001)
        XCTAssertEqual(resolution?.usedFallback, false)

        try writeIndex(port: 31002)
        resolution = resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: "runner-key", modelRef: modelRef)
        XCTAssertEqual(resolution?.target.port, 31002)
    }

    func testResolveModelRunnerFailsClosedForMissingOrDeadRunner() throws {
        let stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
        let runnersDir = stateDir.appendingPathComponent("runners")
        try FileManager.default.createDirectory(at: runnersDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: stateDir) }

        XCTAssertNil(resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: nil, modelRef: "missing"))

        let data = try JSONSerialization.data(withJSONObject: [
            "runners": [[
                "model_ref": "model",
                "host": "127.0.0.1",
                "port": 31001,
                "pid": Int32.max,
            ]],
        ])
        try data.write(to: runnersDir.appendingPathComponent("index.json"))
        XCTAssertNil(resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: nil, modelRef: "model"))
    }

    func testResolveModelRunnerPrefersExactKeyAndReportsFallback() throws {
        let stateDir = FileManager.default.temporaryDirectory
            .appendingPathComponent(UUID().uuidString)
        let runnersDir = stateDir.appendingPathComponent("runners")
        try FileManager.default.createDirectory(at: runnersDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: stateDir) }

        let modelRef = "hf.co/example/model@main/model.gguf"
        let data = try JSONSerialization.data(withJSONObject: [
            "runners": [
                ["key": "wrong", "model_ref": modelRef, "host": "127.0.0.1", "port": 31001, "pid": ProcessInfo.processInfo.processIdentifier],
                ["key": "paired", "model_ref": modelRef, "host": "127.0.0.1", "port": 31002, "pid": ProcessInfo.processInfo.processIdentifier],
            ],
        ])
        try data.write(to: runnersDir.appendingPathComponent("index.json"))

        let exact = resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: "paired", modelRef: modelRef)
        XCTAssertEqual(exact?.target.port, 31002)
        XCTAssertEqual(exact?.usedFallback, false)

        let fallback = resolveModelRunnerTarget(stateDir: stateDir.path, modelRunnerKey: "missing", modelRef: modelRef)
        XCTAssertEqual(fallback?.target.port, 31001)
        XCTAssertEqual(fallback?.usedFallback, true)
    }
}
