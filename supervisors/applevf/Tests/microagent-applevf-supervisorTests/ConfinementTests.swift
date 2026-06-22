import Darwin
import Foundation
@testable import microagent_applevf_supervisor
import XCTest

final class ConfinementTests: XCTestCase {
    func testSeatbeltProfileDeniesByDefaultAndAllowsWritableSurface() {
        let profile = seatbeltProfile(
            writableSubpaths: ["/var/run/microagent/web"],
            writableFiles: ["/images/rootfs.img"]
        )
        // Deny-by-default is the security floor.
        XCTAssertTrue(profile.contains("(deny default)"), "profile must deny by default")
        // The high-value confinement: writes are scoped to the allow-list.
        XCTAssertTrue(profile.contains("(allow file-write* (subpath \"/var/run/microagent/web\"))"))
        XCTAssertTrue(profile.contains("(allow file-write* (literal \"/images/rootfs.img\"))"))
        // Network egress is loopback-only.
        XCTAssertTrue(profile.contains("(allow network-outbound (remote ip \"localhost:*\"))"))
        XCTAssertFalse(profile.contains("(allow network-outbound (remote ip \"*\"))"))
        // Reads stay broad in this rollout posture.
        XCTAssertTrue(profile.contains("(allow file-read*)"))
    }

    func testSeatbeltProfileEmitsPrivateVariantForVarPaths() {
        let profile = seatbeltProfile(writableSubpaths: ["/var/folders/ab/cd/T/ws"], writableFiles: [])
        XCTAssertTrue(profile.contains("(subpath \"/var/folders/ab/cd/T/ws\")"))
        XCTAssertTrue(
            profile.contains("(subpath \"/private/var/folders/ab/cd/T/ws\")"),
            "must also allow the /private-prefixed canonical form the sandbox sees"
        )
    }

    func testSbplQuoteEscapesQuotesAndBackslashes() {
        XCTAssertEqual(sbplQuote("/a/b"), "\"/a/b\"")
        XCTAssertEqual(sbplQuote("/a/\"q\"/b"), "\"/a/\\\"q\\\"/b\"")
        XCTAssertEqual(sbplQuote("/a/\\b"), "\"/a/\\\\b\"")
    }

    func testBuildSeatbeltProfileWritesRootfsAndRWDisksOnly() {
        let identity = Identity(requestID: "r", runtimeID: "web", role: .workload, backend: "apple-vf", homeHash: nil)
        var config = Config(kernelPath: "/k/Image", rootfsPath: "/img/root.img", stateDir: "/state")
        config.disks = [
            Disk(name: "data", path: "/img/data.img", mountpoint: "/data", mode: "rw"),
            Disk(name: "ref", path: "/img/ref.img", mountpoint: "/ref", mode: "ro"),
        ]
        let profile = buildSeatbeltProfile(identity: identity, config: config)
        // rootfs is always rw.
        XCTAssertTrue(profile.contains("(allow file-write* (literal \"/img/root.img\"))"))
        // rw disk is writable; ro disk is not granted write.
        XCTAssertTrue(profile.contains("(allow file-write* (literal \"/img/data.img\"))"))
        XCTAssertFalse(profile.contains("(literal \"/img/ref.img\")"))
        // The workspace runtime dir is writable.
        XCTAssertTrue(profile.contains("(subpath \"/state/web\")"))
    }

    func testSelfCheckProfileApplies() throws {
        // The self-check profile must be valid SBPL that sandbox_init accepts on
        // this host — this is the honesty basis for ConfinementActive. Apply it in
        // a subprocess so we don't confine the test runner itself.
        let supervisor = productsExecutableURL()
        let process = Process()
        process.executableURL = supervisor
        process.arguments = ["--confinement-selfcheck"]
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
        } catch {
            throw XCTSkip("supervisor executable not runnable in this environment: \(error)")
        }
        process.waitUntilExit()
        XCTAssertEqual(process.terminationStatus, 0, "self-check must succeed (sandbox applies)")
    }

    // Resolves the built supervisor binary alongside the test bundle.
    private func productsExecutableURL() -> URL {
        let bundleDir = Bundle(for: type(of: self)).bundleURL.deletingLastPathComponent()
        return bundleDir.appendingPathComponent("microagent-applevf-supervisor")
    }
}
