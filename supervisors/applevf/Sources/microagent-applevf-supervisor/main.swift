import Foundation
import Security

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
    case halted
    case quarantined
    case failed
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
    var disks: [Disk]?
    var vsockListeners: [VsockListener]?
    var mediation: MediationConfig?
    var network: NetworkConfig?
    var serialInput: Bool?
}

struct MediationConfig: Codable {
    var enabled: Bool
    var required: Bool
    var port: UInt32?
    var target: String?
    var failClosed: Bool
}

struct NetworkConfig: Codable {
    var mode: String
    var interface: String?
    var portForwards: [PortForward]?
    var dns: [String]?
    var routes: [String]?
    var ip: String?
}

struct PortForward: Codable {
    var protocolName: String
    var host: String?
    var hostPort: UInt16
    var guestPort: UInt16

    enum CodingKeys: String, CodingKey {
        case protocolName = "protocol"
        case host
        case hostPort
        case guestPort
    }
}

struct Disk: Codable {
    var name: String
    var path: String
    var mountpoint: String
    var mode: String
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
    var supervisorPath: String?
    var supervisorAvailable: Bool?
    var consoleAvailable: Bool?
    var consoleMode: String?
}

struct ReadinessSignal: Codable {
    var ready: Bool? = nil
    var observedAt: Date? = nil
    var detail: String? = nil
    var error: String? = nil
}

struct RuntimeReadiness: Codable {
    var guestReady: ReadinessSignal
    var shellReady: ReadinessSignal
    var resultReady: ReadinessSignal
    var mediationReady: ReadinessSignal
}

struct RuntimeResult: Codable {
    var identity: Identity
    var backend: String?
    var resultPath: String?
    var startedAt: String?
    var completedAt: String?
    var exitCode: Int
    var stdout: String?
    var stderr: String?
    var error: String?
}

struct GuestResult: Codable {
    var startedAt: String?
    var exitedAt: String?
    var exitCode: Int
    var stdout: String?
    var stderr: String?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case startedAt = "started_at"
        case exitedAt = "exited_at"
        case exitCode = "exit_code"
        case stdout
        case stderr
        case error
    }
}

struct Response: Codable {
    var ok: Bool
    var backend: String? = nil
    var event: Event? = nil
    var host: HostSupport? = nil
    var readiness: RuntimeReadiness? = nil
    var result: RuntimeResult? = nil
    var mediation: MediationConfig? = nil
    var network: NetworkConfig? = nil
    var error: String? = nil
}

let backendName = "apple-vf"
let eventFileName = "event.json"
let eventsFileName = "events.json"
let configFileName = "config.json"
let runtimeFileName = "runtime.json"
let serialLogFileName = "serial.log"
let serialInputFileName = "serial.in"
let supervisorLogFileName = "supervisor.log"
let quarantineAckFileName = "quarantine.ack.json"
let quarantineControlSignal = SIGUSR1
let maxSocketConnections = 128
let maxResultSocketBytes = 16 * 1024 * 1024
let decoder = JSONDecoder()
decoder.dateDecodingStrategy = .iso8601
let encoder = JSONEncoder()
encoder.dateEncodingStrategy = .iso8601
encoder.outputFormatting = [.prettyPrinted, .sortedKeys]

struct RuntimeState: Codable {
    var event: Event
    var config: Config
    var pid: Int32?
    var serialLogPath: String
    var serialInputPath: String?
    var startedAt: Date?
    var updatedAt: Date
    var readiness: RuntimeReadiness?
    var error: String?
}

func main() -> Int32 {
    do {
        let request = try readRequest()
        if request.command == "console" {
            try runConsole(request)
            return 0
        }
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
    if args.count == 2 && args[0] == "--request-json" {
        return try decoder.decode(Request.self, from: Data(args[1].utf8))
    }
    let data = FileHandle.standardInput.readDataToEndOfFile()
    if data.isEmpty {
        throw ProtocolError.invalid("request JSON is required on stdin or with --request")
    }
    return try decoder.decode(Request.self, from: data)
}

func handle(_ request: Request) throws -> Response {
    switch request.command {
    case "run":
        try runVM(request)
        return Response(ok: true, backend: backendName)
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
        try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
        return response(event: event, config: config, error: nil)
    case "start":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        try ensureCanStart(identity: identity, stateDir: config.stateDir)
        let support = hostSupport()
        guard support.frameworkAvailable && support.virtualizationSupported else {
            return Response(ok: false, backend: backendName, error: "Apple Virtualization is not available on this host")
        }
        #if canImport(Virtualization)
        if #available(macOS 13.0, *) {
            let vmConfig = try virtualMachineConfiguration(identity: identity, config: config, serialMode: .detached)
            try vmConfig.validate()
        }
        #endif
        let event = Event(identity: identity, state: .starting, detail: "serial=\(serialLogPath(identity: identity, stateDir: config.stateDir).path)", observedAt: Date())
        try writeState(event: event, config: config)
        let process = Process()
        process.executableURL = URL(fileURLWithPath: currentExecutablePath())
        process.arguments = ["--request-json", try requestJSON(request.withCommand("run"))]
        process.standardInput = FileHandle.nullDevice
        FileManager.default.createFile(atPath: supervisorLogPath(identity: identity, stateDir: config.stateDir).path, contents: nil)
        let supervisorLog = try FileHandle(forWritingTo: supervisorLogPath(identity: identity, stateDir: config.stateDir))
        process.standardOutput = supervisorLog
        process.standardError = supervisorLog
        try process.run()
        try writeRuntimeState(event: event, config: config, pid: process.processIdentifier, error: nil)
        return response(event: event, config: config, error: nil)
    case "inspect":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        var event = try readEvent(identity: identity, stateDir: config.stateDir) ?? Event(identity: identity, state: .unknown, detail: nil, observedAt: Date())
        if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), !processAlive(runtime.pid), event.state == .starting || event.state == .running {
            event = Event(identity: event.identity, state: .stopped, detail: event.detail, observedAt: Date())
            try writeState(event: event, config: runtime.config)
            try writeRuntimeState(event: event, config: runtime.config, pid: nil, error: runtime.error)
        }
        let runtimeConfig = try readRuntimeState(identity: identity, stateDir: config.stateDir)?.config ?? config
        return response(event: event, config: runtimeConfig, error: nil)
    case "stop":
        return try stateOnly(request, state: .stopped, detail: nil)
    case "halt":
        return try stateOnly(request, state: .halted, detail: nil)
    case "quarantine":
        return try quarantine(request)
    case "kill":
        return try stateOnly(request, state: .stopped, detail: "forced")
    case "delete":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        try ensureCanDelete(identity: identity, stateDir: config.stateDir)
        let dir = runtimeDirectory(identity: identity, stateDir: config.stateDir)
        if FileManager.default.fileExists(atPath: dir.path) {
            try FileManager.default.removeItem(at: dir)
        }
        return Response(ok: true, backend: backendName, event: Event(identity: identity, state: .stopped, detail: "deleted", observedAt: Date()))
    default:
        throw ProtocolError.invalid("unknown command: \(request.command)")
    }
}

