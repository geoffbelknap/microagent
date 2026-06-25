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
// S1 is opt-in behind MICROAGENT_APPLEVF_HOSTFD=1 so it does not disturb the
// fail-closed default while the datapath is validated; S4 makes it the default
// for mediated egress.

// Static subnet for the host-fd gateway. The gateway owns .1; the guest is
// configured with .2 via the kernel cmdline.
let hostFDGatewayIP = "192.168.127.1"
let hostFDGuestIP = "192.168.127.2"
let hostFDGuestDNS = "1.1.1.1"

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

// makeHostFDNetworkDevice creates the guest NIC backed by a datagram socketpair
// and spawns the egress datapath subprocess on the peer end. The subprocess
// reads the socket as its stdin (fd 0) and exits when the socket closes (i.e.
// when the VM tears down and releases the framework end), so it is self-reaping.
@available(macOS 13.0, *)
func makeHostFDNetworkDevice() throws -> VZVirtioNetworkDeviceConfiguration {
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

    let handle = FileHandle(fileDescriptor: frameEnd, closeOnDealloc: true)
    let attachment = VZFileHandleNetworkDeviceAttachment(fileHandle: handle)
    attachment.maximumTransmissionUnit = 1500
    let device = VZVirtioNetworkDeviceConfiguration()
    device.attachment = attachment
    return device
}
