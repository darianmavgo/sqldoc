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

// Whether a window is the viewer's document window rather than something the
// application merely owns. Size rules out the menu bar and the other small
// system-owned windows; the panel test rules out the open dialog, which is
// document-sized, and which nothing here has any business moving or resizing.
static BOOL sqldocIsDocumentWindow(NSWindow *w) {
    NSRect f = [w frame];
    if (f.size.width < 200 || f.size.height < 200) return NO;
    if ([w isKindOfClass:[NSPanel class]]) return NO;
    if ([w isSheet]) return NO;
    return YES;
}

static void sqldocShowWindows(void) {
    NSApplication *app = [NSApplication sharedApplication];
    [app setActivationPolicy:NSApplicationActivationPolicyRegular];

    NSRect visible = [[NSScreen mainScreen] visibleFrame];
    for (NSWindow *w in [app windows]) {
        if (!sqldocIsDocumentWindow(w)) continue;
        if (!NSIntersectsRect([w frame], visible)) [w center];
        [w makeKeyAndOrderFront:nil];
    }
    [app activateIgnoringOtherApps:YES];
}

// A document viewer is something you read in, and the window it opens should be
// the size of the thing being read rather than a rectangle you resize every
// time. Filling the screen the window is already on - visibleFrame, so the menu
// bar and the Dock keep their space - is the version of that which leaves the
// window a window: it stays in the current Space, it keeps its title bar, and
// nothing has to be dismissed to get back to what was underneath.
static void sqldocFillScreenNow(void) {
    NSApplication *app = [NSApplication sharedApplication];
    for (NSWindow *w in [app windows]) {
        if (!sqldocIsDocumentWindow(w)) continue;
        NSScreen *screen = [w screen];
        if (screen == nil) screen = [NSScreen mainScreen];
        [w setFrame:[screen visibleFrame] display:YES];
    }
}

static void sqldocFillScreen(void) {
    // The window may not exist yet, for the same reason activation retries.
    dispatch_async(dispatch_get_main_queue(), ^{ sqldocFillScreenNow(); });
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 300 * NSEC_PER_MSEC),
                   dispatch_get_main_queue(), ^{ sqldocFillScreenNow(); });
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

// fillScreen opens the window at the size of the screen it is on.
func fillScreen() { C.sqldocFillScreen() }