func quarantine(_ request: Request) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    let detail = "host-side network, mediation, and serial input severed"
    let event = Event(identity: identity, state: .quarantined, detail: detail, observedAt: Date())
    if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid {
        let ack = quarantineAckPath(identity: identity, stateDir: runtime.config.stateDir)
        try? FileManager.default.removeItem(at: ack)
        if kill(pid, quarantineControlSignal) != 0 && errno != ESRCH {
            throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
        }
        try waitForQuarantineAck(path: ack, timeout: 2.0)
        try writeState(event: event, config: runtime.config)
        try writeRuntimeState(event: event, config: runtime.config, pid: pid, error: nil)
        return response(event: event, config: runtime.config, error: nil)
    }
    try writeState(event: event, config: config)
    try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
    return response(event: event, config: config, error: nil)
}

func waitForQuarantineAck(path: URL, timeout: TimeInterval) throws {
    let deadline = Date().addingTimeInterval(timeout)
    while Date() < deadline {
        if FileManager.default.fileExists(atPath: path.path) {
            return
        }
        usleep(20_000)
    }
    throw ProtocolError.invalid("apple-vf quarantine control did not acknowledge before timeout")
}

func stateOnly(_ request: Request, state: VMState, detail: String?) throws -> Response {
    let identity = try validatedIdentity(request.identity)
    let config = try stateConfig(request.config)
    let event = Event(identity: identity, state: state, detail: detail, observedAt: Date())
    if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid {
        let signal = detail == "forced" ? SIGKILL : SIGTERM
        if kill(pid, signal) != 0 && errno != ESRCH {
            throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
        }
        try writeState(event: event, config: runtime.config)
        try writeRuntimeState(event: event, config: runtime.config, pid: nil, error: nil)
        return response(event: event, config: runtime.config, error: nil)
    } else {
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: nil, error: nil)
        return response(event: event, config: config, error: nil)
    }
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
    if !isSafeIdentifier(identity.runtimeID) {
        throw ProtocolError.invalid("identity.runtimeID must be a safe basename")
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
    var diskNames = Set<String>()
    var diskMountpoints = Set<String>()
    for disk in config.disks ?? [] {
        if disk.name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("disk name is required")
        }
        if disk.name == "rootfs" {
            throw ProtocolError.invalid("disk name rootfs is reserved")
        }
        if !diskNames.insert(disk.name).inserted {
            throw ProtocolError.invalid("duplicate disk name \(disk.name)")
        }
        if disk.path.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            throw ProtocolError.invalid("disk \(disk.name) path is required")
        }
        if disk.mountpoint.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !disk.mountpoint.hasPrefix("/") {
            throw ProtocolError.invalid("disk \(disk.name) mountpoint must be absolute")
        }
        if !diskMountpoints.insert(disk.mountpoint).inserted {
            throw ProtocolError.invalid("duplicate disk mountpoint \(disk.mountpoint)")
        }
        if disk.mode != "ro" && disk.mode != "rw" {
            throw ProtocolError.invalid("disk \(disk.name) mode must be ro or rw")
        }
        try readableFile(disk.path, name: "disk \(disk.name) path")
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
    try validateNetworkConfig(config.network)
    try validateMediationConfig(config.mediation)
    try readableFile(config.kernelPath, name: "config.kernelPath")
    try readableFile(config.rootfsPath, name: "config.rootfsPath")
    return config
}

func validateMediationConfig(_ mediation: MediationConfig?) throws {
    guard let mediation, mediation.enabled else {
        return
    }
    if mediation.required && !mediation.failClosed {
        throw ProtocolError.invalid("required mediation must set failClosed=true")
    }
    if mediation.port == nil || mediation.port == 0 {
        throw ProtocolError.invalid("mediation.port is required when mediation is enabled")
    }
    if (mediation.target ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        throw ProtocolError.invalid("mediation.target is required when mediation is enabled")
    }
    _ = try parseTCPHostPort(mediation.target ?? "")
}

