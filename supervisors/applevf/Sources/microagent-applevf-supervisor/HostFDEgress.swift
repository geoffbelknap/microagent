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
let hostFDTeardownLock = NSLock()

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
    let bin = try egressDatapathBinaryPath()

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
    proc.arguments = hostFDDatapathArgs(config: config, identity: identity)
    // The datapath reads guest frames from its stdin (the peer socket end).
    proc.standardInput = FileHandle(fileDescriptor: datapathEnd, closeOnDealloc: false)
    proc.standardOutput = FileHandle.nullDevice
    // Do not inherit supervisor stderr: foreground supervisors are pipe-backed
    // by the Go parent, and a long-lived datapath child holding that pipe open
    // prevents cmd.Run from observing EOF even after the supervisor exits.
    proc.standardError = FileHandle.nullDevice
    do {
        try proc.run()
    } catch {
        close(frameEnd)
        close(datapathEnd)
        throw ProtocolError.invalid("apple-vf host-fd: spawn egress datapath: \(error)")
    }
    // The child inherited datapathEnd as fd 0; the parent drops its copy.
    close(datapathEnd)
    hostFDFrameEnd = frameEnd
    hostFDDatapath = proc
}

func closeHostFDEgress() {
    hostFDTeardownLock.lock()
    let frameEnd = hostFDFrameEnd
    let datapath = hostFDDatapath
    hostFDFrameEnd = -1
    hostFDDatapath = nil
    hostFDTeardownLock.unlock()

    // Broker endpoint companions share the datapath's lifecycle: each holds a
    // live credential that must die with the VM.
    teardownBrokerCompanions()

    if frameEnd >= 0 {
        close(frameEnd)
    }
    guard let datapath else {
        return
    }
    if datapath.isRunning {
        datapath.terminate()
        let exited = DispatchSemaphore(value: 0)
        DispatchQueue.global(qos: .utility).async {
            datapath.waitUntilExit()
            exited.signal()
        }
        if exited.wait(timeout: .now() + 2) == .timedOut && datapath.isRunning {
            kill(datapath.processIdentifier, SIGKILL)
        }
    }
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
