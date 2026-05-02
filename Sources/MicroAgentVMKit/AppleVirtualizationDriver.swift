import Foundation

#if canImport(Darwin)
import Darwin
#endif

#if canImport(Virtualization)
@preconcurrency import Virtualization
#endif

public struct AppleVirtualizationHostSupport: Codable, Equatable, Sendable {
    public var backend: String
    public var architecture: String
    public var frameworkAvailable: Bool
    public var virtualizationSupported: Bool

    public var ok: Bool {
        frameworkAvailable && virtualizationSupported
    }
}

public enum AppleVirtualizationHost {
    public static let backendName = "apple-vf"

    public static func support() -> AppleVirtualizationHostSupport {
        #if canImport(Virtualization)
        let available = true
        let supported: Bool
        if #available(macOS 13.0, *) {
            supported = VZVirtualMachine.isSupported
        } else {
            supported = false
        }
        #else
        let available = false
        let supported = false
        #endif

        return AppleVirtualizationHostSupport(
            backend: backendName,
            architecture: hostArchitecture(),
            frameworkAvailable: available,
            virtualizationSupported: supported
        )
    }
}

public struct AppleVirtualizationDriver: RuntimeDriver {
    public let stateDir: String

    public init(stateDir: String) {
        self.stateDir = stateDir
    }

    public func prepare(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent {
        try validate(identity: identity, config: config)
        let event = RuntimeEvent(identity: identity, state: .prepared)
        try write(event: event, config: config)
        return event
    }

    public func start(identity: RuntimeIdentity, config: VMConfig) async throws -> RuntimeEvent {
        try validate(identity: identity, config: config)
        let support = AppleVirtualizationHost.support()
        guard support.ok else {
            throw RuntimeDriverError.invalidConfiguration("Apple Virtualization is not available on this host")
        }
        let event = RuntimeEvent(identity: identity, state: .starting)
        try write(event: event, config: config)
        throw RuntimeDriverError.notImplemented("Apple Virtualization start is not implemented")
    }

    public func stop(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        try validateRuntimeIdentity(identity)
        let event = RuntimeEvent(identity: identity, state: .stopped)
        try write(event: event, config: nil)
        return event
    }

    public func kill(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        try validateRuntimeIdentity(identity)
        let event = RuntimeEvent(identity: identity, state: .stopped, detail: "forced")
        try write(event: event, config: nil)
        return event
    }

    public func inspect(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        try validateRuntimeIdentity(identity)
        return try readEvent(identity: identity) ?? RuntimeEvent(identity: identity, state: .unknown)
    }

    public func delete(identity: RuntimeIdentity) async throws -> RuntimeEvent {
        try validateRuntimeIdentity(identity)
        try FileManager.default.removeItem(at: runtimeDirectory(identity: identity))
        return RuntimeEvent(identity: identity, state: .stopped, detail: "deleted")
    }

    private func validate(identity: RuntimeIdentity, config: VMConfig) throws {
        try validateRuntimeIdentity(identity)
        try validateRuntimeConfig(config)
        if identity.backend != AppleVirtualizationHost.backendName {
            throw RuntimeDriverError.invalidConfiguration("backend must be \(AppleVirtualizationHost.backendName)")
        }
    }

    private func runtimeDirectory(identity: RuntimeIdentity) -> URL {
        URL(fileURLWithPath: stateDir).appendingPathComponent(identity.runtimeID, isDirectory: true)
    }

    private func eventPath(identity: RuntimeIdentity) -> URL {
        runtimeDirectory(identity: identity).appendingPathComponent("event.json")
    }

    private func configPath(identity: RuntimeIdentity) -> URL {
        runtimeDirectory(identity: identity).appendingPathComponent("config.json")
    }

    private func write(event: RuntimeEvent, config: VMConfig?) throws {
        let directory = runtimeDirectory(identity: event.identity)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        try encoder.encode(event).write(to: eventPath(identity: event.identity), options: .atomic)
        if let config {
            try encoder.encode(config).write(to: configPath(identity: event.identity), options: .atomic)
        }
    }

    private func readEvent(identity: RuntimeIdentity) throws -> RuntimeEvent? {
        let path = eventPath(identity: identity)
        guard FileManager.default.fileExists(atPath: path.path) else {
            return nil
        }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try decoder.decode(RuntimeEvent.self, from: Data(contentsOf: path))
    }
}

private func hostArchitecture() -> String {
    #if canImport(Darwin)
    var uts = utsname()
    uname(&uts)
    return withUnsafePointer(to: &uts.machine) {
        $0.withMemoryRebound(to: CChar.self, capacity: 1) {
            String(cString: $0)
        }
    }
    #else
    return "unknown"
    #endif
}
