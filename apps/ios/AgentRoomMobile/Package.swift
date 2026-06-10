// swift-tools-version: 5.9

import PackageDescription

let package = Package(
    name: "AgentRoomMobile",
    platforms: [
        .iOS(.v17),
        .macOS(.v14)
    ],
    products: [
        .library(
            name: "AgentRoomMobile",
            targets: ["AgentRoomMobile"]
        )
    ],
    targets: [
        .target(
            name: "AgentRoomMobile"
        ),
        .testTarget(
            name: "AgentRoomMobileTests",
            dependencies: ["AgentRoomMobile"]
        )
    ]
)