func validateNetworkConfig(_ network: NetworkConfig?) throws {
    guard let network else {
        return
    }
    let mode = normalizedNetworkMode(network)
    switch mode {
    case "user", "nat", "isolated", "bridged":
        break
    default:
        throw ProtocolError.invalid("network.mode must be user, nat, isolated, or bridged")
    }
    #if canImport(Virtualization)
    if mode == "bridged" {
        if #available(macOS 13.0, *) {
            // This code path is valid Virtualization.framework usage, but Apple
            // gates it behind a restricted entitlement that open-source projects
            // cannot self-sign into existence. Local sudo does not help.
            guard hasEntitlement("com.apple.vm.networking") else {
                throw ProtocolError.invalid("Apple VF bridged networking is blocked by Apple's restricted com.apple.vm.networking entitlement. Open-source builds cannot self-sign it, and sudo will not bypass the check.")
            }
            _ = try bridgedInterface(named: network.interface)
        }
    }
    #endif
}

func hasEntitlement(_ name: String) -> Bool {
    guard let task = SecTaskCreateFromSelf(nil),
          let value = SecTaskCopyValueForEntitlement(task, name as CFString, nil) else {
        return false
    }
    return (value as? Bool) == true
}

func normalizedNetworkMode(_ network: NetworkConfig?) -> String {
    let mode = network?.mode.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    return mode.isEmpty ? "nat" : mode
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

func ensureCanStart(identity: Identity, stateDir: String) throws {
    guard let runtime = try readRuntimeState(identity: identity, stateDir: stateDir) else {
        return
    }
    switch runtime.event.state {
    case .unknown, .prepared, .halted, .stopped, .failed:
        return
    case .quarantined:
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is quarantined; halt, stop, or kill it before start")
    case .starting, .running:
        throw ProtocolError.invalid("workspace \(identity.runtimeID) is already \(runtime.event.state.rawValue)")
    }
}

func ensureCanDelete(identity: Identity, stateDir: String) throws {
    guard let runtime = try readRuntimeState(identity: identity, stateDir: stateDir) else {
        return
    }
    if processAlive(runtime.pid) {
        throw ProtocolError.invalid("apple-vf workspace \(identity.runtimeID) is running; stop or kill it before delete")
    }
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
        virtualizationSupported: supported,
        supervisorPath: currentExecutablePath(),
        supervisorAvailable: true,
        consoleAvailable: true,
        consoleMode: "interactive"
    )
}

func runtimeDirectory(identity: Identity, stateDir: String) -> URL {
    URL(fileURLWithPath: stateDir).appendingPathComponent(identity.runtimeID, isDirectory: true)
}

func isSafeIdentifier(_ value: String) -> Bool {
    let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
    if trimmed.isEmpty || trimmed == "." || trimmed == ".." {
        return false
    }
    return !trimmed.contains("/") && !trimmed.contains("\\") && !trimmed.contains("\0")
}

func eventPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(eventFileName)
}

func configPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(configFileName)
}

func runtimePath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(runtimeFileName)
}

func serialLogPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(serialLogFileName)
}

func serialInputPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(serialInputFileName)
}

func supervisorLogPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(supervisorLogFileName)
}

func quarantineAckPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent(quarantineAckFileName)
}

func resultPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("result.json")
}

func normalizedFilePath(_ path: String) -> String {
    URL(fileURLWithPath: path).standardizedFileURL.path
}

func response(event: Event, config: Config, error: String?) -> Response {
    var response = Response(
        ok: event.state != .failed,
        backend: backendName,
        event: event,
        host: nil,
        readiness: readiness(event: event, config: config),
        result: try? readRuntimeResult(identity: event.identity, stateDir: config.stateDir),
        mediation: config.mediation,
        network: config.network,
        error: error
    )
    if let error, !error.isEmpty {
        response.ok = false
    }
    return response
}

func readiness(event: Event, config: Config) -> RuntimeReadiness {
    var readiness = RuntimeReadiness(
        guestReady: ReadinessSignal(),
        shellReady: ReadinessSignal(),
        resultReady: ReadinessSignal(),
        mediationReady: ReadinessSignal()
    )
    if event.state == .running || event.state == .halted || event.state == .stopped || event.state == .quarantined {
        readiness.guestReady = ReadinessSignal(ready: true, observedAt: event.observedAt, detail: "workspace reached runtime state \(event.state.rawValue)", error: nil)
    }
    if event.state == .running, config.serialInput == true {
        let path = serialInputPath(identity: event.identity, stateDir: config.stateDir)
        if FileManager.default.fileExists(atPath: path.path) {
            readiness.shellReady = ReadinessSignal(ready: true, observedAt: fileModTime(path), detail: "console input is available", error: nil)
        }
    }
    let path = resultPath(identity: event.identity, stateDir: config.stateDir)
    if FileManager.default.fileExists(atPath: path.path) {
        readiness.resultReady = ReadinessSignal(ready: true, observedAt: fileModTime(path), detail: "guest result is available", error: nil)
    }
    if let mediation = config.mediation, mediation.enabled {
        let ready = event.state == .running
        readiness.mediationReady = ReadinessSignal(
            ready: ready,
            observedAt: event.observedAt,
            detail: "mediation required=\(mediation.required) failClosed=\(mediation.failClosed) port=\(mediation.port ?? 0) target=\(mediation.target ?? "")",
            error: !ready && mediation.required ? "required mediation is not ready" : nil
        )
    }
    return readiness
}

func readRuntimeResult(identity: Identity, stateDir: String) throws -> RuntimeResult {
    let path = resultPath(identity: identity, stateDir: stateDir)
    let guest = try decoder.decode(GuestResult.self, from: Data(contentsOf: path))
    return RuntimeResult(
        identity: identity,
        backend: backendName,
        resultPath: path.path,
        startedAt: guest.startedAt,
        completedAt: guest.exitedAt,
        exitCode: guest.exitCode,
        stdout: guest.stdout,
        stderr: guest.stderr,
        error: guest.error
    )
}

