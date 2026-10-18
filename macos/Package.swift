// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "KyDrive",
    platforms: [.macOS(.v13)],
    targets: [
        .target(name: "KyDriveKit"),
        .executableTarget(name: "KyDrive", dependencies: ["KyDriveKit"]),
        .testTarget(name: "KyDriveKitTests", dependencies: ["KyDriveKit"]),
    ]
)
