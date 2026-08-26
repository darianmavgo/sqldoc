// The bundle's executable.
//
// Double-clicking a document does not pass its path in argv. Launch Services
// sends the application an "open documents" Apple Event instead, and a program
// that only reads os.Args — as the Go viewer does — never learns which file the
// user actually asked for. It launches, finds nothing to show, and sits there.
//
// This launcher exists to receive that event. It is a real NSApplication, so
// the event reaches it; it then starts the viewer as a child process with the
// paths on the command line, which is the one interface the viewer needs. It
// stays alive afterwards so that opening a second database while the first is
// still on screen delivers another event here and opens another window, rather
// than silently activating the running app and doing nothing.

import AppKit

final class Launcher: NSObject, NSApplicationDelegate {
    private var pending: [String] = []
    private var running: Set<NSRunningApplication> = []
    private var outstanding = 0
    private var watcher: Timer?
    private var started = false

    // The viewer is a nested application bundle rather than a bare executable,
    // and it is started through Launch Services rather than forked directly.
    // A child process spawned from here inherits no GUI session: it runs
    // perfectly, serves its port, and never puts a window on screen. Going
    // through Launch Services is what gets it a session that can own windows.
    private var viewerApp: URL {
        Bundle.main.bundleURL.appendingPathComponent("Contents/Library/Viewer.app")
    }

    // Delivered for a double-click, for "Open With", and for a drop onto the
    // Dock icon. It can arrive before or after didFinishLaunching.
    func application(_ sender: NSApplication, openFiles filenames: [String]) {
        if started {
            spawn(filenames)
        } else {
            pending.append(contentsOf: filenames)
        }
        sender.reply(toOpenOrPrint: .success)
    }

    func applicationDidFinishLaunching(_ note: Notification) {
        // Anything after the executable path is a file to open; Launch Services
        // adds arguments of its own, which are skipped.
        let argv = CommandLine.arguments.dropFirst().filter { !$0.hasPrefix("-") }
        pending.append(contentsOf: argv)

        // The open-documents event arrives just after launch rather than
        // before, so give it a moment before deciding there is nothing to open.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) { [self] in
            started = true
            spawn(pending)
            pending = []
        }
    }

    // Opening with nothing to show is legitimate: the viewer puts up its start
    // page, with recent files and a place to drop one.
    private func spawn(_ files: [String]) {
        let config = NSWorkspace.OpenConfiguration()
        config.arguments = files
        config.activates = true
        // Each open gets its own instance, so opening a second database while
        // the first is on screen gives a second window instead of quietly
        // activating the one already there.
        config.createsNewApplicationInstance = true

        outstanding += 1
        NSWorkspace.shared.openApplication(at: viewerApp, configuration: config) { [weak self] app, error in
            DispatchQueue.main.async { self?.launched(app, error) }
        }
    }

    private func launched(_ app: NSRunningApplication?, _ error: Error?) {
        outstanding -= 1
        if let error {
            let a = NSAlert()
            a.messageText = "sqldoc could not open a window"
            a.informativeText = "\(viewerApp.path)\n\n\(error.localizedDescription)"
            a.alertStyle = .critical
            a.runModal()
            NSApp.terminate(nil)
            return
        }
        guard let app else { return }
        running.insert(app)
        // NSRunningApplication reports termination through KVO rather than a
        // callback, so the launcher polls: it only needs to know when the last
        // window has gone so it can stop too.
        watch()
    }

    private func watch() {
        guard watcher == nil else { return }
        watcher = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            guard let self else { return }
            running = running.filter { !$0.isTerminated }
            if running.isEmpty && outstanding == 0 && started {
                NSApp.terminate(nil)
            }
        }
    }

    func applicationWillTerminate(_ note: Notification) {
        watcher?.invalidate()
    }
}

let app = NSApplication.shared
let delegate = Launcher()
app.delegate = delegate
// Accessory rather than regular: the windows belong to the viewer processes,
// and a second icon in the Dock for a launcher with no window is just noise.
app.setActivationPolicy(.accessory)
app.run()
