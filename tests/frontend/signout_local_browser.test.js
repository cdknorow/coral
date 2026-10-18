// Settings > Browser sign-in > Sign out (#4164): beside "Sign out other browsers", it signs this browser out. The stored content
// (drafts, pending messages, recent folders, stars, the agent panels' unsent requests) goes, the session is ended with
// POST /auth/logout (the real server's answer is recorded), display preferences stay, and the sign-in page shows. It is a plain
// button: reachable and pressed from the keyboard (Enter), and a touch-size target on a phone.
//
// Drives Chrome over CDP through agent_view_harness.js against an isolated dev server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;

    // The Security row as for a signed-in browser (the list API is stubbed); /auth/logout goes to the real server and is recorded.
    const SCENE = `(async () => {
        const S = window.__coralStatic || '/static/';
        const RealFetch = window.fetch;
        window.__logout = null;
        window.fetch = async (u, o = {}) => {
            const url = String(u);
            if (url.startsWith('/api/system/auth-status')) return new Response(JSON.stringify({ authenticated: true, method: 'session', password_set: true, local: true }), { status: 200 });
            if (url.startsWith('/api/auth/sessions') && !o.method) return new Response(JSON.stringify({ sessions: [
                { id: 'aaaaaaaaaaaa', label: 'Chrome on macOS', method: 'password', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 86400000).toISOString(), persistent: true, current: true },
                { id: 'bbbbbbbbbbbb', label: 'Phone', method: 'password', last_seen: new Date().toISOString(), expires_at: new Date(Date.now() + 86400000).toISOString(), persistent: true, current: false }], recent: [] }), { status: 200 });
            if (url === '/auth/logout') {
                const r = await RealFetch(u, o);
                sessionStorage.setItem('zz-logout', JSON.stringify({ method: o.method, status: r.status, storedAtCall: Object.keys(localStorage).filter(k => /^coral-(input-drafts|pending-messages|recent-dirs|starred-files-)/.test(k)).length + Object.keys(sessionStorage).filter(k => k.startsWith('coral-panel-request:')).length }));
                return r;
            }
            return RealFetch(u, o);
        };
        localStorage.setItem('coral-input-drafts', '{"a":"unsent text"}');
        localStorage.setItem('coral-pending-messages', '[{"text":"pending"}]');
        localStorage.setItem('coral-recent-dirs', '["/home/x/project"]');
        localStorage.setItem('coral-starred-files-s1', '["a.txt"]');
        localStorage.setItem('coral-group-by-server', 'true');
        sessionStorage.setItem('coral-panel-request:agent-1', JSON.stringify({ draft: 'a panel request typed but not sent' }));
        sessionStorage.setItem('some-other-key', 'kept');
        const sec = await import(S + 'security_settings.js');
        await window.showSettingsModal('security');
        await sec.loadSecurityRow();
        return true;
    })()`;
    const navigations = [];

    try {
        await Page.enable();
        client.Page.frameNavigated(e => { if (!e.frame.parentId) navigations.push(new URL(e.frame.url).pathname); });
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await require('./helpers/wait').bind(ev).coralReady('startup', 'settings');

        console.log('1. The button sits beside "Sign out other browsers", a real button');
        await ev(SCENE);
        await waitFor(`!!document.getElementById('sec-signout')`, 'the Sign out button', 4000);
        const lay = await ev(`(() => { const b = document.getElementById('sec-signout'), o = document.getElementById('sec-signout-others');
            const rb = b.getBoundingClientRect(), ro = o.getBoundingClientRect();
            return { tag: b.tagName, type: b.type, text: b.textContent, sameParent: b.parentElement === o.parentElement, sameRow: rb.height > 0 && Math.abs(rb.top - ro.top) < 2, h: Math.round(rb.height), others: o.textContent }; })()`);
        assert.deepEqual([lay.tag, lay.type, lay.text, lay.sameParent, lay.sameRow], ['BUTTON', 'button', 'Sign out', true, true], JSON.stringify(lay));
        console.log('   ✓ <button type=button>Sign out</button> next to', JSON.stringify(lay.others));

        console.log('2. Phone: a touch-size target, no horizontal overflow');
        await Emulation.setDeviceMetricsOverride({ width: 375, height: 812, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        // The button eases to its touch size (a CSS transition): wait for it, don't sample mid-way.
        await waitFor(`document.getElementById('sec-signout').getBoundingClientRect().height >= 43.5`, 'the touch size', 3000);
        const ph = await ev(`(() => { const b = document.getElementById('sec-signout'); const r = b.getBoundingClientRect(); const probe = document.createElement('div'); probe.style.cssText = 'position:absolute;visibility:hidden;height:var(--control-touch-height, 44px)'; document.body.appendChild(probe); const touch = probe.getBoundingClientRect().height; probe.remove(); return { touch, h: Math.round(r.height), w: Math.round(r.width), overflow: document.documentElement.scrollWidth - innerWidth }; })()`);
        assert.ok(ph.h >= ph.touch - 0.5 && ph.w >= 44, `phone target (${JSON.stringify(ph)})`);
        assert.equal(ph.overflow, 0, 'no horizontal overflow');
        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.clearDeviceMetricsOverride();
        console.log(`   ✓ ${ph.w} x ${ph.h} px on a 375 px phone (touch size ${ph.touch})`);

        console.log('3. Enter on the focused button signs out: content cleared first, logout POSTed, sign-in page shown, display prefs kept');
        await ev(`document.getElementById('sec-signout').focus(); true`);
        assert.equal(await ev(`document.activeElement.id`), 'sec-signout', 'the button takes keyboard focus');
        const before = navigations.length;
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        const until = Date.now() + 8000;
        while (!navigations.slice(before).includes('/signin') && Date.now() < until) await new Promise(r => setTimeout(r, 20));
        assert.ok(navigations.slice(before).includes('/signin'), `the sign-in page shows (${JSON.stringify(navigations.slice(before))})`);
        // After the navigation this is the sign-in page of the same origin: its storage is the one we seeded.
        const after = await ev(`({ stored: Object.keys(localStorage).filter(k => /^coral-(input-drafts|pending-messages|recent-dirs|starred-files-)/.test(k)),
            panel: Object.keys(sessionStorage).filter(k => k.startsWith('coral-panel-request:')), kept: localStorage.getItem('coral-group-by-server'), other: sessionStorage.getItem('some-other-key'), logout: JSON.parse(sessionStorage.getItem('zz-logout') || 'null'), path: location.pathname })`);
        assert.deepEqual(after.stored, [], 'drafts, pending messages, recent folders and stars are gone');
        assert.deepEqual(after.panel, [], 'the panels\' unsent requests are gone');
        assert.equal(after.kept, 'true', 'a display preference stays (the tab layout)');
        assert.equal(after.other, 'kept', 'nothing else in the session storage is touched');
        assert.deepEqual(after.logout, { method: 'POST', status: 200, storedAtCall: 0 }, 'POST /auth/logout, answered ok, after the content was cleared');
        assert.equal(after.path, '/signin');
        console.log('   ✓ cleared, /auth/logout POSTed, /signin shown, display preference kept');

        console.log('\nAll Sign out checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
