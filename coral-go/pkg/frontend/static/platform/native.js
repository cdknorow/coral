/* Shared native app initialization — health check, link intercept, body classes.
   Extracted from coral-app/main.go injected JS. */

import { platform } from './detect.js';

export function initNative() {
    if (!platform.isNative) return;

    // Apply classes on both <html> and <body> for CSS selector compatibility.
    // w.Init() applies them on <html> synchronously (before CSS evaluation),
    // we also add to <body> as a safety net. classList.add is idempotent.
    for (const el of [document.documentElement, document.body]) {
        el.classList.add('native-app');
        if (platform.isMacOS)   el.classList.add('native-macos');
        if (platform.isWindows) el.classList.add('native-windows');
        if (platform.isLinux)   el.classList.add('native-linux');
    }

    initHealthCheck();
    initLinkInterceptor();
}

// Server health check — detect when the backend crashes.
// Polls /api/health every 5s, shows disconnect overlay on failure.
function initHealthCheck() {
    let wasConnected = false;
    let overlayEl = null;

    function getOverlay() {
        if (overlayEl) return overlayEl;
        overlayEl = document.getElementById('coral-app-disconnect-overlay');
        if (overlayEl) return overlayEl;

        const el = document.createElement('div');
        el.id = 'coral-app-disconnect-overlay';
        el.style.cssText = 'display:none;position:fixed;inset:0;z-index:99999;background:rgba(0,0,0,0.75);align-items:center;justify-content:center;flex-direction:column;font-family:-apple-system,BlinkMacSystemFont,sans-serif;color:#e0e0e0;';
        el.innerHTML = '<div style="text-align:center;max-width:400px;padding:32px">'
            + '<div style="font-size:48px;margin-bottom:16px">\u26a0\ufe0f</div>'
            + '<h2 style="margin:0 0 8px;font-size:20px;color:#fff">Server Disconnected</h2>'
            + '<p style="margin:0 0 16px;font-size:14px;color:#aaa">Coral may have crashed. Check <code style="background:rgba(255,255,255,0.1);padding:2px 6px;border-radius:3px;font-size:12px">~/.coral/coral.log</code> for details.</p>'
            + '<div style="display:flex;align-items:center;gap:8px;justify-content:center">'
            + '<span class="coral-reconnect-spinner" style="display:inline-block;width:16px;height:16px;border:2px solid rgba(255,255,255,0.2);border-top-color:#fff;border-radius:50%;animation:coral-reconnect-spin 0.8s linear infinite"></span>'
            + '<span style="font-size:13px;color:#888">Reconnecting...</span>'
            + '</div></div>';

        const style = document.createElement('style');
        style.textContent = '@keyframes coral-reconnect-spin { to { transform: rotate(360deg); } }';
        document.head.appendChild(style);
        document.body.appendChild(el);
        overlayEl = el;
        return el;
    }

    function checkHealth() {
        fetch('/api/health', { method: 'GET', cache: 'no-store' })
            .then(function(r) {
                if (r.ok) {
                    if (!wasConnected) wasConnected = true;
                    const ov = getOverlay();
                    if (ov.style.display !== 'none') {
                        ov.style.display = 'none';
                        location.reload();
                    }
                } else {
                    showDisconnect();
                }
            })
            .catch(function() {
                showDisconnect();
            });
    }

    function showDisconnect() {
        if (!wasConnected) return;
        const ov = getOverlay();
        ov.style.display = 'flex';
    }

    // Start polling — wasConnected starts true at DOMContentLoaded
    wasConnected = true;
    setInterval(checkHealth, 5000);
}

// Link interceptor — open external links in system browser instead of webview.
function initLinkInterceptor() {
    document.addEventListener('click', function(e) {
        const a = e.target.closest('a');
        if (!a) return;
        const href = a.getAttribute('href');
        if (!href) return;
        // Single-agent popout links: prefer a second in-app window when the
        // native bridge supports internal routes; otherwise fall back to the
        // same-origin URL in the system browser.
        const isAgentPopout = a.target === '_blank'
            && (/^\/agent\/[0-9a-f-]{36}$/i.test(href) || href.startsWith(location.origin + '/agent/'));
        if (isAgentPopout) {
            e.preventDefault();
            const abs = new URL(href, location.origin).href;
            if (window._coralOpenInternalWindow) {
                window._coralOpenInternalWindow(abs);
            } else if (window._coralOpenExternal) {
                window._coralOpenExternal(abs);
            } else {
                window.open(abs, '_blank', 'noopener,noreferrer');
            }
            return;
        }
        const isExternal = href.startsWith('http') && !href.startsWith(location.origin);
        if (isExternal || a.target === '_blank') {
            e.preventDefault();
            if (window._coralOpenExternal) {
                window._coralOpenExternal(href);
            } else {
                window.open(href, '_blank');
            }
        }
    }, true);
}
