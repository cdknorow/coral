// The running Coral's version at the bottom of the top-bar gear menu (#4589 follow-up): a small muted line, not a menu item, taken from the
// server's own status; no line when the status has no version. Drives Chrome over CDP through agent_view_harness.js against an isolated dev
// server; run_cdp_suites.mjs finds it by name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const load = async stub => {
        await Page.navigate({ url: 'about:blank' });
        await Page.addScriptToEvaluateOnNewDocument({ source: `window.__verStub = ${JSON.stringify(stub)}; (() => { const inner = window.fetch; window.fetch = (url, opts) => { const u = String(url && url.url || url);
            if (window.__verStub !== null && u.includes('/api/system/status')) return inner(url, opts).then(async r => { const d = await r.clone().json(); const o = { ...d }; if (window.__verStub === '') delete o.version; else if (window.__verStub) o.version = window.__verStub; return new Response(JSON.stringify(o), { status: 200, headers: { 'content-type': 'application/json' } }); });
            return inner(url, opts); }; })();` });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await require('./helpers/wait').bind(ev).coralReady('startup');
        await ev(`window.__coral.toggleTopBarSettings(); true`);
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });

        console.log('1. A version from the server: shown at the very bottom, below Check for Updates');
        await load('1.4.88');
        await waitFor(`!document.getElementById('coral-version-row').hidden`, 'the version line');
        const shown = await ev(`document.getElementById('coral-version').textContent`);
        assert.equal(shown, 'Coral v1.4.88');
        const order = await ev(`(() => { const m = document.getElementById('top-bar-settings-menu'); const kids = [...m.children].filter(e => e.getClientRects().length); const last = kids[kids.length - 1]; const upd = [...m.querySelectorAll('button')].find(b => /Check for Updates/.test(b.textContent)); return { lastIsVersion: last.id === 'coral-version-row', below: document.getElementById('coral-version').getBoundingClientRect().top >= upd.getBoundingClientRect().bottom }; })()`);
        assert.deepEqual(order, { lastIsVersion: true, below: true }, 'last in the menu, below Check for Updates');
        console.log('   ✓ ' + shown);

        console.log('1b. The server\'s own answer, unchanged: the line is its version, or absent when it reports none (a dev build)');
        await load(null);
        const real = await ev(`fetch('/api/system/status').then(r => r.json()).then(d => d.version || '')`);
        await ev(`new Promise(r => setTimeout(r, 600))`); // sleep-ok: the line either appears or stays out; there is no event for a status answer
        const row = await ev(`({ hidden: document.getElementById('coral-version-row').hidden, text: document.getElementById('coral-version').textContent })`);
        if (real) assert.equal(row.text, /^\d/.test(String(real).replace(/^v/, '')) ? `Coral v${String(real).replace(/^v/, '')}` : `Coral ${real}`, 'the real version');
        else assert.equal(row.hidden, true, 'no version from the server, no line');
        console.log(`   ✓ server version ${JSON.stringify(real)}: ${row.hidden ? 'no line' : row.text}`);
        await load('1.4.88');
        await waitFor(`!document.getElementById('coral-version-row').hidden`, 'the version line again');

        console.log('2. Not a menu item: no role, no tabstop, Tab skips it; it reads as text');
        const item = await ev(`(() => { const e = document.getElementById('coral-version'); const m = document.getElementById('top-bar-settings-menu');
            const focusables = [...m.querySelectorAll('a[href], button, [tabindex]:not([tabindex="-1"])')].filter(x => x.getClientRects().length);
            return { tag: e.tagName, role: e.getAttribute('role'), tabindex: e.getAttribute('tabindex'), inside: !!e.closest('button, a, [role=menuitem]'), focusableCount: focusables.filter(x => x.contains(e) || e.contains(x)).length, text: e.textContent.length > 0, hidden: e.closest('[hidden], [aria-hidden=true]') !== null }; })()`);
        assert.deepEqual(item, { tag: 'DIV', role: null, tabindex: null, inside: false, focusableCount: 0, text: true, hidden: false }, JSON.stringify(item));
        console.log('   ✓ plain text');

        console.log('3. Muted, and still 4.5:1 on the menu');
        const rgb = c => c.match(/[\d.]+/g).slice(0, 3).map(Number);
        const lum = ([r, g, b]) => { const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
        for (const theme of ['dark', 'light']) {
            await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${theme}'); return true; })()`);
            await waitFor(`getComputedStyle(document.getElementById('coral-version')).color !== ''`, 'a colour');
            const c = await ev(`(() => { const e = document.getElementById('coral-version'); const m = document.getElementById('top-bar-settings-menu'); return { fg: getComputedStyle(e).color, bg: getComputedStyle(m).backgroundColor, size: parseFloat(getComputedStyle(e).fontSize) }; })()`);
            const [a, b] = [lum(rgb(c.fg)), lum(rgb(c.bg))]; const ratio = (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
            assert.ok(ratio >= 4.5, `${theme}: contrast ${ratio.toFixed(2)} for ${c.fg} on ${c.bg}`);
            assert.ok(c.size <= 12, `${theme}: small (${c.size} px)`);
        }
        console.log('   ✓ both themes');

        console.log('4. Names and gaps: a v-prefixed version, a build name, and no version at all');
        for (const [stub, want] of [['v9.8.7', 'Coral v9.8.7'], ['9.8.7', 'Coral v9.8.7'], ['dev', 'Coral dev']]) {
            await load(stub);
            await waitFor(`!document.getElementById('coral-version-row').hidden`, `the line for ${stub}`);
            assert.equal(await ev(`document.getElementById('coral-version').textContent`), want, stub);
        }
        await load('');
        await ev(`new Promise(r => setTimeout(r, 600))`); // sleep-ok: nothing to wait for; the line must stay out
        assert.equal(await ev(`document.getElementById('coral-version-row').hidden`), true, 'no version in the status: no line, no divider');
        console.log('   ✓ formats and the empty case');

        console.log('\nAll version line checks passed');
    } finally { await client.close(); }
})().catch(err => { console.error(err); process.exit(1); });
