//go:build webview && darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>

// cocoaShowEULA displays a modal EULA acceptance dialog with a scrollable
// text view. Returns 1 if the user clicks Accept, 0 if they click Decline.
int cocoaShowEULA(const char *title, const char *info, const char *accept, const char *decline, const char *name, const char *tosText) {
	// Run directly — this is called from main() which is already on the main
	// thread (goroutine 1 locked to OS thread 0 via init()/runtime.LockOSThread()).
	// Using dispatch_sync to the main queue from the main thread would DEADLOCK.
	NSString *text = [NSString stringWithUTF8String:tosText];

	NSAlert *alert = [[NSAlert alloc] init];
	alert.messageText = [NSString stringWithUTF8String:title];
	alert.informativeText = [NSString stringWithUTF8String:info];
	alert.alertStyle = NSAlertStyleInformational;
	NSButton *acceptButton = [alert addButtonWithTitle:[NSString stringWithUTF8String:accept]];
	NSButton *declineButton = [alert addButtonWithTitle:[NSString stringWithUTF8String:decline]];
	// Pressing Return must not accept terms nobody has read: Decline is the default button (the terminal prompt treats Enter as a refusal too),
	// and Accept is a deliberate click, or Space on it after Tab.
	acceptButton.keyEquivalent = @"";
	declineButton.keyEquivalent = @"\r";

	// Scrollable text view for the TOS content
	NSScrollView *scrollView = [[NSScrollView alloc] initWithFrame:NSMakeRect(0, 0, 500, 300)];
	scrollView.hasVerticalScroller = YES;
	scrollView.borderType = NSBezelBorder;

	NSTextView *textView = [[NSTextView alloc] initWithFrame:NSMakeRect(0, 0, 480, 300)];
	textView.editable = NO;
	textView.selectable = YES;
	textView.font = [NSFont systemFontOfSize:[NSFont systemFontSize]];
	textView.textColor = [NSColor labelColor];
	textView.backgroundColor = [NSColor textBackgroundColor];
	textView.accessibilityLabel = [NSString stringWithUTF8String:name];
	[textView setString:text];
	textView.autoresizingMask = NSViewWidthSizable;

	scrollView.documentView = textView;
	alert.accessoryView = scrollView;

	[alert layout];
	// The terms have keyboard focus when the dialog opens, so the arrow keys and Page Down read them; Tab then reaches the buttons.
	alert.window.initialFirstResponder = textView;

	NSModalResponse response = [alert runModal];
	return (response == NSAlertFirstButtonReturn) ? 1 : 0;
}
*/
import "C"

import (
	"unsafe"

	"github.com/cdknorow/coral/internal/eulatext"
)

// showEULADialog shows a native Cocoa dialog with the TOS text.
// Returns true if the user accepts, false if they decline.
func showEULADialog(tosText string) bool {
	cTitle, cInfo := C.CString(eulatext.DialogTitle), C.CString(eulatext.DialogIntro)
	cAccept, cDecline, cName := C.CString(eulatext.AcceptLabel), C.CString(eulatext.DeclineLabel), C.CString(eulatext.AccessibleName)
	cText := C.CString(tosText)
	defer func() {
		for _, p := range []*C.char{cTitle, cInfo, cAccept, cDecline, cName, cText} {
			C.free(unsafe.Pointer(p))
		}
	}()
	return C.cocoaShowEULA(cTitle, cInfo, cAccept, cDecline, cName, cText) == 1
}
