// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "AgentctlFleet",
    platforms: [
        .macOS(.v14)
    ],
    products: [
        .executable(name: "AgentctlFleet", targets: ["AgentctlFleet"])
    ],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm", from: "1.20.0")
    ],
    targets: [
        .executableTarget(
            name: "AgentctlFleet",
            dependencies: [
                .product(name: "SwiftTerm", package: "SwiftTerm")
            ]
        ),
        .testTarget(
            name: "AgentctlFleetTests",
            dependencies: ["AgentctlFleet"]
        )
    ]
)
