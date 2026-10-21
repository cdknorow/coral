#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include "_cgo_export.h"

// Whether macOS started this app as a login item: the launch Apple Event carries keyAEPropData = keyAELaunchedAsLogInItem ('prdt' = 'lgit'). It is read when the app is about to
// finish launching, which is the only time that event is current; the observer is added before main, as the notification centre exists then.
static volatile int gLaunchedAsLoginItem = 0;

__attribute__((constructor)) static void coralWatchLaunch(void) {
    [[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationWillFinishLaunchingNotification
                                                      object:nil
                                                       queue:nil
                                                  usingBlock:^(NSNotification *note) {
        NSAppleEventDescriptor *event = [[NSAppleEventManager sharedAppleEventManager] currentAppleEvent];
        NSAppleEventDescriptor *prop = [event paramDescriptorForKeyword:0x70726474 /* 'prdt' keyAEPropData */];
        if (prop != nil && [prop enumCodeValue] == 0x6C676974 /* 'lgit' keyAELaunchedAsLogInItem */) {
            gLaunchedAsLoginItem = 1;
        }
    }];
}

int coralLaunchedAsLoginItem(void) { return gLaunchedAsLoginItem; }

// The app is opened again while it runs: macOS sends the application delegate applicationShouldHandleReopen:hasVisibleWindows:. systray's delegate does not have it, so it is
// added to the delegate's class; the Go side shows the window. Returning YES leaves AppKit's own handling alone.
static BOOL coralShouldHandleReopen(id self, SEL _cmd, NSApplication *app, BOOL hasVisibleWindows) {
    coralTrayReopen();
    return YES;
}

void coralInstallReopenHandler(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        id delegate = [NSApp delegate];
        if (delegate == nil) {
            return;
        }
        Class cls = [delegate class];
        SEL sel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
        if (class_getInstanceMethod(cls, sel) != NULL) {
            return;
        }
        NSString *types = [NSString stringWithFormat:@"%s@:@%s", @encode(BOOL), @encode(BOOL)];
        class_addMethod(cls, sel, (IMP)coralShouldHandleReopen, [types UTF8String]);
    });
}
