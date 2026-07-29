import Foundation
@testable import microagent_applevf_supervisor
import XCTest

// The guest result payload can carry stdout/stderr; it must be written
// owner-only, and born that way rather than chmod'ed after landing.
final class ResultWriteTests: XCTestCase {
    func testWriteDataAtomically0600() throws {
        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("result-write-tests-\(UUID().uuidString)")
        let target = dir.appendingPathComponent("result.json")

        try writeDataAtomically0600(Data("first".utf8), to: target)
        let attrs = try FileManager.default.attributesOfItem(atPath: target.path)
        XCTAssertEqual((attrs[.posixPermissions] as? NSNumber)?.uint16Value, 0o600)
        XCTAssertEqual(try Data(contentsOf: target), Data("first".utf8))

        // Overwrites atomically and keeps the restricted mode.
        try writeDataAtomically0600(Data("second".utf8), to: target)
        let attrs2 = try FileManager.default.attributesOfItem(atPath: target.path)
        XCTAssertEqual((attrs2[.posixPermissions] as? NSNumber)?.uint16Value, 0o600)
        XCTAssertEqual(try Data(contentsOf: target), Data("second".utf8))

        // No temp files left behind.
        let leftovers = try FileManager.default.contentsOfDirectory(atPath: dir.path)
        XCTAssertEqual(leftovers, ["result.json"])
    }
}
