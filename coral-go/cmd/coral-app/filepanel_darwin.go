//go:build webview

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// The file chooser of the window's web view (#5743). A web page's <input type="file"> reaches the host through WKUIDelegate's
// runOpenPanelWithParameters; without a delegate that has it, the click does nothing. webview_go sets such a delegate, but hands it to
// the web view autoreleased, and WKWebView.UIDelegate is a weak property: the delegate was gone before the first page loaded (measured:
// UIDelegate is nil right after webview.New), so Import Template and Import from Folder opened nothing. This delegate is held by a
// strong global and replaces it.
@interface CoralUIDelegate : NSObject <WKUIDelegate>
@end

@implementation CoralUIDelegate
- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters
    initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *URLs))completionHandler {
	NSOpenPanel *panel = [NSOpenPanel openPanel];
	// A folder input (webkitdirectory) chooses a folder; any other input chooses files.
	panel.canChooseDirectories = parameters.allowsDirectories;
	panel.canChooseFiles = !parameters.allowsDirectories;
	panel.allowsMultipleSelection = parameters.allowsMultipleSelection;
	void (^done)(NSModalResponse) = ^(NSModalResponse response) {
		completionHandler(response == NSModalResponseOK ? panel.URLs : nil); // nil: cancelled
	};
	NSWindow *window = webView.window;
	if (window) {
		[panel beginSheetModalForWindow:window completionHandler:done];
	} else {
		done([panel runModal]);
	}
}
@end

// WKWebView holds its UIDelegate weakly: this is the strong reference.
static CoralUIDelegate *coralUIDelegate;

static WKWebView *coralFindWebView(NSView *v) {
	if ([v isKindOfClass:[WKWebView class]]) return (WKWebView *)v;
	for (NSView *s in v.subviews) {
		WKWebView *found = coralFindWebView(s);
		if (found) return found;
	}
	return nil;
}

// coralInstallFilePanel gives the web view of the window a file chooser that stays. 1: installed; 0: the window has no web view.
static int coralInstallFilePanel(void *win) {
	NSWindow *window = (__bridge NSWindow *)win;
	WKWebView *webView = coralFindWebView(window.contentView);
	if (!webView) return 0;
	if (!coralUIDelegate) coralUIDelegate = [[CoralUIDelegate alloc] init];
	webView.UIDelegate = coralUIDelegate;
	return 1;
}

// coralFilePanelState reads what the web view of the window has as its delegate: -1 no web view, 0 none, 1 one that answers the file chooser.
static int coralFilePanelState(void *win) {
	NSWindow *window = (__bridge NSWindow *)win;
	WKWebView *webView = coralFindWebView(window.contentView);
	if (!webView) return -1;
	id d = webView.UIDelegate;
	return d && [d respondsToSelector:@selector(webView:runOpenPanelWithParameters:initiatedByFrame:completionHandler:)] ? 1 : 0;
}

// coralSpinRunLoop turns the main run loop for a moment, so what is autoreleased on this thread is released (for the test).
static void coralSpinRunLoop(double seconds) {
	[[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:seconds]];
}
*/
import "C"

import (
	"log"
	"unsafe"
)

// installFilePanel makes <input type="file"> (Import Template, Import from Folder) open the system's file chooser in the app window. It must run
// on the main thread, after the window exists.
func installFilePanel(win unsafe.Pointer) {
	if C.coralInstallFilePanel(win) == 0 {
		log.Println("[WEBVIEW] no web view found in the window: file inputs will not open a chooser")
		return
	}
	log.Println("[WEBVIEW] file chooser installed")
}

// filePanelState is what the window's web view has as its UI delegate (see coralFilePanelState); for the test.
func filePanelState(win unsafe.Pointer) int { return int(C.coralFilePanelState(win)) }

// spinRunLoop lets the main thread's autorelease pools drain, as the app's run loop does between events (for the test).
func spinRunLoop(seconds float64) { C.coralSpinRunLoop(C.double(seconds)) }
