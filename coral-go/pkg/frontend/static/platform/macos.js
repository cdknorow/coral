/* macOS WKWebView workarounds.
   WKWebView has layout timing quirks that don't affect Chrome/Safari. */

import { platform } from './detect.js';

export function initMacOS() {
    if (!platform.isMacOS || !platform.isNative) return;
    // WKWebView-specific init applied here as needed.
}
