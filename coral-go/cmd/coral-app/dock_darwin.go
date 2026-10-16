//go:build webview

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

// showInDock sets the app to appear in the macOS Dock as a regular application
// and loads the app icon from the bundle's Resources directory.
void showInDock() {
    dispatch_async(dispatch_get_main_queue(), ^{
        NSLog(@"[DOCK] showInDock: setting activation policy");
        NSApplication *app = [NSApplication sharedApplication];
        [app setActivationPolicy:NSApplicationActivationPolicyRegular];
        NSLog(@"[DOCK] activation policy set to Regular");

        // Load the app icon from the bundle
        NSBundle *bundle = [NSBundle mainBundle];
        NSString *iconPath = [bundle pathForResource:@"AppIcon" ofType:@"icns"];
        NSLog(@"[DOCK] icon path: %@", iconPath ?: @"(not found)");
        if (iconPath) {
            NSImage *icon = [[NSImage alloc] initWithContentsOfFile:iconPath];
            if (icon) {
                [app setApplicationIconImage:icon];
                NSLog(@"[DOCK] app icon set");
            }
        }
    });
}

*/
import "C"

func showInDock() {
	C.showInDock()
}
