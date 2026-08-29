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

static void sqldocSetupMenu(void) {
    static BOOL menuInstalled = NO;
    if (menuInstalled) return;
    menuInstalled = YES;

    NSApplication *app = [NSApplication sharedApplication];

    NSMenu *mainMenu = [[NSMenu alloc] initWithTitle:@"MainMenu"];

    // 1. Application (Apple) Menu
    NSMenuItem *appMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:appMenuItem];
    NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"sqldoc"];
    [appMenuItem setSubmenu:appMenu];

    NSMenuItem *aboutItem = [appMenu addItemWithTitle:@"About sqldoc" action:@selector(orderFrontStandardAboutPanel:) keyEquivalent:@""];
    [aboutItem setTarget:app];

    [appMenu addItem:[NSMenuItem separatorItem]];

    NSMenuItem *servicesItem = [appMenu addItemWithTitle:@"Services" action:NULL keyEquivalent:@""];
    NSMenu *servicesMenu = [[NSMenu alloc] initWithTitle:@"Services"];
    [servicesItem setSubmenu:servicesMenu];
    [app setServicesMenu:servicesMenu];

    [appMenu addItem:[NSMenuItem separatorItem]];

    NSMenuItem *hideItem = [appMenu addItemWithTitle:@"Hide sqldoc" action:@selector(hide:) keyEquivalent:@"h"];
    [hideItem setTarget:app];

    NSMenuItem *hideOthersItem = [appMenu addItemWithTitle:@"Hide Others" action:@selector(hideOtherApplications:) keyEquivalent:@"h"];
    [hideOthersItem setKeyEquivalentModifierMask:NSEventModifierFlagOption | NSEventModifierFlagCommand];
    [hideOthersItem setTarget:app];

    NSMenuItem *showAllItem = [appMenu addItemWithTitle:@"Show All" action:@selector(unhideAllApplications:) keyEquivalent:@""];
    [showAllItem setTarget:app];

    [appMenu addItem:[NSMenuItem separatorItem]];

    NSMenuItem *quitItem = [appMenu addItemWithTitle:@"Quit sqldoc" action:@selector(terminate:) keyEquivalent:@"q"];
    [quitItem setTarget:app];

    // 2. File Menu
    NSMenuItem *fileMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:fileMenuItem];
    NSMenu *fileMenu = [[NSMenu alloc] initWithTitle:@"File"];
    [fileMenuItem setSubmenu:fileMenu];
    [fileMenu addItemWithTitle:@"Close Window" action:@selector(performClose:) keyEquivalent:@"w"];

    // 3. Edit Menu (Standard clipboard & editing shortcuts: Cmd+C, Cmd+V, Cmd+X, Cmd+A, Cmd+Z)
    NSMenuItem *editMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:editMenuItem];
    NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
    [editMenuItem setSubmenu:editMenu];

    [editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
    NSMenuItem *redoItem = [editMenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"Z"];
    [redoItem setKeyEquivalentModifierMask:NSEventModifierFlagCommand | NSEventModifierFlagShift];
    [editMenu addItem:[NSMenuItem separatorItem]];
    [editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
    [editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
    [editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
    [editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];

    // 4. View Menu
    NSMenuItem *viewMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:viewMenuItem];
    NSMenu *viewMenu = [[NSMenu alloc] initWithTitle:@"View"];
    [viewMenuItem setSubmenu:viewMenu];

    NSMenuItem *fullScreenItem = [viewMenu addItemWithTitle:@"Toggle Full Screen" action:@selector(toggleFullScreen:) keyEquivalent:@"f"];
    [fullScreenItem setKeyEquivalentModifierMask:NSEventModifierFlagControl | NSEventModifierFlagCommand];

    // 5. Window Menu
    NSMenuItem *windowMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:windowMenuItem];
    NSMenu *windowMenu = [[NSMenu alloc] initWithTitle:@"Window"];
    [windowMenuItem setSubmenu:windowMenu];

    [windowMenu addItemWithTitle:@"Minimize" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
    [windowMenu addItemWithTitle:@"Zoom" action:@selector(performZoom:) keyEquivalent:@""];
    [windowMenu addItem:[NSMenuItem separatorItem]];
    [windowMenu addItemWithTitle:@"Bring All to Front" action:@selector(arrangeInFront:) keyEquivalent:@""];

    // 6. Help Menu
    NSMenuItem *helpMenuItem = [[NSMenuItem alloc] init];
    [mainMenu addItem:helpMenuItem];
    NSMenu *helpMenu = [[NSMenu alloc] initWithTitle:@"Help"];
    [helpMenuItem setSubmenu:helpMenu];
    [app setHelpMenu:helpMenu];

    [app setWindowsMenu:windowMenu];
    [app setMainMenu:mainMenu];

    // Local key event monitor to guarantee Cmd+Q, Cmd+W, Cmd+H, Cmd+M always work even when WKWebView has focus
    [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskKeyDown handler:^NSEvent *(NSEvent *event) {
        NSEventModifierFlags flags = [event modifierFlags] & NSEventModifierFlagDeviceIndependentFlagsMask;
        if (flags == NSEventModifierFlagCommand) {
            NSString *chars = [[event charactersIgnoringModifiers] lowercaseString];
            if ([chars isEqualToString:@"q"]) {
                [NSApp terminate:nil];
                return nil;
            } else if ([chars isEqualToString:@"w"]) {
                [[NSApp keyWindow] performClose:nil];
                return nil;
            } else if ([chars isEqualToString:@"m"]) {
                [[NSApp keyWindow] performMiniaturize:nil];
                return nil;
            } else if ([chars isEqualToString:@"h"]) {
                [NSApp hide:nil];
                return nil;
            }
        } else if (flags == (NSEventModifierFlagCommand | NSEventModifierFlagOption)) {
            NSString *chars = [[event charactersIgnoringModifiers] lowercaseString];
            if ([chars isEqualToString:@"h"]) {
                [NSApp hideOtherApplications:nil];
                return nil;
            }
        }
        return event;
    }];
}

static void sqldocShowWindows(void) {
    NSApplication *app = [NSApplication sharedApplication];
    [app setActivationPolicy:NSApplicationActivationPolicyRegular];
    sqldocSetupMenu();

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

// setupMenu installs the standard native macOS menu bar and keyboard shortcuts.
func setupMenu() { C.sqldocSetupMenu() }

// activate brings the viewer's window onto the screen and in front.
func activate() { C.sqldocActivate() }

// fillScreen opens the window at the size of the screen it is on.
func fillScreen() { C.sqldocFillScreen() }
