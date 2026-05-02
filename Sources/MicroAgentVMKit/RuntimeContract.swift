import Foundation

public enum ComponentRole: String, Codable, Sendable {
    case workload
    case enforcer
}

public enum VMState: String, Codable, Sendable {
    case unknown
    case prepared
    case starting
    case running
    case stopping
    case stopped
    case failed
}

public struct RuntimeIdentity: Codable, Equatable, Sendable {
    public var requestID: String
    public var runtimeID: String
    public var role: ComponentRole
    public var backend: String
    public var homeHash: String?

    public init(
        requestID: String,
        runtimeID: String,
        role: ComponentRole,
        backend: String,
        homeHash: String? = nil
    ) {
        self.requestID = requestID
        self.runtimeID = runtimeID
        self.role = role
        self.backend = backend
        self.homeHash = homeHash
    }
}

public struct VMConfig: Codable, Equatable, Sendable {
    public var kernelPath: String
    public var rootfsPath: String
    public var stateDir: String
    public var memoryMiB: Int
    public var cpuCount: Int
    public var vsockListeners: [VsockListener]

    public init(
        kernelPath: String,
        rootfsPath: String,
        stateDir: String,
        memoryMiB: Int = 512,
        cpuCount: Int = 2,
        vsockListeners: [VsockListener] = []
    ) {
        self.kernelPath = kernelPath
        self.rootfsPath = rootfsPath
        self.stateDir = stateDir
        self.memoryMiB = memoryMiB
        self.cpuCount = cpuCount
        self.vsockListeners = vsockListeners
    }

    enum CodingKeys: String, CodingKey {
        case kernelPath
        case rootfsPath
        case stateDir
        case memoryMiB
        case cpuCount
        case vsockListeners
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        self.kernelPath = try container.decode(String.self, forKey: .kernelPath)
        self.rootfsPath = try container.decode(String.self, forKey: .rootfsPath)
        self.stateDir = try container.decode(String.self, forKey: .stateDir)
        self.memoryMiB = try container.decodeIfPresent(Int.self, forKey: .memoryMiB) ?? 512
        self.cpuCount = try container.decodeIfPresent(Int.self, forKey: .cpuCount) ?? 2
        self.vsockListeners = try container.decodeIfPresent([VsockListener].self, forKey: .vsockListeners) ?? []
    }
}

public struct VsockListener: Codable, Equatable, Sendable {
    public var port: UInt32
    public var target: String

    public init(port: UInt32, target: String) {
        self.port = port
        self.target = target
    }
}

public struct RuntimeEvent: Codable, Equatable, Sendable {
    public var identity: RuntimeIdentity
    public var state: VMState
    public var detail: String?
    public var observedAt: Date

    public init(
        identity: RuntimeIdentity,
        state: VMState,
        detail: String? = nil,
        observedAt: Date = Date()
    ) {
        self.identity = identity
        self.state = state
        self.detail = detail
        self.observedAt = observedAt
    }
}
