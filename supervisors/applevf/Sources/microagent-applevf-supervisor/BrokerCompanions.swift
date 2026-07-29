import Foundation

// Broker endpoint companions.
//
// Each vsock listener targeting broker://serve is served by a Go
// `microagent --broker-serve` subprocess — the same portable endpoint server
// the Firecracker vsock companion uses, so the two backends cannot drift on
// credential handling, decision records, or CONNECT gating. The companion
// binds an owner-only unix socket in the runtime directory; the vsock
// listener splices guest connections to it. Companions MUST be spawned
// before the VM child applies Seatbelt confinement (the sandbox is inherited
// and loopback-only, so a post-confinement companion could neither exec nor
// reach its upstream) and are terminated with the host-fd egress teardown.

// brokerListenerTarget marks a vsock listener served by a broker endpoint
// companion. Keep in lockstep with vmkit.BrokerListenerTarget.
let brokerListenerTarget = "broker://serve"

nonisolated(unsafe) var brokerCompanions: [Process] = []
let brokerCompanionLock = NSLock()

func brokerSocketPath(identity: Identity, stateDir: String, port: UInt32) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir).appendingPathComponent("broker-\(port).sock")
}

// maxUnixSocketPathBytes is sockaddr_un.sun_path's capacity on macOS (104,
// minus the NUL). A companion socket past it fails bind() with an error that
// does not name the real cause, so the length is checked upfront instead.
let maxUnixSocketPathBytes = 103

func validateBrokerSocketPath(_ sock: URL, port: UInt32) throws {
    let bytes = Array(sock.path.utf8).count
    if bytes > maxUnixSocketPathBytes {
        throw ProtocolError.invalid("broker companion socket path for vsock \(port) is \(bytes) bytes; unix sockets cap at \(maxUnixSocketPathBytes) — use a shorter --state-dir or workspace name (path: \(sock.path))")
    }
}

// brokerEndpoint resolves the endpoint declaration for a broker listener
// port, with the same legacy single-endpoint fallback the Firecracker
// supervisor applies (brokerForPort).
func brokerEndpoint(config: Config, port: UInt32) -> BrokerConfig? {
    if let brokers = config.brokers, !brokers.isEmpty {
        return brokers.first { ($0.vsockPort ?? 0) == port }
    }
    return config.broker
}

// brokerServeArgs builds the companion argv. Every endpoint field the
// companion consumes must be forwarded here — a field this function drops is
// silently unenforced on apple-vf. BrokerCompanionArgsTests pins the mapping.
// The secret travels as a scheme-prefixed REFERENCE, never a value; the
// companion resolves it host-side.
func brokerServeArgs(endpoint: BrokerConfig, config: Config, identity: Identity, listenPath: String) -> [String] {
    var args = [
        "--broker-serve",
        "--state-dir", config.stateDir,
        "--name", identity.runtimeID,
        "--listen", listenPath,
        "--upstream", endpoint.upstream,
        "--secret", "\(endpoint.secret.name)=\(endpoint.secret.ref)",
    ]
    if endpoint.proxy == true {
        args.append("--proxy")
    }
    for host in endpoint.connectAllowlist ?? [] {
        args.append("--connect-allow")
        args.append(host)
    }
    if let ca = endpoint.upstreamCAFile, !ca.isEmpty {
        args.append("--upstream-ca")
        args.append(ca)
    }
    if endpoint.capture == true {
        args.append("--capture")
    }
    return args
}

// prepareBrokerCompanionsBeforeConfinement spawns one companion per broker
// listener and waits for each to bind its socket, fail-closed: a workspace
// must not boot half-brokered, and a guest must never dial a socket nothing
// serves.
func prepareBrokerCompanionsBeforeConfinement(config: Config, identity: Identity) throws {
    let listeners = (config.vsockListeners ?? []).filter { $0.target == brokerListenerTarget }
    guard !listeners.isEmpty else {
        return
    }
    let bin = try egressDatapathBinaryPath()
    for listener in listeners {
        guard let endpoint = brokerEndpoint(config: config, port: listener.port) else {
            throw ProtocolError.invalid("vsock listener \(listener.port) targets the egress broker but no broker endpoint is configured for that port")
        }
        let sock = brokerSocketPath(identity: identity, stateDir: config.stateDir, port: listener.port)
        try validateBrokerSocketPath(sock, port: listener.port)
        try FileManager.default.createDirectory(at: sock.deletingLastPathComponent(), withIntermediateDirectories: true)
        try? FileManager.default.removeItem(at: sock)
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: bin)
        proc.arguments = brokerServeArgs(endpoint: endpoint, config: config, identity: identity, listenPath: sock.path)
        proc.standardInput = FileHandle.nullDevice
        proc.standardOutput = FileHandle.nullDevice
        // Same reasoning as the egress datapath: do not inherit supervisor
        // stderr, or a long-lived child holding the Go parent's pipe open
        // prevents it from observing EOF after the supervisor exits.
        proc.standardError = FileHandle.nullDevice
        do {
            try proc.run()
        } catch {
            throw ProtocolError.invalid("spawn broker companion for vsock \(listener.port): \(error)")
        }
        brokerCompanionLock.lock()
        brokerCompanions.append(proc)
        brokerCompanionLock.unlock()
        let deadline = Date().addingTimeInterval(30)
        while !FileManager.default.fileExists(atPath: sock.path) {
            if !proc.isRunning {
                throw ProtocolError.invalid("broker companion for vsock \(listener.port) exited during startup (unresolvable secret reference, or invalid endpoint config)")
            }
            if Date() > deadline {
                throw ProtocolError.invalid("broker companion for vsock \(listener.port) did not bind \(sock.lastPathComponent) in time")
            }
            usleep(50_000)
        }
    }
}

// teardownBrokerCompanions terminates every spawned companion; the live
// credential each holds dies with its process.
func teardownBrokerCompanions() {
    brokerCompanionLock.lock()
    let procs = brokerCompanions
    brokerCompanions = []
    brokerCompanionLock.unlock()
    for proc in procs where proc.isRunning {
        proc.terminate()
    }
}

// dialUnix opens a stream connection to a unix socket path, returning -1 on
// any failure (mirroring dialTCP's contract for the splice delegates).
func dialUnix(_ path: String) -> Int32 {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    if fd < 0 {
        return -1
    }
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8)
    let capacity = MemoryLayout.size(ofValue: addr.sun_path) - 1
    if bytes.count > capacity {
        close(fd)
        return -1
    }
    withUnsafeMutableBytes(of: &addr.sun_path) { raw in
        raw.copyBytes(from: bytes)
    }
    addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
    let rc = withUnsafePointer(to: &addr) { ptr in
        ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
        }
    }
    if rc != 0 {
        close(fd)
        return -1
    }
    return fd
}
