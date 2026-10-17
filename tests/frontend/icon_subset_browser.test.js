// Every icon the running dashboard shows renders as a glyph, not as its name (#3973). The source scan (icon_subset.test.mjs) cannot see a
// name built at run time ('arrow_' + direction); this checks the rendered page: the dashboard, the Settings dialog and the launch dialog
// are walked and each visible Material Icons element's TEXT must be about one em wide (a missing ligature renders as its name, several em wide; the element's own box can be larger).
// It also checks the discriminator itself: every name in the subset renders as a glyph and a name left out does not.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const NAMES = fs.readFileSync(path.join(__dirname, '../../coral-go/pkg/frontend/static/vendor/fonts/material-icons.names.txt'), 'utf8').split('\n').filter(Boolean);
(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async x => { const r = await Runtime.evaluate({ expression: x, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300)); return r.result.value; };
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const until = async (expr, what) => { for (let i = 0; i < 250; i++) { if (await ev(expr)) return; await sleep(20); } throw new Error('timed out waiting for ' + what); };
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await ev(`new Promise(r => { const w = () => typeof window.selectLiveSession === 'function' ? r() : setTimeout(w, 50); w(); })`);
        await ev(`document.fonts.load('24px "Material Icons"').then(() => true)`);
        // 1. the discriminator: each subset name is one em wide, a name that is not in it is not
        const probe = await ev(`(() => {
            const names = ${JSON.stringify(NAMES)}.concat(['flight_takeoff']);
            const box = document.createElement('div'); box.style.cssText = 'position:fixed;left:0;top:0;visibility:hidden';
            const out = {};
            for (const n of names) { const s = document.createElement('span'); s.className = 'material-icons'; s.style.fontSize = '24px'; s.textContent = n; box.appendChild(s); out[n] = s; }
            document.body.appendChild(box);
            const w = Object.fromEntries(Object.entries(out).map(([n, s]) => [n, s.getBoundingClientRect().width]));
            box.remove(); return w; })()`);
        const wide = Object.entries(probe).filter(([n, w]) => n !== 'flight_takeoff' && w > 36).map(([n, w]) => `${n}=${Math.round(w)}`);
        assert.deepEqual(wide, [], 'every icon in the subset renders as one glyph (24 px), not as text: ' + wide.join(', '));
        assert.ok(probe.flight_takeoff > 60, `a name left out of the subset renders as text (${probe.flight_takeoff} px), so this check can tell: the font is really the subset`);
        // 2. the rendered dashboard: every visible icon element is a glyph
        const walk = async label => ev(`(() => { const textWidth = e => { const r = document.createRange(); r.selectNodeContents(e); return r.getBoundingClientRect().width; }; return [...document.querySelectorAll('.material-icons')].filter(e => e.getClientRects().length && e.textContent.trim()).filter(e => { const fs = parseFloat(getComputedStyle(e).fontSize) || 24; return textWidth(e) > fs * 1.6; }).map(e => ${JSON.stringify(label)} + ': ' + e.textContent.trim() + ' (' + Math.round(textWidth(e)) + ' px)'); })()`);
        const bad = [...await walk('dashboard')];
        const iconsIn = async sel => ev(`document.querySelectorAll(${JSON.stringify(sel + ' .material-icons')}).length`);
        for (const [open, id] of [[`window.showSettingsModal()`, 'settings-modal'], [`window.showLaunchModal()`, 'launch-modal']]) {
            await ev(`(async () => { ${open}; return true; })()`);
            await until(`!!document.getElementById('${id}') && getComputedStyle(document.getElementById('${id}')).display !== 'none'`, `${id} to open`);
            assert.ok(await iconsIn('#' + id) > 0, `${id} has icons to check`);
            bad.push(...await walk(id));
            await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
            await until(`getComputedStyle(document.getElementById('${id}')).display === 'none'`, `${id} to close`);
        }
        const count = await ev(`document.querySelectorAll('.material-icons').length`);
        assert.deepEqual(bad, [], 'icons that render as text instead of a glyph: ' + bad.join(', '));
        assert.ok(count > 10, `the page has icons to check (${count})`);
        console.log(`PASS icon subset renders: ${NAMES.length} icons are glyphs, ${count} icon elements on the dashboard all render as glyphs; a missing name shows as text`);
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
