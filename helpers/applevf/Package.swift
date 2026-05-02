// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "microagent-applevf-helper",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .executable(
            name: "microagent-applevf-helper",
            targets: ["microagent-applevf-helper"]
        )
    ],
    targets: [
        .executableTarget(
            name: "microagent-applevf-helper"
        )
    ]
)
