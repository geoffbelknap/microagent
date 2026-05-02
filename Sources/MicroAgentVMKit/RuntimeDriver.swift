import Foundation

public protocol RuntimeDriver: Sendable {
    func prepare(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent
    func start(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent
    func stop(identity: RuntimeIdentity) async throws -> RuntimeEvent
    func kill(identity: RuntimeIdentity) async throws -> RuntimeEvent
    func inspect(identity: RuntimeIdentity) async throws -> RuntimeEvent
    func delete(identity: RuntimeIdentity) async throws -> RuntimeEvent
}

public enum RuntimeDriverError: Error, Equatable, Sendable {
    case notImplemented(String)
    case invalidConfiguration(String)
}

public struct NullRuntimeDriver: RuntimeDriver {
    public init() {}

    public func prepare(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent {
        try validateRuntimeConfig(config)
        return RuntimeEvent(identity: identity, state: .prepared)
    }

    public func start(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent {
        try validateRuntimeConfig(config)
        throw RuntimeDriverError.notImplemented("start is not implemented for NullRuntimeDriver")
    }

    public func stop(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        RuntimeEvent(identity: identity, state: .stopped)
    }

    public func kill(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        RuntimeEvent(identity: identity, state: .stopped, detail: "forced")
    }

    public func inspect(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        RuntimeEvent(identity: identity, state: .unknown)
    }

    public func delete(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        RuntimeEvent(identity: identity, state: .stopped, detail: "deleted")
    }

}

public func validateRuntimeConfig(_ config: VMConfig) throws {
    if config.kernelPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("kernelPath is required")
    }
    if config.rootfsPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("rootfsPath is required")
    }
    if config.stateDir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("stateDir is required")
    }
    if config.memoryMiB <= 0 {
        throw RuntimeDriverError.invalidConfiguration("memoryMiB must be positive")
    }
    if config.cpuCount <= 0 {
        throw RuntimeDriverError.invalidConfiguration("cpuCount must be positive")
    }
    var ports = Set<UInt32>()
    for listener in config.vsockListeners {
        if listener.port == 0 {
            throw RuntimeDriverError.invalidConfiguration("vsock listener port must be positive")
        }
        if !ports.insert(listener.port).inserted {
            throw RuntimeDriverError.invalidConfiguration("duplicate vsock listener port \(listener.port)")
        }
        if listener.target.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw RuntimeDriverError.invalidConfiguration("vsock listener \(listener.port) target is required")
        }
    }
}

public func validateRuntimeIdentity(_ identity: RuntimeIdentity) throws {
    if identity.requestID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("requestID is required")
    }
    if identity.runtimeID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("runtimeID is required")
    }
    if identity.backend.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw RuntimeDriverError.invalidConfiguration("backend is required")
    }
}
