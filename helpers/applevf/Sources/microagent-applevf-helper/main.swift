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
let eventFileName = "event.json"
let configFileName = "config.json"
let runtimeFileName = "runtime.json"
let serialLogFileName = "serial.log"
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
    var startedAt: Date?
    var updatedAt: Date
    var error: String?
}

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
        return Response(ok: true, backend: backendName, event: event)
    case "start":
        let identity = try validatedIdentity(request.identity)
        let config = try validatedConfig(request.config)
        let support = hostSupport()
        guard support.frameworkAvailable && support.virtualizationSupported else {
            return Response(ok: false, backend: backendName, error: "Apple Virtualization is not available on this host")
        }
        let event = Event(identity: identity, state: .starting, detail: "serial=\(serialLogPath(identity: identity, stateDir: config.stateDir).path)", observedAt: Date())
        try writeState(event: event, config: config)
        let process = Process()
        process.executableURL = URL(fileURLWithPath: currentExecutablePath())
        process.arguments = ["--request-json", try requestJSON(request.withCommand("run"))]
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        try process.run()
        try writeRuntimeState(event: event, config: config, pid: process.processIdentifier, error: nil)
        return Response(ok: true, backend: backendName, event: event)
    case "inspect":
        let identity = try validatedIdentity(request.identity)
        let config = try stateConfig(request.config)
        var event = try readEvent(identity: identity, stateDir: config.stateDir) ?? Event(identity: identity, state: .unknown, detail: nil, observedAt: Date())
        if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), !processAlive(runtime.pid), event.state == .starting || event.state == .running {
            event = Event(identity: event.identity, state: .stopped, detail: event.detail, observedAt: Date())
            try writeState(event: event, config: runtime.config)
            try writeRuntimeState(event: event, config: runtime.config, pid: runtime.pid, error: runtime.error)
        }
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
    if let runtime = try readRuntimeState(identity: identity, stateDir: config.stateDir), processAlive(runtime.pid), let pid = runtime.pid {
        let signal = detail == "forced" ? SIGKILL : SIGTERM
        if kill(pid, signal) != 0 && errno != ESRCH {
            throw ProtocolError.invalid("signal \(pid) failed with errno \(errno)")
        }
        try writeState(event: event, config: runtime.config)
        try writeRuntimeState(event: event, config: runtime.config, pid: runtime.pid, error: nil)
    } else {
        try writeState(event: event, config: config)
    }
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
    try readableFile(config.kernelPath, name: "config.kernelPath")
    try readableFile(config.rootfsPath, name: "config.rootfsPath")
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
        supported = true
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

func writeState(event: Event, config: Config) throws {
    let directory = runtimeDirectory(identity: event.identity, stateDir: config.stateDir)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try encoder.encode(event).write(to: eventPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
    try encoder.encode(config).write(to: configPath(identity: event.identity, stateDir: config.stateDir), options: .atomic)
}

func writeRuntimeState(event: Event, config: Config, pid: Int32?, error: String?) throws {
    let runtime = RuntimeState(
        event: event,
        config: config,
        pid: pid,
        serialLogPath: serialLogPath(identity: event.identity, stateDir: config.stateDir).path,
        startedAt: event.state == .starting || event.state == .running ? Date() : nil,
        updatedAt: Date(),
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
final class ResultSocketDelegate: NSObject, VZVirtioSocketListenerDelegate {
    private let path: String
    private var connections: [VZVirtioSocketConnection] = []

    init(path: String) {
        self.path = path
    }

    func listener(_ listener: VZVirtioSocketListener, shouldAcceptNewConnection connection: VZVirtioSocketConnection, from socketDevice: VZVirtioSocketDevice) -> Bool {
        connections.append(connection)
        let fd = connection.fileDescriptor
        let path = self.path
        DispatchQueue.global(qos: .utility).async {
            var data = Data()
            var buffer = [UInt8](repeating: 0, count: 4096)
            while true {
                let n = read(fd, &buffer, buffer.count)
                if n > 0 {
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
        let vmConfig = try virtualMachineConfiguration(identity: identity, config: config, attachSerial: true)
        try vmConfig.validate()

        let vm = VZVirtualMachine(configuration: vmConfig)
        let delegate = VMRunDelegate(identity: identity, config: config)
        vm.delegate = delegate
        var socketDelegates: [ResultSocketDelegate] = []
        let semaphore = DispatchSemaphore(value: 0)
        var startError: Error?
        vm.start { result in
            switch result {
            case .success:
                socketDelegates = installSocketListeners(vm: vm, config: config)
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
        withExtendedLifetime((delegate, socketDelegates)) {
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
func installSocketListeners(vm: VZVirtualMachine, config: Config) -> [ResultSocketDelegate] {
    guard let socket = vm.socketDevices.first as? VZVirtioSocketDevice else {
        return []
    }
    var delegates: [ResultSocketDelegate] = []
    for listenerConfig in config.vsockListeners ?? [] {
        let listener = VZVirtioSocketListener()
        let delegate = ResultSocketDelegate(path: listenerConfig.target)
        listener.delegate = delegate
        socket.setSocketListener(listener, forPort: listenerConfig.port)
        delegates.append(delegate)
    }
    return delegates
}

@available(macOS 13.0, *)
func virtualMachineConfiguration(identity: Identity, config: Config, attachSerial: Bool) throws -> VZVirtualMachineConfiguration {
    let vmConfig = VZVirtualMachineConfiguration()
    vmConfig.platform = VZGenericPlatformConfiguration()
    let bootLoader = VZLinuxBootLoader(kernelURL: URL(fileURLWithPath: config.kernelPath))
    bootLoader.commandLine = "console=hvc0 root=/dev/vda rw init=/sbin/microagent-init"
    vmConfig.bootLoader = bootLoader
    vmConfig.cpuCount = config.cpuCount ?? 2
    vmConfig.memorySize = UInt64(config.memoryMiB ?? 512) * 1024 * 1024
    let attachment = try VZDiskImageStorageDeviceAttachment(url: URL(fileURLWithPath: config.rootfsPath), readOnly: false)
    vmConfig.storageDevices = [VZVirtioBlockDeviceConfiguration(attachment: attachment)]
    vmConfig.entropyDevices = [VZVirtioEntropyDeviceConfiguration()]
    if attachSerial {
        let serial = VZVirtioConsoleDeviceSerialPortConfiguration()
        FileManager.default.createFile(atPath: serialLogPath(identity: identity, stateDir: config.stateDir).path, contents: nil)
        let serialHandle = try FileHandle(forWritingTo: serialLogPath(identity: identity, stateDir: config.stateDir))
        try serialHandle.seekToEnd()
        serial.attachment = VZFileHandleSerialPortAttachment(fileHandleForReading: nil, fileHandleForWriting: serialHandle)
        vmConfig.serialPorts = [serial]
    }
    if !(config.vsockListeners ?? []).isEmpty {
        vmConfig.socketDevices = [VZVirtioSocketDeviceConfiguration()]
    }
    return vmConfig
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