func fileModTime(_ url: URL) -> Date? {
    guard let attributes = try? FileManager.default.attributesOfItem(atPath: url.path),
          let modified = attributes[.modificationDate] as? Date else {
        return nil
    }
    return modified
}

func writeState(event: Event, config: Config) throws {
    let directory = runtimeDirectory(identity: event.identity, stateDir: config.stateDir)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try encoder.encode(event).write(to: eventPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
    try appendEvent(event: event, stateDir: config.stateDir)
    try encoder.encode(config).write(to: configPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
}

func appendEvent(event: Event, stateDir: String) throws {
    let maxEvents = 1024
    let path = runtimeDirectory(identity: event.identity, stateDir: stateDir).appendingPathComponent(eventsFileName)
    var events: [Event] = []
    if FileManager.default.fileExists(atPath: path.path) {
        let data = try Data(contentsOf: path)
        if !data.isEmpty {
            events = try decoder.decode([Event].self, from: data)
        }
    }
    events.append(event)
    if events.count > maxEvents {
        events = Array(events.suffix(maxEvents))
    }
    try encoder.encode(events).write(to: path, options: .atomic)
}

func writeRuntimeState(event: Event, config: Config, pid: Int32?, error: String?) throws {
    let previous = try? readRuntimeState(identity: event.identity, stateDir: config.stateDir)
    let startedAt = event.state == .starting || event.state == .running ? Date() : previous?.startedAt
    let runtime = RuntimeState(
        event: event,
        config: config,
        pid: pid,
        serialLogPath: serialLogPath(identity: event.identity, stateDir: config.stateDir).path,
        serialInputPath: serialInputPath(identity: event.identity, stateDir: config.stateDir).path,
        startedAt: startedAt,
        updatedAt: Date(),
        readiness: readiness(event: event, config: config),
        error: error
    )
    try encoder.encode(runtime).write(to: runtimePath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
}

func readEvent(identity: Identity, stateDir: String) throws -> Event? {
    let path = eventPath(identity: identity, stateDir: stateDir)
    guard FileManager.default.fileExists(atPath: path.path) else {
        return nil
    }
    return try decoder.decode(Event.self, from: Data(contentsOf: path))
}

func readRuntimeState(identity: Identity, stateDir: String) throws -> RuntimeState? {
    let path = runtimePath(identity: identity, stateDir: stateDir)
    guard FileManager.default.fileExists(atPath: path.path) else {
        return nil
    }
    return try decoder.decode(RuntimeState.self, from: Data(contentsOf: path))
}

func requestJSON(_ request: Request) throws -> String {
    let data = try encoder.encode(request)
    return String(data: data, encoding: .utf8) ?? "{}"
}

func currentExecutablePath() -> String {
    #if canImport(Darwin)
    var size = UInt32(0)
    _ = _NSGetExecutablePath(nil, &size)
    var buffer = [CChar](repeating: 0, count: Int(size))
    if _NSGetExecutablePath(&buffer, &size) == 0 {
        let bytes = buffer.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }
        return String(decoding: bytes, as: UTF8.self)
    }
    #endif
    return CommandLine.arguments[0]
}

extension Request {
    func withCommand(_ command: String) -> Request {
        Request(command: command, identity: identity, config: config)
    }
}

func readableFile(_ path: String, name: String) throws {
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory), !isDirectory.boolValue, FileManager.default.isReadableFile(atPath: path) else {
        throw ProtocolError.invalid("\(name) is not readable at \(path)")
    }
}

func processAlive(_ pid: Int32?) -> Bool {
    guard let pid, pid > 0 else {
        return false
    }
    if kill(pid, 0) == 0 {
        return true
    }
    return errno == EPERM
}

struct TCPHostPort {
    let host: String
    let port: UInt16
}

func parseTCPHostPort(_ raw: String) throws -> TCPHostPort {
    let parts = raw.trimmingCharacters(in: .whitespacesAndNewlines).split(separator: ":", omittingEmptySubsequences: false)
    if parts.count != 2 || parts[0].isEmpty || parts[1].isEmpty {
        throw ProtocolError.invalid("target must be host:port, got \(raw)")
    }
    guard let port = UInt16(parts[1]) else {
        throw ProtocolError.invalid("target port is invalid in \(raw)")
    }
    return TCPHostPort(host: String(parts[0]), port: port)
}

#if canImport(Virtualization)
enum SerialAttachmentMode {
    case detached
    case standardIO
}

@available(macOS 13.0, *)
extension VZVirtioSocketConnection: @retroactive @unchecked Sendable {}

@available(macOS 13.0, *)
final class VMRunDelegate: NSObject, VZVirtualMachineDelegate {
    let identity: Identity
    let config: Config

    init(identity: Identity, config: Config) {
        self.identity = identity
        self.config = config
    }

    func guestDidStop(_ virtualMachine: VZVirtualMachine) {
        updateRuntime(identity: identity, config: config, state: .stopped, error: nil)
        CFRunLoopStop(CFRunLoopGetMain())
    }

    func virtualMachine(_ virtualMachine: VZVirtualMachine, didStopWithError error: Error) {
        updateRuntime(identity: identity, config: config, state: .failed, error: error.localizedDescription)
        CFRunLoopStop(CFRunLoopGetMain())
    }
}

@available(macOS 13.0, *)
final class SocketListenerHandle {
    let port: UInt32
    let listener: VZVirtioSocketListener
    let delegate: VZVirtioSocketListenerDelegate

    init(port: UInt32, listener: VZVirtioSocketListener, delegate: VZVirtioSocketListenerDelegate) {
        self.port = port
        self.listener = listener
        self.delegate = delegate
    }
}

@available(macOS 13.0, *)
protocol QuarantineClosable {
    func quarantineClose()
}

@available(macOS 13.0, *)
final class TCPPublishForwarder: @unchecked Sendable {
    private let socketDevice: VZVirtioSocketDevice
    private var listenerFDs: [Int32]
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(socketDevice: VZVirtioSocketDevice, forwards: [PortForward]) throws {
        self.socketDevice = socketDevice
        var opened: [Int32] = []
        do {
            for forward in forwards {
                opened.append(try listenTCP(forward))
            }
        } catch {
            for fd in opened {
                close(fd)
            }
            throw error
        }
        self.listenerFDs = opened
        for (idx, forward) in forwards.enumerated() {
            let fd = opened[idx]
            Thread.detachNewThread {
                self.acceptLoop(listenerFD: fd, guestVsockPort: UInt32(forward.hostPort))
            }
        }
    }

    deinit {
        quarantineClose()
    }

    func quarantineClose() {
        lock.lock()
        let fds = listenerFDs
        listenerFDs = []
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for fd in fds {
            shutdown(fd, SHUT_RDWR)
            close(fd)
        }
        for connection in retainedConnections {
            connection.close()
        }
    }

    private func acceptLoop(listenerFD: Int32, guestVsockPort: UInt32) {
        while true {
            let tcpFD = accept(listenerFD, nil, nil)
            if tcpFD < 0 {
                return
            }
            connectTCP(tcpFD, toGuestVsockPort: guestVsockPort)
        }
    }

    private func connectTCP(_ tcpFD: Int32, toGuestVsockPort guestVsockPort: UInt32) {
        let semaphore = DispatchSemaphore(value: 0)
        let resultBox = SocketConnectionResult()
        DispatchQueue.main.async {
            self.socketDevice.connect(toPort: guestVsockPort) { result in
                if case .success(let connection) = result {
                    resultBox.connection = connection
                }
                semaphore.signal()
            }
        }
        if semaphore.wait(timeout: .now() + 10) == .timedOut {
            close(tcpFD)
            return
        }
        guard let connection = resultBox.connection else {
            close(tcpFD)
            return
        }
        if !retain(connection) {
            close(tcpFD)
            connection.close()
            return
        }
        let vsockFD = connection.fileDescriptor
        Thread.detachNewThread {
            copyFD(from: tcpFD, to: vsockFD)
            shutdown(vsockFD, SHUT_WR)
            close(tcpFD)
            connection.close()
        }
        Thread.detachNewThread {
            copyFD(from: vsockFD, to: tcpFD)
            shutdown(tcpFD, SHUT_WR)
            connection.close()
            self.release(connection)
        }
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
final class SocketConnectionResult: @unchecked Sendable {
    private let lock = NSLock()
    private var stored: VZVirtioSocketConnection?

    var connection: VZVirtioSocketConnection? {
        get {
            lock.lock()
            defer { lock.unlock() }
            return stored
        }
        set {
            lock.lock()
            stored = newValue
            lock.unlock()
        }
    }
}

@available(macOS 13.0, *)
final class ResultSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let path: String
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(path: String) {
        self.path = path
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        if !retain(connection) {
            connection.close()
            return false
        }
        let fd = connection.fileDescriptor
        let path = self.path
        DispatchQueue.global(qos: .utility).async {
            defer {
                connection.close()
                self.release(connection)
            }
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while true {
                let n = read(fd, &buffer, buffer.count)
                if n > 0 {
                    if data.count + n > maxResultSocketBytes {
                        return
                    }
                    data.append(buffer, count: n)
                    continue
                }
                break
            }
            try? FileManager.default.createDirectory(at: URL(fileURLWithPath: path).deletingLastPathComponent(), withIntermediateDirectories: true)
            try? data.write(to: URL(fileURLWithPath: path), options: .atomic)
        }
        return true
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension ResultSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}

@available(macOS 13.0, *)
final class TCPSocketDelegate: NSObject, VZVirtioSocketListenerDelegate, @unchecked Sendable {
    private let target: TCPHostPort
    private let lock = NSLock()
    private var connections: [VZVirtioSocketConnection] = []

    init(target: TCPHostPort) {
        self.target = target
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        let remoteFD = dialTCP(target)
        if remoteFD < 0 {
            return false
        }
        if !retain(connection) {
            close(remoteFD)
            connection.close()
            return false
        }
        let localFD = connection.fileDescriptor
        Thread.detachNewThread {
            copyFD(from: localFD, to: remoteFD)
            shutdown(remoteFD, SHUT_WR)
            connection.close()
        }
        Thread.detachNewThread {
            copyFD(from: remoteFD, to: localFD)
            shutdown(localFD, SHUT_WR)
            close(remoteFD)
            connection.close()
            self.release(connection)
        }
        return true
    }

    private func retain(_ connection: VZVirtioSocketConnection) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if connections.count >= maxSocketConnections {
            return false
        }
        connections.append(connection)
        return true
    }

    private func release(_ connection: VZVirtioSocketConnection) {
        lock.lock()
        connections.removeAll { $0 === connection }
        lock.unlock()
    }
}

@available(macOS 13.0, *)
extension TCPSocketDelegate: QuarantineClosable {
    func quarantineClose() {
        lock.lock()
        let retainedConnections = connections
        connections = []
        lock.unlock()
        for connection in retainedConnections {
            connection.close()
        }
    }
}

@available(macOS 13.0, *)
final class QuarantineController {
    private let identity: Identity
    private let config: Config
    private let vm: VZVirtualMachine
    private let socketListeners: [SocketListenerHandle]
    private let publishForwarder: TCPPublishForwarder?
    private var source: DispatchSourceSignal?
    private var quarantined = false

    init(identity: Identity, config: Config, vm: VZVirtualMachine, socketListeners: [SocketListenerHandle], publishForwarder: TCPPublishForwarder?) {
        self.identity = identity
        self.config = config
        self.vm = vm
        self.socketListeners = socketListeners
        self.publishForwarder = publishForwarder
    }

    func start() {
        signal(quarantineControlSignal, SIG_IGN)
        let source = DispatchSource.makeSignalSource(signal: quarantineControlSignal, queue: .main)
        source.setEventHandler { [weak self] in
            self?.quarantine()
        }
        source.resume()
        self.source = source
    }

    private func quarantine() {
        if !quarantined {
            quarantined = true
            for device in vm.networkDevices {
                device.attachment = nil
            }
            if let socket = vm.socketDevices.first as? VZVirtioSocketDevice {
                for handle in socketListeners {
                    socket.removeSocketListener(forPort: handle.port)
                    (handle.delegate as? QuarantineClosable)?.quarantineClose()
                }
            }
            publishForwarder?.quarantineClose()
            try? FileManager.default.removeItem(at: serialInputPath(identity: identity, stateDir: config.stateDir))
        }
        writeQuarantineAck()
    }

    private func writeQuarantineAck() {
        let body: [String: String] = [
            "runtimeID": identity.runtimeID,
            "observedAt": ISO8601DateFormatter().string(from: Date())
        ]
        if let data = try? JSONSerialization.data(withJSONObject: body, options: [.prettyPrinted, .sortedKeys]) {
            try? data.write(to: quarantineAckPath(identity: identity, stateDir: config.stateDir), options: .atomic)
        }
    }
}
#endif

func updateRuntime(identity: Identity, config: Config, state: VMState, error: String?) {
    let event = Event(identity: identity, state: state, detail: "serial=\(serialLogPath(identity: identity, stateDir: config.stateDir).path)", observedAt: Date())
    do {
        let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir)
        try writeState(event: event, config: config)
        try writeRuntimeState(event: event, config: config, pid: runtime?.pid, error: error)
    } catch {
        // Background VM state updates cannot safely change the stdout protocol.
    }
}

func runVM(_ request: Request) throws {
    let identity = try validatedIdentity(request.identity)
    let config = try validatedConfig(request.config)
    #if canImport(Virtualization)
    guard hostSupport().virtualizationSupported else {
        throw ProtocolError.invalid("Apple Virtualization is not available on this host")
    }
    if #available(macOS 13.0, *) {
        let vmConfig = try virtualMachineConfiguration(identity: identity, config: config, serialMode: .detached)
        try vmConfig.validate()

        let vm = VZVirtualMachine(configuration: vmConfig)
        let delegate = VMRunDelegate(identity: identity, config: config)
        vm.delegate = delegate
        let socketListeners = try installSocketListeners(vm: vm, identity: identity, config: config)
        let publishForwarder = try installTCPPublishForwarder(vm: vm, config: config)
        let quarantineController = QuarantineController(identity: identity, config: config, vm: vm, socketListeners: socketListeners, publishForwarder: publishForwarder)
        quarantineController.start()
        let semaphore = DispatchSemaphore(value: 0)
        var startError: Error?
        vm.start { result in
            switch result {
            case .success:
                updateRuntime(identity: identity, config: config, state: .running, error: nil)
            case .failure(let error):
                startError = error
                updateRuntime(identity: identity, config: config, state: .failed, error: error.localizedDescription)
            }
            semaphore.signal()
        }
        while semaphore.wait(timeout: .now()) == .timedOut {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
        }
        if let startError {
            throw startError
        }
        withExtendedLifetime((delegate, socketListeners, publishForwarder, quarantineController)) {
            CFRunLoopRun()
        }
    } else {
        throw ProtocolError.invalid("Apple Virtualization requires macOS 13 or newer")
    }
    #else
    throw ProtocolError.invalid("Virtualization.framework is not available in this build")
    #endif
}

