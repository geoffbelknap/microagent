import Foundation
import Virtualization

// Apple VF host-fd egress capture provider (applevf-host-fd-gateway).
//
// Instead of the framework's in-process NAT (VZNATNetworkDeviceAttachment),
// which exposes no capture point, the guest's only NIC is a host-owned datagram
// socket (VZFileHandleNetworkDeviceAttachment). The supervisor spawns the Go
// `microagent --egress-datapath` subprocess on the other end of the socketpair;
// that subprocess runs a userspace gVisor stack that is the guest's L3 gateway
// and routes all egress (out to the network in S1; through the mediator in S2+).
// Because the guest has no other uplink, egress cannot bypass it.
//
// The datapath MUST be spawned before the VM child applies its Seatbelt
// confinement: the sandbox is inherited by children and is loopback-only, so a
// post-confinement datapath could neither exec nor reach the network. Hence
// prepareHostFDEgressBeforeConfinement() runs first and stashes the framework
// end of the socket for networkDevices to attach.
//
// The host-fd provider is the default for mediated Apple VF user networking.
// MICROAGENT_APPLEVF_HOSTFD=1 still enables the datapath for unmediated smoke
// tests without changing the explicit egress=off native NAT behavior.

// Static subnet for the host-fd gateway. The gateway owns .1; the guest is
// configured with .2 via the kernel cmdline.
let hostFDSubnet = "192.168.127.0/24"
let hostFDGatewayIP = "192.168.127.1"
// Guest address is CIDR (guest init parses microagent_net_ip as a CIDR).
let hostFDGuestIP = "192.168.127.2/24"
// The packaged Apple VF kernel does not currently provide IPv6. Keep the
// datapath constants for the eventual kernel capability, but do not advertise
// or inject IPv6 until that boot path can configure it successfully.
let hostFDIPv6Enabled = false
let hostFDIPv6Subnet = "fd00:6d69:6372:7f::/64"
let hostFDGatewayIPv6 = "fd00:6d69:6372:7f::1"
let hostFDGuestIPv6 = "fd00:6d69:6372:7f::2/64"
let hostFDGuestDNS = "1.1.1.1"

// staticUserDefaultDNS matches the firecracker supervisor's default: a static
// user-mode guest with no declared nameservers still gets working resolution.
let staticUserDefaultDNS = ["1.1.1.1", "8.8.8.8"]

// hostFDFrameEnd is the framework end of the socketpair, opened before
// confinement and consumed by networkDevices. -1 until prepared. One VM per
// supervisor process, so a single value suffices.
// Accessed only on the single VM-setup path (prepared before confinement, read
// by networkDevices) before any VM thread runs, so the access is serialized.
nonisolated(unsafe) var hostFDFrameEnd: Int32 = -1
nonisolated(unsafe) var hostFDDatapath: Process?
nonisolated(unsafe) var hostFDDiagnostics: BoundedDatapathDiagnostics?
let hostFDTeardownLock = NSLock()

struct DatapathStartupFailure: Codable, Equatable, CustomStringConvertible {
    var boundary: String
    var executablePath: String
    var exitStatus: Int32?
    var diagnosticsPath: String
    var reason: String

    var description: String {
        var detail = "\(boundary) startup failed: \(reason); executable=\(executablePath)"
        if let exitStatus {
            detail += "; exit_status=\(exitStatus)"
        }
        detail += "; diagnostics=\(diagnosticsPath)"
        return detail
    }
}

struct DatapathStartupStatus: Codable, Equatable {
    var ok: Bool
    var failure: DatapathStartupFailure?
}

struct DatapathStartupError: Error, CustomStringConvertible {
    var failure: DatapathStartupFailure
    var description: String { failure.description }
}

func datapathDiagnosticsPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir)
        .appendingPathComponent(datapathDiagnosticsFileName)
}

func datapathStartupStatusPath(identity: Identity, stateDir: String) -> URL {
    runtimeDirectory(identity: identity, stateDir: stateDir)
        .appendingPathComponent(datapathStartupFileName)
}

