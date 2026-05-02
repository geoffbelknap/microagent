import Foundation
import Testing
@testable import MicroAgentVMKit

@Test func appleVirtualizationPreparePersistsInspectableState() async throws {
    let stateDir = FileManager.default.temporaryDirectory
        .appendingPathComponent("microagent-vmkit-tests-\(UUID().uuidString)", isDirectory: true)
    defer {
        try? FileManager.default.removeItem(at: stateDir)
    }
    let driver = AppleVirtualizationDriver(stateDir: stateDir.path)
    let identity = RuntimeIdentity(
        requestID: "req-1",
        runtimeID: "agent-1",
        role: .workload,
        backend: AppleVirtualizationHost.backendName
    )
    let config = VMConfig(
        kernelPath: "/tmp/kernel",
        rootfsPath: "/tmp/rootfs.ext4",
        stateDir: stateDir.path
    )

    let prepared = try await driver.prepare(identity: identity, config: config)
    let inspected = try await driver.inspect(identity: identity)

    #expect(prepared.state == .prepared)
    #expect(inspected.state == .prepared)
    #expect(inspected.identity == identity)
}

@Test func appleVirtualizationDriverRejectsWrongBackend() async throws {
    let driver = AppleVirtualizationDriver(stateDir: FileManager.default.temporaryDirectory.path)
    let identity = RuntimeIdentity(
        requestID: "req-1",
        runtimeID: "agent-1",
        role: .workload,
        backend: "other"
    )
    let config = VMConfig(
        kernelPath: "/tmp/kernel",
        rootfsPath: "/tmp/rootfs.ext4",
        stateDir: "/tmp/state"
    )

    await #expect(throws: RuntimeDriverError.invalidConfiguration("backend must be apple-vf")) {
        _ = try await driver.prepare(identity: identity, config: config)
    }
}
