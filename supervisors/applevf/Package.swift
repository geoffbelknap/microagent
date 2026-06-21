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
        .executableTarget(
            name: "microagent-applevf-supervisor"
        ),
        .testTarget(
            name: "microagent-applevf-supervisorTests",
            dependencies: ["microagent-applevf-supervisor"]
        )
    ]
)