func writeDatapathStartupStatus(_ status: DatapathStartupStatus, identity: Identity, stateDir: String) throws {
    try writeDataAtomically0600(
        encoder.encode(status),
        to: datapathStartupStatusPath(identity: identity, stateDir: stateDir)
    )
}

private final class DatapathExitState: @unchecked Sendable {
    private let lock = NSLock()
    private var status: Int32?

    func record(_ value: Int32) {
        lock.lock()
        status = value
        lock.unlock()
    }

    func snapshot() -> Int32? {
        lock.lock()
        let value = status
        lock.unlock()
        return value
    }
}

final class BoundedDatapathDiagnostics: @unchecked Sendable {
    let pipe = Pipe()
    let path: URL
    private let lock = NSLock()
    private var stored = Data()
    private let redactions: [String]

    init(path: URL, sensitiveArguments: [String], environment: [String: String] = ProcessInfo.processInfo.environment) throws {
        self.path = path
        redactions = Set(
            environment.values + sensitiveArguments
        )
        .filter { !$0.isEmpty }
        .sorted { $0.count > $1.count }
        try FileManager.default.createDirectory(
            at: path.deletingLastPathComponent(),
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        guard FileManager.default.createFile(
            atPath: path.path,
            contents: nil,
            attributes: [.posixPermissions: 0o600]
        ) else {
            throw ProtocolError.invalid("create \(datapathDiagnosticsFileName) failed")
        }
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else {
                handle.readabilityHandler = nil
                return
            }
            self?.append(data)
        }
    }

    private func append(_ data: Data) {
        var text = String(decoding: data, as: UTF8.self)
        for value in redactions {
            text = text.replacingOccurrences(of: value, with: "<redacted>")
        }
        var sanitized = Data(text.utf8)
        lock.lock()
        let remaining = maxDatapathDiagnosticBytes - stored.count
        if remaining > 0 {
            if sanitized.count > remaining {
                sanitized = sanitized.prefix(remaining)
            }
            stored.append(sanitized)
            try? stored.write(to: path)
            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
        }
        lock.unlock()
    }

    func finish() {
        try? pipe.fileHandleForWriting.close()
        pipe.fileHandleForReading.readabilityHandler = nil
        let tail = pipe.fileHandleForReading.readDataToEndOfFile()
        if !tail.isEmpty {
            append(tail)
        }
        try? pipe.fileHandleForReading.close()
    }
}

enum CACertMaterialError: Error, CustomStringConvertible {
    case missing(String)
    case unreadable(String)
    case empty(String)
    case oversized(String, Int)
    case invalid(String)

    var description: String {
        switch self {
        case .missing(let path): return "CA material is missing at \(path)"
        case .unreadable(let path): return "CA material is unreadable at \(path)"
        case .empty(let path): return "CA material is empty at \(path)"
        case .oversized(let path, let size): return "CA material at \(path) is \(size) bytes; maximum is \(maxCACertBytes)"
        case .invalid(let path): return "CA material is not a certificate PEM at \(path)"
        }
    }
}

func loadValidatedCACert(path: URL) throws -> Data {
    guard FileManager.default.fileExists(atPath: path.path) else {
        throw CACertMaterialError.missing(path.path)
    }
    guard let attributes = try? FileManager.default.attributesOfItem(atPath: path.path),
          let size = attributes[.size] as? NSNumber else {
        throw CACertMaterialError.unreadable(path.path)
    }
    if size.intValue == 0 {
        throw CACertMaterialError.empty(path.path)
    }
    if size.intValue > maxCACertBytes {
        throw CACertMaterialError.oversized(path.path, size.intValue)
    }
    guard let data = try? Data(contentsOf: path) else {
        throw CACertMaterialError.unreadable(path.path)
    }
    let text = String(decoding: data, as: UTF8.self)
    guard text.contains("-----BEGIN CERTIFICATE-----"),
          text.contains("-----END CERTIFICATE-----") else {
        throw CACertMaterialError.invalid(path.path)
    }
    return data
}

