// Makes sqldoc the application macOS opens a SQLite database with, so a
// double-click in Finder works without going through "Open With" every time.
//
// This is deliberately a separate step rather than something the app does when
// it launches: taking over a file type behind someone's back is obnoxious, and
// the Info.plist claims the type at rank "Alternate" precisely so that
// installing sqldoc does not change what already opens .db files. Running this
// is the moment the choice is actually made.

import Foundation
import AppKit
import UniformTypeIdentifiers

let bundleID = "com.mavgo.sqldoc"

// The extensions a SQLite file commonly carries. Each is resolved to whatever
// type identifier this machine actually uses for it, which is a dynamic one on
// systems where no installed application claims the extension.
let extensions = ["db", "sqlite", "sqlite3", "db3", "s3db", "sl3"]

guard let appURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID) else {
    FileHandle.standardError.write("sqldoc.app is not installed\n".data(using: .utf8)!)
    exit(1)
}

var types: [UTType] = []
for ext in extensions {
    if let t = UTType(filenameExtension: ext) { types.append(t) }
}
if let declared = UTType("com.mavgo.sqldoc.sqlite") { types.append(declared) }

var failures = 0
let group = DispatchGroup()

for type in types {
    group.enter()
    NSWorkspace.shared.setDefaultApplication(at: appURL, toOpen: type) { error in
        if let error {
            FileHandle.standardError.write(
                "  \(type.identifier): \(error.localizedDescription)\n".data(using: .utf8)!)
            failures += 1
        } else {
            print("  \(type.preferredFilenameExtension.map { "." + $0 } ?? type.identifier) -> sqldoc")
        }
        group.leave()
    }
}

// The calls are asynchronous and the process must stay alive until they land.
if group.wait(timeout: .now() + 20) == .timedOut {
    FileHandle.standardError.write("timed out waiting for Launch Services\n".data(using: .utf8)!)
    exit(1)
}
exit(failures > 0 ? 1 : 0)
