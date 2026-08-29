// NSOpenPanel, owned by the viewer.
//
// Asking Finder to put up "choose file" works from a program with no event loop
// of its own, and it is what internal/pick falls back to. What it cannot do is
// end well: the dialog belongs to Finder, so Finder is the application in front
// when it closes, and the window the file was chosen for is left behind it. No
// other Mac application behaves that way because no other Mac application
// borrows its file dialog.
//
// This is the dialog every other application uses. It belongs to this process,
// it hangs off this window as a sheet, and when it closes the window it is
// attached to is already in front and already showing the database. There is
// nothing to bring back afterwards, because nothing ever went away.

#import <Cocoa/Cocoa.h>
#include "openpanel_darwin.h"

// Implemented in Go, in openpanel_darwin.go.
extern void sqldocPanelDone(uintptr_t handle, const char *path);

// The window to hang the sheet on. Ordinarily the key window is the viewer's
// and there is nothing to think about; the size test is the same one activate()
// uses to tell a document window from the small system-owned windows an
// application always has.
static NSWindow *sqldocDocumentWindow(NSApplication *app) {
    NSWindow *w = [app keyWindow];
    if (w == nil) w = [app mainWindow];
    if (w != nil) return w;
    for (NSWindow *candidate in [app windows]) {
        NSRect f = [candidate frame];
        if (f.size.width >= 200 && f.size.height >= 200 && [candidate isVisible]) {
            return candidate;
        }
    }
    return nil;
}

void sqldocOpenPanel(const char *prompt, uintptr_t handle) {
    NSString *message = prompt ? [NSString stringWithUTF8String:prompt] : @"";

    // AppKit will not put a panel on screen from anywhere but the main thread,
    // and the caller is an HTTP handler on some goroutine's thread. Queueing it
    // is also what keeps the request from blocking the run loop that has to
    // draw the panel.
    dispatch_async(dispatch_get_main_queue(), ^{
        NSApplication *app = [NSApplication sharedApplication];
        [app activateIgnoringOtherApps:YES];

        NSOpenPanel *panel = [NSOpenPanel openPanel];
        panel.message = message;
        panel.canChooseFiles = YES;
        panel.canChooseDirectories = NO;
        panel.allowsMultipleSelection = NO;
        panel.showsHiddenFiles = NO;
        // Deliberately no type filter. Restricting to a list of extensions
        // greys out databases named anything else, and SQLite files routinely
        // carry an unexpected extension or none at all; a picker that will not
        // let you select the file you are pointing at is worse than one that
        // accepts anything and reports a clear error when it is not a database.

        void (^done)(NSModalResponse) = ^(NSModalResponse result) {
            const char *path = NULL;
            if (result == NSModalResponseOK) {
                path = [[[panel URL] path] UTF8String];
            }
            // Copied on the Go side before this autorelease pool drains.
            sqldocPanelDone(handle, path);
        };

        NSWindow *host = sqldocDocumentWindow(app);
        if (host != nil) {
            [panel beginSheetModalForWindow:host completionHandler:done];
        } else {
            // Nothing to attach to yet: a free-standing panel still belongs to
            // this application, which is the part that matters.
            [panel beginWithCompletionHandler:done];
        }
    });
}