struct HostFDEgressClosure {
    var datapathPresent: Bool
    var datapathTerminated: Bool
    var brokerCompanionsPresent: Int
    var brokerCompanionsTerminated: Int

    var complete: Bool {
        datapathTerminated && brokerCompanionsTerminated == brokerCompanionsPresent
    }
}

func hostFDEgressEnabled(config: Config? = nil) -> Bool {
    if ProcessInfo.processInfo.environment["MICROAGENT_APPLEVF_HOSTFD"] == "1" {
        return true
    }
    guard normalizedNetworkMode(config?.network) == "user" else {
        return false
    }
    // Mirror Go's vmkit.EgressMediationOn: the mediated datapath runs for the
    // final egress-mode vocabulary — "broker" (the default) and "mitm" — and NOT
    // for "off". The old "guarded"/"strict" names were retired in commit 452c510
    // and never reach the supervisor; gating on them here silently dropped every
    // default (broker) workspace to unmediated native NAT. An empty/unset mode is
    // the low-level raw primitive leaving it unspecified: treat it as unmediated,
    // matching EgressMediationOn(""). Keep this in lockstep with
    // pkg/vmkit/types.go:EgressMediationOn.
    let mode = config?.egressMode?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() ?? ""
    return mode == "broker" || mode == "mitm"
}

// egressDatapathBinaryPath resolves the Go microagent binary that hosts the
// `--egress-datapath` subprocess. The Go side sets MICROAGENT_EGRESS_DATAPATH_BIN
// when it spawns this supervisor.
func egressDatapathBinaryPath() throws -> String {
    if let p = ProcessInfo.processInfo.environment["MICROAGENT_EGRESS_DATAPATH_BIN"],
       !p.trimmingCharacters(in: .whitespaces).isEmpty {
        return p
    }
    throw ProtocolError.invalid("apple-vf host-fd egress requires MICROAGENT_EGRESS_DATAPATH_BIN (path to the microagent binary)")
}

// hostFDDatapathArgs builds the argv for the egress datapath subprocess.
// Every egress-relevant Config field must be forwarded here: a field this
// function drops is silently unenforced on apple-vf (the workspace layer and
// manifest still report it as set). HostFDDatapathArgsTests pins the mapping.
func hostFDDatapathArgs(config: Config, identity: Identity) -> [String] {
    var args = [
        "--egress-datapath",
        "--fd", "0",
        "--gateway-ip", hostFDGatewayIP,
        "--state-dir", config.stateDir,
        "--name", identity.runtimeID,
        "--session-id", identity.sessionID ?? "",
        // Pass the resolved mode through; the datapath's own vmkit.EgressMediationOn
        // decides whether to mediate. We only reach here for broker/mitm (mediated)
        // or the MICROAGENT_APPLEVF_HOSTFD smoke-test override — for the override
        // with no mode, "off" runs the datapath as plain unmediated NAT (the
        // documented smoke-test behavior). Never default to a retired name.
        "--egress-mode", config.egressMode ?? "off",
    ]
    if hostFDIPv6Enabled {
        args.append("--gateway-ipv6")
        args.append(hostFDGatewayIPv6)
    }
    if config.egressAllowlistLocked == true {
        args.append("--lock-allowlist")
    }
    for host in config.egressAllow ?? [] {
        args.append("--allow")
        args.append(host)
    }
    for host in config.egressPassthrough ?? [] {
        args.append("--passthrough")
        args.append(host)
    }
    if let swap = config.egressSwapConfigPath, !swap.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        args.append("--swap-config")
        args.append(swap)
    }
    // Resolver allowlist: the workspace's configured nameservers are the only
    // addresses the datapath will forward guest DNS to (confused-deputy guard),
    // matching what the Firecracker supervisor forwards from config.Network.DNS.
    // Empty when no DNS is configured, leaving the internal-address floor.
    for resolver in config.network?.dns ?? [] {
        let trimmed = resolver.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            args.append("--resolver")
            args.append(trimmed)
        }
    }
    // Bounded-operations caps (ASK tenet 8). Each is emitted only when non-zero
    // so an uncapped workspace's argv is byte-identical to the pre-caps one,
    // mirroring the Firecracker mediator argv shape.
    if let bps = config.egressMaxBytesPerSec, bps > 0 {
        args.append("--max-bps")
        args.append(String(bps))
    }
    if let total = config.egressMaxTotalBytes, total > 0 {
        args.append("--max-bytes")
        args.append(String(total))
    }
    if let conns = config.egressMaxConcurrentConns, conns > 0 {
        args.append("--max-conns")
        args.append(String(conns))
    }
    if let auditBytes = config.egressAuditMaxBytes, auditBytes > 0 {
        args.append("--audit-max-bytes")
        args.append(String(auditBytes))
        if let backups = config.egressAuditMaxBackups, backups > 0 {
            args.append("--audit-max-backups")
            args.append(String(backups))
        }
    }
    return args
}