func runConsole(_ request: Request) throws {
    let identity = try validatedIdentity(request.identity)
    let config = try validatedConfig(request.config)
    #if canImport(Virtualization)
    guard hostSupport().virtualizationSupported else {
        throw ProtocolError.invalid("Apple Virtualization is not available on this host")
    }
    if #available(macOS 13.0, *) {
        let vmConfig = try virtualMachineConfiguration(identity: identity, config: config, serialMode: .standardIO)
        try vmConfig.validate()

        let vm = VZVirtualMachine(configuration: vmConfig)
        let delegate = VMRunDelegate(identity: identity, config: config)
        vm.delegate = delegate
        let socketListeners = try installSocketListeners(vm: vm, identity: identity, config: config)
        let publishForwarder = try installTCPPublishForwarder(vm: vm, config: config)
        let quarantineController = QuarantineController(identity: identity, config: config, vm: vm, socketListeners: socketListeners, publishForwarder: publishForwarder)
        quarantineController.start()
        let semaphore = DispatchSemaphore(value: 0)
        var startError: Error?
        vm.start { result in
            switch result {
            case .success:
                updateRuntime(identity: identity, config: config, state: .running, error: nil)
            case .failure(let error):
                startError = error
                updateRuntime(identity: identity, config: config, state: .failed, error: error.localizedDescription)
            }
            semaphore.signal()
        }
        while semaphore.wait(timeout: .now()) == .timedOut {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
        }
        if let startError {
            throw startError
        }
        withExtendedLifetime((delegate, socketListeners, publishForwarder, quarantineController)) {
            CFRunLoopRun()
        }
    } else {
        throw ProtocolError.invalid("Apple Virtualization requires macOS 13 or newer")
    }
    #else
    throw ProtocolError.invalid("Virtualization.framework is not available in this build")
    #endif
}

