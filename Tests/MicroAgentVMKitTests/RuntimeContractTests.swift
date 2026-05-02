import Foundation
import Testing
@testable import MicroAgentVMKit

@Test func runtimeIdentityRoundTripsThroughJSON() throws {
    let identity = RuntimeIdentity(
        requestID: "req-1",
        runtimeID: "agent-1",
        role: .workload,
        backend: "apple-vf",
        homeHash: "abc123"
    )

    let data = try JSONEncoder().encode(identity)
    let decoded = try JSONDecoder().decode(RuntimeIdentity.self, from: data)

    #expect(decoded == identity)
}

@Test func prepareValidatesRequiredPaths() async throws {
    let driver = NullRuntimeDriver()
    let identity = RuntimeIdentity(
        requestID: "req-1",
        runtimeID: "agent-1",
        role: .workload,
        backend: "null"
    )
    let config = VMConfig(kernelPath: "", rootfsPath: "/tmp/rootfs.ext4", stateDir: "/tmp/state")

    await #expect(throws: RuntimeDriverError.invalidConfiguration("kernelPath is required")) {
        _ = try await driver.prepare(identity: identity, config: config)
    }
}
