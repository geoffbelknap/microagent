import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// Runtime state and responses must report the addressing the guest actually
// receives, matching the firecracker supervisor's runtimeNetworkConfig. A
// declared-spec echo hid the host-fd datapath's real subnet from
// `microagent network` on macOS.
final class EffectiveNetworkTests: XCTestCase {
    private func config() -> Config {
        Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
    }

    func testHostFDReportsRealAddressingWithNilNetwork() {
        var cfg = config()
        cfg.egressMode = "broker"
        let network = effectiveNetworkConfig(cfg)
        XCTAssertEqual(network?.mode, "user")
        XCTAssertEqual(network?.ip, hostFDGuestIP)
        XCTAssertEqual(network?.subnet, hostFDSubnet)
        XCTAssertEqual(network?.gateway, hostFDGatewayIP)
        XCTAssertNil(network?.ipv6)
        XCTAssertNil(network?.ipv6Subnet)
        XCTAssertNil(network?.ipv6Gateway)
        XCTAssertEqual(network?.dns, [hostFDGuestDNS])
        XCTAssertEqual(network?.routes, ["0.0.0.0/0 via \(hostFDGatewayIP)"])
    }

    func testHostFDPreservesDeclaredDNS() {
        var cfg = config()
        cfg.egressMode = "mitm"
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.dns = ["8.8.4.4"]
        XCTAssertEqual(effectiveNetworkConfig(cfg)?.dns, ["8.8.4.4"])
    }

    func testStaticUserModeFillsDefaultsAndRoutes() {
        var cfg = config()
        cfg.egressMode = "off"
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.ip = "10.0.0.5/24"
        cfg.network?.gateway = "10.0.0.1"
        let network = effectiveNetworkConfig(cfg)
        XCTAssertEqual(network?.dns, staticUserDefaultDNS)
        XCTAssertEqual(network?.routes, ["0.0.0.0/0 via 10.0.0.1"])
    }

    func testIsolatedModeIsReturnedUnchanged() {
        var cfg = config()
        cfg.network = NetworkConfig(mode: "isolated")
        XCTAssertEqual(effectiveNetworkConfig(cfg)?.mode, "isolated")
        XCTAssertNil(effectiveNetworkConfig(cfg)?.ip)
    }

    // Declared static addressing under the mediated datapath was silently
    // ignored; it must fail closed instead.
    func testValidatedConfigRejectsStaticAddressingUnderMediation() throws {
        var cfg = try realFilesConfig()
        cfg.egressMode = "broker"
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.ip = "10.0.0.5/24"
        cfg.network?.gateway = "10.0.0.1"
        XCTAssertThrowsError(try validatedConfig(cfg)) { error in
            XCTAssertTrue("\(error)".contains("owns the guest subnet"), "\(error)")
        }
    }

    func testValidatedConfigAllowsDeclaredDNSUnderMediation() throws {
        var cfg = try realFilesConfig()
        cfg.egressMode = "broker"
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.dns = ["8.8.4.4"]
        XCTAssertNoThrow(try validatedConfig(cfg))
    }

    // A snapshot captured before the declared/effective persistence split
    // carries the datapath's own assigned addressing; replaying it must not
    // fail closed against the supervisor's own echo — while any OTHER
    // addressing still does.
    func testValidatedConfigToleratesOwnEffectiveEcho() throws {
        var cfg = try realFilesConfig()
        cfg.egressMode = "broker"
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.ip = hostFDGuestIP
        cfg.network?.gateway = hostFDGatewayIP
        cfg.network?.subnet = hostFDSubnet
        cfg.network?.ipv6 = hostFDGuestIPv6
        cfg.network?.ipv6Gateway = hostFDGatewayIPv6
        cfg.network?.ipv6Subnet = hostFDIPv6Subnet
        cfg.network?.routes = ["0.0.0.0/0 via \(hostFDGatewayIP)", "::/0 via \(hostFDGatewayIPv6)"]
        XCTAssertNoThrow(try validatedConfig(cfg))

        cfg.network?.ip = "10.9.9.9/24"
        XCTAssertThrowsError(try validatedConfig(cfg)) { error in
            XCTAssertTrue("\(error)".contains("owns the guest subnet"), "\(error)")
        }
    }

    // validatedConfig stats kernel/rootfs paths, so give it real temp files.
    private func realFilesConfig() throws -> Config {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("effective-network-tests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let kernel = dir.appendingPathComponent("Image")
        let rootfs = dir.appendingPathComponent("root.img")
        try Data("k".utf8).write(to: kernel)
        try Data("r".utf8).write(to: rootfs)
        return Config(kernelPath: kernel.path, rootfsPath: rootfs.path, stateDir: dir.path)
    }
}
