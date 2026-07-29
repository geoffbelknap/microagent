import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// linuxKernelCommandLine is how the supervisor tells guest init which vsock
// services exist. A port the supervisor drops here is a silent feature gap in
// the guest: caCertPort was dropped exactly this way, so mitm workspaces
// booted without the per-workspace CA and guest TLS could not verify
// intercepted connections (credential swap broke on apple-vf while working on
// Firecracker, which passes the same port via its own cmdline builder).
final class KernelCommandLineTests: XCTestCase {
    private func config() -> Config {
        Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
    }

    func testCACertPortForwarded() {
        var cfg = config()
        cfg.caCertPort = 1030
        XCTAssertTrue(
            linuxKernelCommandLine(for: cfg).contains("microagent_ca_cert_port=1030"),
            "caCertPort must reach the guest cmdline or mitm CA delivery silently never happens"
        )
    }

    func testCACertPortOmittedWhenUnset() {
        XCTAssertFalse(linuxKernelCommandLine(for: config()).contains("microagent_ca_cert_port"))
    }

    // The cmdline gate must key on the resolved guest port like the
    // firecracker builder: a config setting only the guest override must
    // still announce the service (gating on the raw host field dropped it).
    func testShellExecAnnouncedFromGuestPortOverrideAlone() {
        var cfg = config()
        cfg.guestShellPort = 22001
        cfg.guestExecPort = 42001
        let cmdline = linuxKernelCommandLine(for: cfg)
        XCTAssertTrue(cmdline.contains("microagent_shell_port=22001"), cmdline)
        XCTAssertTrue(cmdline.contains("microagent_exec_port=42001"), cmdline)
    }

    func testShellExecOmittedWhenNoPortsAtAll() {
        let cmdline = linuxKernelCommandLine(for: config())
        XCTAssertFalse(cmdline.contains("microagent_shell_port"))
        XCTAssertFalse(cmdline.contains("microagent_exec_port"))
    }

    // The host-fd datapath forwards guest DNS only to the declared resolvers,
    // so resolv.conf must carry them: a fixed default here plus a declared
    // allowlist there made every guest query refusable.
    func testHostFDGuestDNSFollowsDeclaredResolvers() {
        var cfg = config()
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.dns = [" 8.8.4.4 ", ""]
        cfg.egressMode = "broker"
        let cmdline = linuxKernelCommandLine(for: cfg)
        XCTAssertTrue(cmdline.contains("microagent_net_dns=8.8.4.4"), cmdline)
        XCTAssertTrue(cmdline.contains("microagent_net_ip=\(hostFDGuestIP)"), cmdline)
        XCTAssertTrue(cmdline.contains("microagent_net_gw=\(hostFDGatewayIP)"), cmdline)
    }

    func testHostFDGuestDNSDefaultsWithoutDeclaredResolvers() {
        var cfg = config()
        cfg.network = NetworkConfig(mode: "user")
        cfg.egressMode = "broker"
        XCTAssertTrue(linuxKernelCommandLine(for: cfg).contains("microagent_net_dns=\(hostFDGuestDNS)"))
    }

    // Static user-mode addressing with no declared nameservers must still
    // resolve, matching the firecracker supervisor's injected default.
    func testStaticUserModeInjectsDefaultDNS() {
        var cfg = config()
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.ip = "10.0.0.5/24"
        cfg.network?.gateway = "10.0.0.1"
        cfg.egressMode = "off"
        XCTAssertTrue(
            linuxKernelCommandLine(for: cfg).contains("microagent_net_dns=\(staticUserDefaultDNS.joined(separator: ","))")
        )
    }

    func testStaticUserModeKeepsDeclaredDNS() {
        var cfg = config()
        cfg.network = NetworkConfig(mode: "user")
        cfg.network?.ip = "10.0.0.5/24"
        cfg.network?.gateway = "10.0.0.1"
        cfg.network?.dns = ["10.0.0.53"]
        cfg.egressMode = "off"
        XCTAssertTrue(linuxKernelCommandLine(for: cfg).contains("microagent_net_dns=10.0.0.53"))
    }
}
