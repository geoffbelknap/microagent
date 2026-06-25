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
// S1 is opt-in behind MICROAGENT_APPLEVF_HOSTFD=1 so it does not disturb the
// fail-closed default while the datapath is validated; S4 makes it the default
// for mediated egress.

// Static subnet for the host-fd gateway. The gateway owns .1; the guest is
// configured with .2 via the kernel cmdline.
let hostFDGatewayIP = "192.168.127.1"
// Guest address is CIDR (guest init parses microagent_net_ip as a CIDR).
let hostFDGuestIP = "192.168.127.2/24"
let hostFDGuestDNS = "1.1.1.1"

// hostFDFrameEnd is the framework end of the socketpair, opened before
// confinement and consumed by networkDevices. -1 until prepared. One VM per
// supervisor process, so a single value suffices.
// Accessed only on the single VM-setup path (prepared before confinement, read
// by networkDevices) before any VM thread runs, so the access is serialized.
nonisolated(unsafe) var hostFDFrameEnd: Int32 = -1
nonisolated(unsafe) var hostFDDatapath: Process?

func hostFDEgressEnabled() -> Bool {
    ProcessInfo.processInfo.environment["MICROAGENT_APPLEVF_HOSTFD"] == "1"
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

// prepareHostFDEgressBeforeConfinement creates the guest NIC socketpair and
// spawns the egress datapath subprocess on the peer end. It must be called
// before applyConfinement so the datapath runs unsandboxed (full network access
// for NAT, and able to exec). No-op when host-fd egress is disabled or already
// prepared.
func prepareHostFDEgressBeforeConfinement() throws {
    guard hostFDEgressEnabled(), hostFDFrameEnd < 0 else { return }
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
    proc.arguments = ["--egress-datapath", "--fd", "0", "--gateway-ip", hostFDGatewayIP]
    // The datapath reads guest frames from its stdin (the peer socket end).
    proc.standardInput = FileHandle(fileDescriptor: datapathEnd, closeOnDealloc: false)
    proc.standardOutput = FileHandle.nullDevice
    // standardError is inherited so datapath logs surface in the supervisor log.
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

// makeHostFDNetworkDevice attaches the framework end of the prepared socket as
// the guest NIC. prepareHostFDEgressBeforeConfinement must have run first.
@available(macOS 13.0, *)
func makeHostFDNetworkDevice() throws -> VZVirtioNetworkDeviceConfiguration {
    guard hostFDFrameEnd >= 0 else {
        throw ProtocolError.invalid("apple-vf host-fd: egress datapath was not prepared before confinement")
    }
    let handle = FileHandle(fileDescriptor: hostFDFrameEnd, closeOnDealloc: true)
    let attachment = VZFileHandleNetworkDeviceAttachment(fileHandle: handle)
    attachment.maximumTransmissionUnit = 1500
    let device = VZVirtioNetworkDeviceConfiguration()
    device.attachment = attachment
    return device
}
