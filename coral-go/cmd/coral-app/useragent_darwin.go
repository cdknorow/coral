//go:build webview && darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#include <stdlib.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static WKWebView *coralUAFindWebView(NSView *v) {
	if ([v isKindOfClass:[WKWebView class]]) return (WKWebView *)v;
	for (NSView *s in v.subviews) {
		WKWebView *found = coralUAFindWebView(s);
		if (found) return found;
	}
	return nil;
}

// coralMarkUserAgent adds marker to the web view's User-Agent: its own (what it sends now) plus " marker". 1: set; 0: the window has no web view,
// or WebKit would not say its User-Agent.
static int coralMarkUserAgent(void *win, const char *marker) {
	NSWindow *window = (__bridge NSWindow *)win;
	WKWebView *webView = coralUAFindWebView(window.contentView);
	if (!webView) return 0;
	NSString *current = webView.customUserAgent;
	if (current.length == 0) {
		id ua = [webView valueForKey:@"userAgent"];
		if (![ua isKindOfClass:[NSString class]] || [(NSString *)ua length] == 0) return 0;
		current = (NSString *)ua;
	}
	webView.customUserAgent = [NSString stringWithFormat:@"%@ %s", current, marker];
	return 1;
}

// coralUserAgent is the web view's custom User-Agent now ("" when none), for the test.
static const char *coralUserAgent(void *win) {
	NSWindow *window = (__bridge NSWindow *)win;
	WKWebView *webView = coralUAFindWebView(window.contentView);
	if (!webView || webView.customUserAgent.length == 0) return strdup(""); // always malloc'd: the Go side frees it
	return strdup(webView.customUserAgent.UTF8String);
}
*/
import "C"

import (
	"log"
	"unsafe"

	"github.com/subgentic/coral/internal/appmarker"
	"github.com/subgentic/coral/internal/config"
)

// markAppUserAgentBeforeWindow: on macOS the web view is marked after the window exists (markAppUserAgent).
func markAppUserAgentBeforeWindow() bool { return false }

// markAppUserAgent adds the app's marker to the web view's User-Agent, so the Sessions page can name the app's sign-ins "Coral app". It must run
// on the main thread, after the window exists and before the first page loads. It returns whether it set it.
func markAppUserAgent(win unsafe.Pointer) bool {
	marker := C.CString(appmarker.Marker(config.ReportedVersion()))
	defer C.free(unsafe.Pointer(marker))
	if C.coralMarkUserAgent(win, marker) == 0 {
		log.Println("[WEBVIEW] the web view's User-Agent is unknown: sessions made in this window are not marked as the app's")
		return false
	}
	return true
}

// customUserAgent is the web view's custom User-Agent ("" when none); for the test.
func customUserAgent(win unsafe.Pointer) string {
	p := C.coralUserAgent(win)
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}