// prepareHostFDEgressBeforeConfinement creates the guest NIC socketpair and
// spawns the egress datapath subprocess on the peer end. It must be called
// before applyConfinement so the datapath runs unsandboxed (full network access
// for NAT, and able to exec). No-op when host-fd egress is disabled or already
// prepared.
func prepareHostFDEgressBeforeConfinement(config: Config, identity: Identity) throws {
    guard hostFDEgressEnabled(config: config), hostFDFrameEnd < 0 else { return }
    let diagnosticsPath = datapathDiagnosticsPath(identity: identity, stateDir: config.stateDir)
    let bin: String
    do {
        bin = try egressDatapathBinaryPath()
    } catch {
        throw DatapathStartupError(failure: DatapathStartupFailure(
            boundary: "apple-vf.host-fd.datapath",
            executablePath: "",
            exitStatus: nil,
            diagnosticsPath: diagnosticsPath.path,
            reason: "an explicit MICROAGENT_EGRESS_DATAPATH_BIN pointing to the microagent CLI is required"
        ))
    }

    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: bin, isDirectory: &isDirectory),
          !isDirectory.boolValue,
          FileManager.default.isExecutableFile(atPath: bin) else {
        throw DatapathStartupError(failure: DatapathStartupFailure(
            boundary: "apple-vf.host-fd.datapath",
            executablePath: bin,
            exitStatus: nil,
            diagnosticsPath: diagnosticsPath.path,
            reason: "datapath executable is missing, not a regular file, or not executable"
        ))
    }

    var fds: [Int32] = [-1, -1]
    let rc = fds.withUnsafeMutableBufferPointer { ptr in
        socketpair(AF_UNIX, SOCK_DGRAM, 0, ptr.baseAddress!)
    }
    if rc != 0 {
        throw ProtocolError.invalid("apple-vf host-fd: socketpair failed (errno \(errno))")
    }
    let frameEnd = fds[0]
    let datapathEnd = fds[1]

    // The framework end must not leak into the datapath child, or the socket
    // never closes on VM teardown and the datapath would not self-reap.
    _ = fcntl(frameEnd, F_SETFD, FD_CLOEXEC)
    // Grow the framework-end socket buffers so guest bursts do not drop frames.
    var bufSize: Int32 = 1 << 20
    _ = setsockopt(frameEnd, SOL_SOCKET, SO_SNDBUF, &bufSize, socklen_t(MemoryLayout<Int32>.size))
    _ = setsockopt(frameEnd, SOL_SOCKET, SO_RCVBUF, &bufSize, socklen_t(MemoryLayout<Int32>.size))

    let proc = Process()
    proc.executableURL = URL(fileURLWithPath: bin)
    let arguments = hostFDDatapathArgs(config: config, identity: identity)
    proc.arguments = arguments
    // The datapath reads guest frames from its stdin (the peer socket end).
    proc.standardInput = FileHandle(fileDescriptor: datapathEnd, closeOnDealloc: false)
    proc.standardOutput = FileHandle.nullDevice
    // Do not inherit supervisor stderr: foreground supervisors are pipe-backed
    // by the Go parent, and a long-lived datapath child holding that pipe open
    // prevents cmd.Run from observing EOF even after the supervisor exits.
    // Drain it into an owner-only bounded record, redacting environment and
    // argument values so diagnostics cannot become a credential side channel.
    let sensitiveArguments: [String] = arguments.enumerated().compactMap { pair -> String? in
        let (index, value) = pair
        if value.hasPrefix("--") || value.isEmpty { return nil }
        return index > 0 ? value : nil
    }
    let diagnostics: BoundedDatapathDiagnostics
    do {
        diagnostics = try BoundedDatapathDiagnostics(
            path: diagnosticsPath,
            sensitiveArguments: sensitiveArguments
        )
    } catch {
        close(frameEnd)
        close(datapathEnd)
        throw DatapathStartupError(failure: DatapathStartupFailure(
            boundary: "apple-vf.host-fd.datapath",
            executablePath: bin,
            exitStatus: nil,
            diagnosticsPath: diagnosticsPath.path,
            reason: "cannot create bounded datapath diagnostics"
        ))
    }
    proc.standardError = diagnostics.pipe.fileHandleForWriting
    let exitState = DatapathExitState()
    proc.terminationHandler = { child in
        exitState.record(child.terminationStatus)
    }
    do {
        try proc.run()
    } catch {
        diagnostics.finish()
        close(frameEnd)
        close(datapathEnd)
        throw DatapathStartupError(failure: DatapathStartupFailure(
            boundary: "apple-vf.host-fd.datapath",
            executablePath: bin,
            exitStatus: nil,
            diagnosticsPath: diagnosticsPath.path,
            reason: "could not spawn datapath executable"
        ))
    }
    // The child inherited datapathEnd as fd 0; the parent drops its copy.
    close(datapathEnd)

    let caPath = runtimeDirectory(identity: identity, stateDir: config.stateDir)
        .appendingPathComponent("egress-ca.pem")
    let requiresCA = (config.caCertPort ?? 0) > 0
    let deadline = Date().addingTimeInterval(datapathStartupTimeout)
    let livenessDeadline = Date().addingTimeInterval(0.15)
    var lastCAError: Error = CACertMaterialError.missing(caPath.path)
    var ready = false
    while Date() < deadline {
        if let status = exitState.snapshot() {
            diagnostics.finish()
            close(frameEnd)
            throw DatapathStartupError(failure: DatapathStartupFailure(
                boundary: "apple-vf.host-fd.datapath",
                executablePath: bin,
                exitStatus: status,
                diagnosticsPath: diagnosticsPath.path,
                reason: "datapath exited before preboot readiness"
            ))
        }
        if requiresCA {
            do {
                _ = try loadValidatedCACert(path: caPath)
                ready = true
                break
            } catch CACertMaterialError.missing {
                lastCAError = CACertMaterialError.missing(caPath.path)
            } catch {
                _ = terminateAndReapChildProcesses([proc])
                diagnostics.finish()
                close(frameEnd)
                throw DatapathStartupError(failure: DatapathStartupFailure(
                    boundary: "apple-vf.host-fd.ca-material",
                    executablePath: bin,
                    exitStatus: exitState.snapshot(),
                    diagnosticsPath: diagnosticsPath.path,
                    reason: String(describing: error)
                ))
            }
        } else if Date() >= livenessDeadline {
            ready = true
            break
        }
        RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.01))
    }
    guard ready else {
        _ = terminateAndReapChildProcesses([proc])
        diagnostics.finish()
        close(frameEnd)
        throw DatapathStartupError(failure: DatapathStartupFailure(
            boundary: requiresCA ? "apple-vf.host-fd.ca-material" : "apple-vf.host-fd.datapath",
            executablePath: bin,
            exitStatus: exitState.snapshot(),
            diagnosticsPath: diagnosticsPath.path,
            reason: requiresCA ? String(describing: lastCAError) : "datapath did not become ready"
        ))
    }
    hostFDFrameEnd = frameEnd
    hostFDDatapath = proc
    hostFDDiagnostics = diagnostics
}

