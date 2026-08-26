//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// Two things go wrong when the viewer runs from an application bundle rather
// than from a shell, and both of them look exactly like "the app didn't start".
//
// The window is placed off the side of the display — measured at x = -1523 on a
// 1728-point screen — so it is genuinely on screen and genuinely invisible. Any
// window that does not intersect the visible frame is recentred.
//
// The application also has to be told to come forward. Ordering the window
// front and activating is what actually puts it in front of whatever the user
// was looking at when they double-clicked the file.
static void sqldocShowWindows(void) {
    NSApplication *app = [NSApplication sharedApplication];
    [app setActivationPolicy:NSApplicationActivationPolicyRegular];

    NSRect visible = [[NSScreen mainScreen] visibleFrame];
    for (NSWindow *w in [app windows]) {
        NSRect f = [w frame];
        // The menu bar and other small system-owned windows are left alone;
        // only something document-sized is the viewer's own window.
        if (f.size.width < 200 || f.size.height < 200) continue;
        if (!NSIntersectsRect(f, visible)) [w center];
        [w makeKeyAndOrderFront:nil];
    }
    [app activateIgnoringOtherApps:YES];
}

static void sqldocActivate(void) {
    // The window may not exist yet when this is called, since webview creates
    // it as the run loop starts. Once on the first turn of the loop, and again
    // shortly after, covers both orders without needing to guess.
    dispatch_async(dispatch_get_main_queue(), ^{ sqldocShowWindows(); });
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC),
                   dispatch_get_main_queue(), ^{ sqldocShowWindows(); });
}
*/
import "C"

// activate brings the viewer's window onto the screen and in front.
func activate() { C.sqldocActivate() }