#if canImport(Virtualization)
@available(macOS 13.0, *)
func installSocketListeners(vm: VZVirtualMachine, identity: Identity, config: Config) throws -> [SocketListenerHandle] {
    guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
        return []
    }
    var handles: [SocketListenerHandle] = []
    var listeners = config.vsockListeners ?? []
    if let mediation = config.mediation, mediation.enabled, let port = mediation.port, let target = mediation.target {
        if let existing = listeners.first(where: { $0.port == port }) {
            if existing.target != target {
                throw ProtocolError.invalid("mediation port \(port) conflicts with vsock listener target \(existing.target)")
            }
        } else {
            listeners.append(VsockListener(port: port, target: target))
        }
    }
    for listenerConfig in listeners {
        let listener = VZVirtioSocketListener()
        let delegate: VZVirtioSocketListenerDelegate
        if let target = try? parseTCPHostPort(listenerConfig.target) {
            delegate = TCPSocketDelegate(target: target)
        } else {
            if normalizedFilePath(listenerConfig.target) != normalizedFilePath(resultPath(identity: identity, stateDir: config.stateDir).path) {
                throw ProtocolError.invalid("vsock listener \(listenerConfig.port) target must be host:port or the runtime result path")
            }
            delegate = ResultSocketDelegate(path: listenerConfig.target)
        }
        listener.delegate = delegate
        socket.setSocketListener(listener, forPort: listenerConfig.port)
        handles.append(SocketListenerHandle(port: listenerConfig.port, listener: listener, delegate: delegate))
    }
    return handles
}

