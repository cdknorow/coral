/* Platform detection — single source of truth for runtime environment.
   Replaces scattered __CORAL_APP__ checks and navigator.userAgent sniffing.

   NOTE: platform.isNative is a UX hint, not a security boundary.
   Auth decisions must use server-side checks (auth.IsLocalhost middleware),
   not client-side platform flags. */

export const platform = {
    isNative:  false,
    get isBrowser() { return !this.isNative; },
    isMacOS:   false,
    isWindows: false,
    isLinux:   false,

    // Webview engine: 'browser' | 'wkwebview' | 'webview2'
    engine: 'browser',

    // Capability flags — what the runtime supports reliably.
    // Modules check capabilities, not platform names. This way a fix for
    // WKWebView automatically applies if WebView2 has the same issue.
    capabilities: {
        resizeObserver:   true,   // false in WKWebView (some versions)
        inputEvents:      true,   // false in WKWebView (use keyup fallback)
        layoutOnDisplay:  true,   // false in WKWebView (needs rAF reflow)
        externalLinks:    true,   // false in all native (intercept -> system browser)
        nativeTitlebar:   false,  // true in all native (drag regions, traffic lights)
    },

    init() {
        this.isNative  = !!window.__CORAL_APP__;
        this.isMacOS   = navigator.platform?.includes('Mac');
        this.isWindows = navigator.platform?.includes('Win');
        this.isLinux   = navigator.platform?.includes('Linux');

        if (this.isNative) {
            this.capabilities.externalLinks  = false;
            this.capabilities.nativeTitlebar = true;

            if (this.isMacOS) {
                this.engine = 'wkwebview';
                this.capabilities.inputEvents    = false;
                this.capabilities.layoutOnDisplay = false;
                this.capabilities.resizeObserver  = false;
            }
            if (this.isWindows) {
                this.engine = 'webview2';
                // WebView2 is Chromium-based, so most things work.
                // Add capability flags here as issues are discovered.
            }
        }
    }
};
