/* Artifacts tab (#6236), on the Coral Hub only. Where GET /api/account says the account has the tab (artifacts_tab === true, a strict boolean, read once at load) it is the hub's /artifacts page inside the
 * dashboard, in a frame whose address is the constant below: never built from a URL, a hash or an artifact, nothing is posted to or read from the frame, and its opens and downloads leave the frame on the
 * hub's side. It is made exactly as the Inbox's frame is (inbox_tab.js): the same attributes, and the same same-origin CSP frame-src. Where the answer is false, absent, an error or not a boolean there is no tab
 * and no frame. */

const ARTIFACTS_URL = '/artifacts';

/** Read GET /api/account once and show the tab only on artifacts_tab === true. Never throws: no answer is no tab. */
export async function initArtifactsTab() {
    const tab = document.getElementById('nav-tab-artifacts');
    if (!tab) return false;
    let on = false;
    try {
        const r = await fetch('/api/account', { headers: { Accept: 'application/json' } });
        if (r.ok) { const d = await r.json(); on = !!d && d.artifacts_tab === true; }
    } catch { /* no answer: no tab */ }
    if (tab.hidden === on) tab.hidden = !on; // nav_tabs.js redraws the row when the button changes
    return on;
}

/** Called by switchNavTab('artifacts'): the page is loaded the first time the tab is shown and kept after. */
export function showArtifactsTabContent() {
    const view = document.getElementById('artifacts-view');
    if (!view || document.getElementById('artifacts-frame')) return;
    // Made here, not in the page: a hub visit that never opens the tab carries no empty frame.
    const frame = document.createElement('iframe');
    frame.id = 'artifacts-frame';
    frame.className = 'inbox-frame';
    frame.title = 'Artifacts';
    frame.setAttribute('referrerpolicy', 'no-referrer');
    frame.setAttribute('allow', ''); // no camera, microphone, geolocation or other powerful features
    frame.setAttribute('src', ARTIFACTS_URL);
    view.append(frame);
}