@available(macOS 13.0, *)
func installTCPPublishForwarder(vm: VZVirtualMachine, config: Config) throws -> TCPPublishForwarder? {
    let forwards = config.network?.portForwards ?? []
    if forwards.isEmpty {
        return nil
    }
    guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
        throw ProtocolError.invalid("Apple VF publish requires a virtio socket device")
    }
    return try TCPPublishForwarder(socketDevice: socket, forwards: forwards)
}

func listenTCP(_ forward: PortForward) throws -> Int32 {
    if forward.protocolName != "" && forward.protocolName != "tcp" {
        throw ProtocolError.invalid("publish protocol must be tcp")
    }
    let host = (forward.host ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    let bindHost = host.isEmpty ? "127.0.0.1" : (host == "localhost" ? "127.0.0.1" : host)
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 {
        throw ProtocolError.invalid("open published tcp socket failed with errno \(errno)")
    }
    var yes: Int32 = 1
    setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
    var addr = sockaddr_in()
    #if os(macOS)
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    #endif
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = forward.hostPort.bigEndian
    guard inet_pton(AF_INET, bindHost, &addr.sin_addr) == 1 else {
        close(fd)
        throw ProtocolError.invalid("publish host \(bindHost) must be an IPv4 address or localhost")
    }
    let bindResult = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    if bindResult != 0 {
        let saved = errno
        close(fd)
        throw ProtocolError.invalid("listen \(bindHost):\(forward.hostPort) failed with errno \(saved)")
    }
    if listen(fd, 128) != 0 {
        let saved = errno
        close(fd)
        throw ProtocolError.invalid("listen \(bindHost):\(forward.hostPort) failed with errno \(saved)")
    }
    return fd
}

func dialTCP(_ target: TCPHostPort) -> Int32 {
    let fd = socket(AF_INET, SOCK_STREAM, 0)
    if fd < 0 {
        return -1
    }
    var addr = sockaddr_in()
    #if os(macOS)
    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
    #endif
    addr.sin_family = sa_family_t(AF_INET)
    addr.sin_port = target.port.bigEndian
    let host = target.host == "localhost" ? "127.0.0.1" : target.host
    guard inet_pton(AF_INET, host, &addr.sin_addr) == 1 else {
        close(fd)
        return -1
    }
    let result = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
        }
    }
    if result != 0 {
        close(fd)
        return -1
    }
    return fd
}

func copyFD(from source: Int32, to destination: Int32) {
    var buffer = [UInt8](repeating: 0, count: 32 * 1024)
    while true {
        let readCount = buffer.withUnsafeMutableBytes {
            read(source, $0.baseAddress, $0.count)
        }
        if readCount < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
            usleep(1_000)
            continue
        }
        if readCount <= 0 {
            return
        }
        var written = 0
        while written < readCount {
            let result = buffer.withUnsafeBytes {
                write(destination, $0.baseAddress!.advanced(by: written), readCount - written)
            }
            if result < 0 && (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
                usleep(1_000)
                continue
            }
            if result <= 0 {
                return
            }
            written += result
        }
    }
}

