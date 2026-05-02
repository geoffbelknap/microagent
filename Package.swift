// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "microagent-vmkit",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .library(
            name: "MicroAgentVMKit",
            targets: ["MicroAgentVMKit"]
        ),
        .executable(
            name: "microagent-vmkit",
            targets: ["microagent-vmkit"]
        )
    ],
    targets: [
        .target(
            name: "MicroAgentVMKit"
        ),
        .executableTarget(
            name: "microagent-vmkit",
            dependencies: ["MicroAgentVMKit"]
        ),
        .testTarget(
            name: "MicroAgentVMKitTests",
            dependencies: ["MicroAgentVMKit"]
        )
    ]
)
