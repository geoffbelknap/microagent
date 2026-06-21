import Darwin
import Foundation
@testable import microagent_applevf_supervisor
import XCTest

final class NamedNetworkTests: XCTestCase {
    func testNamedNetworkRuntimeConfigAllocatesStaticGuestFieldsAndHosts() throws {
        let dir = try temporaryDirectory()
        let index = NamedNetworkIndex(networks: [
            NamedNetworkRecord(
                name: "devnet",
                subnet: "10.44.71.0/24",
                gateway: "10.44.71.1",
                createdAt: nil,
                members: [NamedNetworkMember(workspace: "db", ip: "10.44.71.2")]
            )
        ])
        try writeNamedNetworkIndex(stateDir: dir.path, index: index)

        let identity = Identity(requestID: "req-1", runtimeID: "web", role: .workload, backend: "apple-vf", homeHash: nil)
        let config = Config(
            kernelPath: "/tmp/Image",
            rootfsPath: "/tmp/rootfs.ext4",
            stateDir: dir.path,
            memoryMiB: 512,
            cpuCount: 1,
            disks: nil,
            vsockListeners: nil,
            mediation: nil,
            network: NetworkConfig(mode: "named", interface: nil, name: "devnet", portForwards: nil, dns: ["1.1.1.1"], routes: nil, ip: nil, subnet: nil, gateway: nil, hosts: nil),
            shellPort: nil,
            execPort: nil,
            leaseSeconds: nil,
            guestExecPort: nil,
            secretsPort: nil,
            secrets: nil,
            secretEnvFiles: nil,
            onDemandSecrets: nil,
            secretsAudit: nil,
            secretsControlPort: nil,
            modelGuestPort: nil,
            modelVsockPort: nil,
            serialInput: nil
        )

        let runtime = try namedNetworkRuntimeConfig(identity: identity, config: config).config
        XCTAssertEqual(runtime.mode, "named")
        XCTAssertEqual(runtime.name, "devnet")
        XCTAssertEqual(runtime.ip, "10.44.71.3/24")
        XCTAssertEqual(runtime.subnet, "10.44.71.0/24")
        XCTAssertEqual(runtime.gateway, "10.44.71.1")
        XCTAssertEqual(runtime.dns ?? [], ["1.1.1.1"])
        XCTAssertEqual(runtime.hosts ?? [], ["db:10.44.71.2", "web:10.44.71.3"])

        let reread = try readNamedNetworkIndex(stateDir: dir.path)
        XCTAssertEqual(reread.networks.first?.members?.map(\.workspace), ["db", "web"])
    }

    func testNamedNetworkSwitchFloodsBroadcastAndLearnsUnicast() throws {
        let dir = try temporaryDirectory()
        let switchPath = dir.appendingPathComponent("switch.sock").path
        let spec = NamedNetworkSwitchSpec(name: "devnet", socketPath: switchPath)
        let ready = expectation(description: "switch ready")
        Thread.detachNewThread {
            do {
                try runNamedNetworkSwitch(spec: spec)
            } catch {
                XCTFail("switch failed: \(error)")
            }
        }
        let deadline = Date().addingTimeInterval(2)
        while Date() < deadline {
            if canConnectUnixDatagram(path: switchPath) {
                ready.fulfill()
                break
            }
            usleep(20_000)
        }
        wait(for: [ready], timeout: 2)

        let aPath = dir.appendingPathComponent("a.sock").path
        let bPath = dir.appendingPathComponent("b.sock").path
        let aFD = try connectedDatagramSocket(localPath: aPath, remotePath: switchPath)
        let bFD = try connectedDatagramSocket(localPath: bPath, remotePath: switchPath)
        defer {
            close(aFD)
            close(bFD)
            _ = unlink(aPath)
            _ = unlink(bPath)
        }
        try sendConnectedDatagram(fd: aFD, frame: [])
        try sendConnectedDatagram(fd: bFD, frame: [])

        let broadcast = ethernetFrame(dst: [0xff, 0xff, 0xff, 0xff, 0xff, 0xff], src: [0x02, 0, 0, 0, 0, 1], payload: [1])
        try sendConnectedDatagram(fd: aFD, frame: broadcast)
        XCTAssertEqual(try recvDatagram(fd: bFD).suffix(1), [1])

        let unicast = ethernetFrame(dst: [0x02, 0, 0, 0, 0, 1], src: [0x02, 0, 0, 0, 0, 2], payload: [2])
        try sendConnectedDatagram(fd: bFD, frame: unicast)
        XCTAssertEqual(try recvDatagram(fd: aFD).suffix(1), [2])
    }

    private func temporaryDirectory() throws -> URL {
        let url = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func ethernetFrame(dst: [UInt8], src: [UInt8], payload: [UInt8]) -> [UInt8] {
        dst + src + [0x08, 0x00] + payload
    }

    private func sendConnectedDatagram(fd: Int32, frame: [UInt8]) throws {
        let sent = frame.withUnsafeBytes { send(fd, $0.baseAddress, frame.count, 0) }
        if sent < 0 {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
    }

    private func recvDatagram(fd: Int32) throws -> [UInt8] {
        var timeout = timeval(tv_sec: 2, tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &timeout, socklen_t(MemoryLayout<timeval>.size))
        var buffer = [UInt8](repeating: 0, count: 2048)
        let n = recv(fd, &buffer, buffer.count, 0)
        if n < 0 {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        return Array(buffer[0..<n])
    }
}
