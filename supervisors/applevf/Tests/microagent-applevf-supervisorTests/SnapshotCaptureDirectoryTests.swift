import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// snapshotCaptureDirectory decides where a snapshot capture writes its
// artifacts. With a host-provided staging directory the capture must land
// there — the host then publishes it atomically over the tag directory — so a
// failed capture never destroys the prior snapshot at the tag. Without one,
// the legacy in-place tag directory keeps older hosts working.
final class SnapshotCaptureDirectoryTests: XCTestCase {
    private func identity() -> Identity {
        Identity(requestID: "r", runtimeID: "ws", role: .workload, backend: "apple-vf", homeHash: nil)
    }

    func testStagingDirectoryWinsWhenProvided() throws {
        let dir = try snapshotCaptureDirectory(
            identity: identity(), stateDir: "/state", tag: "base",
            stagingDir: "/state/ws/.snapshot-staging/base-123"
        )
        XCTAssertEqual(dir.path, "/state/ws/.snapshot-staging/base-123")
    }

    func testFallsBackToTagDirectoryWithoutStaging() throws {
        for stagingDir in [nil, "", "   "] {
            let dir = try snapshotCaptureDirectory(
                identity: identity(), stateDir: "/state", tag: "base", stagingDir: stagingDir
            )
            XCTAssertEqual(
                dir.path,
                snapshotDirectory(identity: identity(), stateDir: "/state", tag: "base").path
            )
        }
    }

    func testRejectsRelativeStagingDirectory() {
        XCTAssertThrowsError(
            try snapshotCaptureDirectory(
                identity: identity(), stateDir: "/state", tag: "base", stagingDir: "relative/staging"
            ),
            "a relative staging path must be rejected fail-closed"
        )
    }
}
