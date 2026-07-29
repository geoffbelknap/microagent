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
}
