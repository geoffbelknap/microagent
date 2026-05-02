import Foundation

#if canImport(Darwin)
import Darwin
#endif

#if canImport(Virtualization)
@preconcurrency import Virtualization
#endif

enum ComponentRole: String, Codable {
    case workload
    case enforcer
}

enum VMState: String, Codable {
    case unknown
    case prepared
    case starting
    case running
    case stopped
}

struct Identity: Codable {
    var requestID: String
    var runtimeID: String
    var role: ComponentRole
    var backend: String
    var homeHash: String?
}

struct Config: Codable {
    var kernelPath: String
    var rootfsPath: String
    var stateDir: String
    var memoryMiB: Int?
    var cpuCount: Int?
    var vsockListeners: [VsockListener]?
}

struct VsockListener: Codable {
    var port: UInt32
    var target: String
}

struct Request: Codable {
    var command: String
    var identity: Identity?
    var config: Config?
}

struct Event: Codable {
    var identity: Identity
    var state: VMState
    var detail: String?
    var observedAt: Date
}

struct HostSupport: Codable {
    var backend: String
    var architecture: String
    var frameworkAvailable: Bool
    var virtualizationSupported: Bool
}

struct Response: Codable {
    var ok: Bool
    var backend: String?
    var event: Event?
    var host: HostSupport?
    var error: String?
}

let backendName = "apple-vf"
let decoder = JSONDecoder()
decoder.dateDecodingStrategy = .iso8601
let encoder = JSONEncoder()
encoder.dateEncodingStrategy = .iso8601
encoder.outputFormatting = [.prettyPrinted, .sortedKeys]

func main() -> Int32 {
    do {
        let request = try readRequest()
        let response = try handle(request)
        write(response)
        return response.ok ? 0 : 1
    } catch {
        write(Response(ok: false, backend: backendName, error: String(describing: error)))
        return 1
    }
}

func readRequest() throws -> Request {
    let args = Array(CommandLine.arguments.dropFirst())
    if args.count == 2 && args[0] == "--request" {
        return try decoder.decode(Request.self, from: Data(contentsOf: URL(fileURLWithPath: args[1])))
    }
    let data = FileHandle.standardInput.readDataToEndOfFile()
    if data.isEmpty {
        throw ProtocolError.invalid("request JSON is required on stdin or with --request")
    }
    return try decoder.decode(Request.self, from: data)
}

func handle(_ request: Request) throws -> Response {
    switch request.command {
    case "host":
        return Response(ok: true, backend: backendName, host: hostSupport())
    case "check":
        let identity = try validatedIdentity(request.identity)
        let _ = try validatedConfig(request.config)
        return Response(ok: true, backend: backendName, event: Event(identity: identity, state: .prepared, detail: "validated", observedAt: Date()))
    case "prepare":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        let event = Event(identity: identity, state: .prepared, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        return Response(ok: true, backend: backendName, event: event)
    case "start":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        let support = hostSupport()
        guard support.frameworkAvailable && support.virtualizationSupported else {
            return Response(ok: false, backend: backendName, error: "Apple Virtualization is not available on this host")
        }
        let event = Event(identity: identity, state: .starting, detail: nil, observedAt: Date())
        try writeState(event: event, config: config)
        return Response(ok: false, backend: backendName, event: event, error: "Apple VF start is not implemented")
    case "inspect":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        let event = try readEvent(identity: identity, stateDir: config.stateDir) ?? Event(identity: identity, state: .unknown, detail: nil, observedAt: Date())
        return Response(ok: true, backend: backendName, event: event)
    case "stop":
        return try stateOnly(request, state: .stopped, detail: nil)
    case "kill":
        return try stateOnly(request, state: .stopped, detail: "forced")
    case "delete":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        let dir = runtimeDirectory(identity: identity, stateDir: config.stateDir)
        if FileManager.default.fileExists(atPath: dir.path) {
            try FileManager.default.removeItem(at: dir)
        }
        return Response(ok: true, backend: backendName, event: Event(identity: identity, state: .stopped, detail: "deleted", observedAt: Date()))
    default:
        throw ProtocolError.invalid("unknown command: \(request.command)")
    }
}

func stateOnly(_ request: Request, state: VMState, detail: String?) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    let event = Event(identity: identity, state: state, detail: detail, observedAt: Date())
    try writeState(event: event, config: config)
    return Response(ok: true, backend: backendName, event: event)
}

func validatedIdentity(_ identity: Identity?) throws -> Identity {
    guard let identity else {
        throw ProtocolError.invalid("identity is required")
    }
    if identity.requestID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("identity.requestID is required")
    }
    if identity.runtimeID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("identity.runtimeID is required")
    }
    if identity.backend != backendName {
        throw ProtocolError.invalid("identity.backend must be \(backendName)")
    }
    return identity
}

func validatedConfig(_ config: Config?) throws -> Config {
    var config = try stateConfig(config)
    if config.kernelPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.kernelPath is required")
    }
    if config.rootfsPath.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.rootfsPath is required")
    }
    if config.memoryMiB == nil {
        config.memoryMiB = 512
    }
    if config.cpuCount == nil {
        config.cpuCount = 2
    }
    if config.memoryMiB ?? 0 <= 0 {
        throw ProtocolError.invalid("config.memoryMiB must be positive")
    }
    if config.cpuCount ?? 0 <= 0 {
        throw ProtocolError.invalid("config.cpuCount must be positive")
    }
    var ports = Set<UInt32>()
    for listener in config.vsockListeners ?? [] {
        if listener.port == 0 {
            throw ProtocolError.invalid("vsock listener port must be positive")
        }
        if !ports.insert(listener.port).inserted {
            throw ProtocolError.invalid("duplicate vsock listener port \(listener.port)")
        }
        if listener.target.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("vsock listener \(listener.port) target is required")
        }
    }
    return config
}

func stateConfig(_ config: Config?) throws -> Config {
    guard let config else {
        throw ProtocolError.invalid("config is required")
    }
    if config.stateDir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("config.stateDir is required")
    }
    return config
}

func hostSupport() -> HostSupport {
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
    return HostSupport(
        backend: backendName,
        architecture: hostArchitecture(),
        frameworkAvailable: available,
        virtualizationSupported: supported
    )
}

func runtimeDirectory(identity: Identity, stateDir: String) -> URL {
    URL(fileURLWithPath: stateDir).appendingPathComponent(identity.runtimeID, isDirectory: true)
}

func eventPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("event.json")
}

func configPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("config.json")
}

func writeState(event: Event, config: Config) throws {
    let directory = runtimeDirectory(identity: event.identity, stateDir: config.stateDir)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try encoder.encode(event).write(to: eventPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
    try encoder.encode(config).write(to: configPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
}

func readEvent(identity: Identity, stateDir: String) throws -> Event? {
    let path = eventPath(identity: identity, stateDir: stateDir)
    guard FileManager.default.fileExists(atPath: path.path) else {
        return nil
    }
    return try decoder.decode(Event.self, from: Data(contentsOf: path))
}

func write(_ response: Response) {
    do {
        FileHandle.standardOutput.write(try encoder.encode(response))
        FileHandle.standardOutput.write(Data("\n".utf8))
    } catch {
        FileHandle.standardError.write(Data("\(error)\n".utf8))
    }
}

func hostArchitecture() -> String {
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

enum ProtocolError: Error, CustomStringConvertible {
    case invalid(String)

    var description: String {
        switch self {
        case .invalid(let message):
            return message
        }
    }
}

exit(main())
