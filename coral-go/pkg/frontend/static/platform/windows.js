/* Windows WebView2 workarounds.
   WebView2 is Chromium-based so fewer quirks expected, but Windows has
   its own titlebar conventions, font rendering, and scroll behavior. */

import { platform } from './detect.js';

export function initWindows() {
    if (!platform.isWindows || !platform.isNative) return;
    // WebView2-specific fixes applied here as discovered.
}