@discardableResult
func closeHostFDEgress() -> HostFDEgressClosure {
    hostFDTeardownLock.lock()
    let frameEnd = hostFDFrameEnd
    let datapath = hostFDDatapath
    let diagnostics = hostFDDiagnostics
    hostFDFrameEnd = -1
    hostFDDatapath = nil
    hostFDDiagnostics = nil
    hostFDTeardownLock.unlock()

    // Broker endpoint companions share the datapath's lifecycle: each holds a
    // live credential that must die with the VM.
    let brokers = takeBrokerCompanions()

    if frameEnd >= 0 {
        close(frameEnd)
    }

    var children = brokers
    if let datapath {
        children.append(datapath)
    }

    let terminated = terminateAndReapChildProcesses(children)
    diagnostics?.finish()
    let brokerTerminated = terminated.prefix(brokers.count).filter { $0 }.count
    let datapathTerminated = datapath == nil || terminated.last == true

    return HostFDEgressClosure(
        datapathPresent: datapath != nil,
        datapathTerminated: datapathTerminated,
        brokerCompanionsPresent: brokers.count,
        brokerCompanionsTerminated: brokerTerminated
    )
}

// ChildExitState serializes completion written by background waiters while the
// quarantine controller blocks the supervisor's main queue. Polling
// Process.isRunning there can remain stale until Foundation gets a chance to
// reap its child, producing a failed acknowledgement after the OS process has
// already exited.
private final class ChildExitState: @unchecked Sendable {
    private let lock = NSLock()
    private var exited: [Bool]

