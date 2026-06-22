// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "microagent-applevf-supervisor",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .executable(
            name: "microagent-applevf-supervisor",
            targets: ["microagent-applevf-supervisor"]
        )
    ],
    targets: [
        // Thin C shim over libSystem's sandbox_init so the Swift supervisor can
        // apply a Seatbelt profile without importing the deprecated <sandbox.h>.
        .target(
            name: "CMicroagentSandbox"
        ),
        .executableTarget(
            name: "microagent-applevf-supervisor",
            dependencies: ["CMicroagentSandbox"]
        ),
        .testTarget(
            name: "microagent-applevf-supervisorTests",
            dependencies: ["microagent-applevf-supervisor"]
        )
    ]
)