@available(macOS 13.0, *)
func virtualMachineConfiguration(identity: Identity, config: Config, serialMode: SerialAttachmentMode?) throws -> VZVirtualMachineConfiguration {
    let vmConfig = VZVirtualMachineConfiguration()
    vmConfig.platform = VZGenericPlatformConfiguration()
    let bootLoader = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: config.kernelPath))
    bootLoader.commandLine = linuxKernelCommandLine(for: config)
    vmConfig.bootLoader = bootLoader
    vmConfig.cpuCount = config.cpuCount ?? 2
    vmConfig.memorySize = UInt64(config.memoryMiB ?? 512) * 1024 * 1024
    let attachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: config.rootfsPath), readOnly: false)
    if let disks = config.disks, !disks.isEmpty {
        var storageDevices: [VZVirtioBlockDeviceConfiguration] = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
        for disk in disks {
            let diskAttachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: disk.path), readOnly: disk.mode == "ro")
            storageDevices.append(VZVirtioBlockDeviceConfiguration(attachment: diskAttachment))
        }
        vmConfig.storageDevices = storageDevices
    } else {
        vmConfig.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
    }
    vmConfig.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    vmConfig.networkDevices = try networkDevices(for: config)
    if let serialMode {
        let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
        switch serialMode {
        case .detached:
            try FileManager.default.createDirectory(at: runtimeDirectory(identity: identity, stateDir: config.stateDir), withIntermediateDirectories: true)
            let inputHandle: FileHandle?
            if config.serialInput == true {
                let inputURL = serialInputPath(identity: identity, stateDir: config.stateDir)
                try prepareSerialInput(path: inputURL.path)
                let inputPipe = Pipe()
                bridgeSerialInput(path: inputURL.path, to: inputPipe.fileHandleForWriting)
                inputHandle = inputPipe.fileHandleForReading
            } else {
                inputHandle = nil
            }
            FileManager.default.createFile(atPath: serialLogPath(identity: identity, stateDir: config.stateDir).path, contents: nil)
            let serialHandle = try FileHandle(forWritingTo: serialLogPath(identity: identity, stateDir: config.stateDir))
            try serialHandle.seekToEnd()
            serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: inputHandle, fileHandleForWriting: serialHandle)
        case .standardIO:
            configureRawTerminal(FileHandle.standardInput)
            serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: FileHandle.standardInput, fileHandleForWriting: FileHandle.standardOutput)
        }
        vmConfig.serialPorts = [serial]
    }
    if !(config.vsockListeners ?? []).isEmpty || config.mediation?.enabled == true || !(config.network?.portForwards ?? []).isEmpty {
        vmConfig.socketDevices = [VZVirtioSocketDeviceConfiguration()]
    }
    return vmConfig
}

func linuxKernelCommandLine(for config: Config) -> String {
    var args = ["console=hvc0", "root=/dev/vda", "rw", "init=/sbin/microagent-init"]
    switch normalizedNetworkMode(config.network) {
    case "user", "nat", "bridged":
        args.append("ip=dhcp")
    default:
        break
    }
    return args.joined(separator: " ")
}

@available(macOS 13.0, *)
func networkDevices(for config: Config) throws -> [VZVirtioNetworkDeviceConfiguration] {
    switch normalizedNetworkMode(config.network) {
    case "user", "nat":
        // Apple Virtualization.framework's VZNATNetworkDeviceAttachment runs in
        // user space inside the framework, so it already provides the
        // unprivileged outbound-only semantics that "user" mode promises on
        // Linux via pasta. Map both "user" and "nat" to it on macOS.
        let device = VZVirtioNetworkDeviceConfiguration()
        device.attachment = VZNATNetworkDeviceAttachment()
        return [device]
    case "isolated":
        return []
    case "bridged":
        let device = VZVirtioNetworkDeviceConfiguration()
        device.attachment = VZBridgedNetworkDeviceAttachment(interface: try bridgedInterface(named: config.network?.interface))
        return [device]
    default:
        throw ProtocolError.invalid("network.mode must be user, nat, isolated, or bridged")
    }
}

@available(macOS 13.0, *)
func bridgedInterface(named rawName: String?) throws -> VZBridgedNetworkInterface {
    let requested = rawName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    let interfaces = VZBridgedNetworkInterface.networkInterfaces
    guard !interfaces.isEmpty else {
        throw ProtocolError.invalid("no bridged network interfaces are available")
    }
    if requested.isEmpty {
        let available = bridgedInterfaceList(interfaces)
        throw ProtocolError.invalid("network.interface is required for bridged mode; available interfaces: \(available)")
    }
    if let match = interfaces.first(where: { $0.identifier == requested || $0.localizedDisplayName == requested }) {
        return match
    }
    let available = bridgedInterfaceList(interfaces)
    throw ProtocolError.invalid("bridged network interface \(requested) was not found; available interfaces: \(available)")
}

@available(macOS 13.0, *)
func bridgedInterfaceList(_ interfaces: [VZBridgedNetworkInterface]) -> String {
    interfaces.map { iface in
        let displayName = iface.localizedDisplayName ?? ""
        if displayName.isEmpty {
            return iface.identifier
        }
        return "\(iface.identifier)(\(displayName))"
    }.joined(separator: ", ")
}

func configureRawTerminal(_ fileHandle: FileHandle) {
    guard isatty(fileHandle.fileDescriptor) == 1 else {
        return
    }
    var attributes = termios()
    if tcgetattr(fileHandle.fileDescriptor, &attributes) == 0 {
        attributes.c_iflag &= ~tcflag_t(ICRNL)
        attributes.c_lflag &= ~tcflag_t(ICANON | ECHO)
        tcsetattr(fileHandle.fileDescriptor, TCSANOW, &attributes)
    }
}

func prepareSerialInput(path: String) throws {
    var info = stat()
    if lstat(path, &info) == 0 {
        if (info.st_mode & S_IFMT) == S_IFIFO {
            return
        }
        try FileManager.default.removeItem(atPath: path)
    } else if errno != ENOENT {
        throw ProtocolError.invalid("inspect serial input failed with errno \(errno)")
    }
    if mkfifo(path, S_IRUSR | S_IWUSR) != 0 && errno != EEXIST {
        throw ProtocolError.invalid("create serial input failed with errno \(errno)")
    }
}

func bridgeSerialInput(path: String, to output: FileHandle) {
    DispatchQueue.global(qos: .utility).async {
        var buffer = [UInt8](repeating: 0, count: 4096)
        while true {
            let fd = open(path, O_RDONLY)
            if fd < 0 {
                usleep(100_000)
                continue
            }
            while true {
                let n = read(fd, &buffer, buffer.count)
                if n > 0 {
                    if !FileManager.default.fileExists(atPath: path) {
                        close(fd)
                        output.closeFile()
                        return
                    }
                    output.write(Data(buffer.prefix(n)))
                    continue
                }
                break
            }
            close(fd)
        }
    }
}
#endif

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