    init(children: [Process]) {
        exited = children.map { !$0.isRunning }
    }

    func markExited(_ index: Int) {
        lock.lock()
        exited[index] = true
        lock.unlock()
    }

    func didExit(_ index: Int) -> Bool {
        lock.lock()
        let result = exited[index]
        lock.unlock()
        return result
    }

    func snapshot() -> [Bool] {
        lock.lock()
        let result = exited
        lock.unlock()
        return result
    }
}

// Signals every child before waiting so N broker endpoints share the same
// bounded windows. A background waitUntilExit owns the authoritative reap for
// each Process: TERM gets one second, then any remaining child gets SIGKILL and
// one final second. Containment acknowledgement is withheld unless every
// waiter completed.
private func terminateAndReapChildProcesses(_ children: [Process]) -> [Bool] {
    let state = ChildExitState(children: children)
    let group = DispatchGroup()
    let active = children.indices.filter { !state.didExit($0) }

    for index in active {
        let child = children[index]
        group.enter()
        DispatchQueue.global(qos: .utility).async {
            child.waitUntilExit()
            state.markExited(index)
            group.leave()
        }
    }

    for index in active where !state.didExit(index) {
        children[index].terminate()
    }
    if group.wait(timeout: .now() + .seconds(1)) == .timedOut {
        for index in active where !state.didExit(index) {
            kill(children[index].processIdentifier, SIGKILL)
        }
        _ = group.wait(timeout: .now() + .seconds(1))
    }
    return state.snapshot()
}

// makeHostFDNetworkDevice attaches the framework end of the prepared socket as
// the guest NIC. prepareHostFDEgressBeforeConfinement must have run first.
@available(macOS 13.0, *)
func makeHostFDNetworkDevice(macAddress: VZMACAddress) throws -> VZVirtioNetworkDeviceConfiguration {
    guard hostFDFrameEnd >= 0 else {
        throw ProtocolError.invalid("apple-vf host-fd: egress datapath was not prepared before confinement")
    }
    let handle = FileHandle(fileDescriptor: hostFDFrameEnd, closeOnDealloc: true)
    let attachment = VZFileHandleNetworkDeviceAttachment(fileHandle: handle)
    attachment.maximumTransmissionUnit = 1500
    let device = VZVirtioNetworkDeviceConfiguration()
    device.attachment = attachment
    device.macAddress = macAddress
    return device
}
