/* Browser-specific initialization.
   Browser is the baseline — most features work without workarounds. */

import { platform } from './detect.js';

export function initBrowser() {
    if (!platform.isBrowser) return;
    // PWA service worker registration, etc.
}
