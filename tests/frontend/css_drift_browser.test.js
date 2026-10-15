// CSS drift guard (#3626, QA's gap 3): record a small, stable set of computed styles for a fixed
// list of representative dashboard elements, in both themes, and compare to a checked-in baseline
// (css_drift_baseline.json). A change that accidentally alters a component's styles -- the way
// #3607's sed refactor silently changed .machine-health-* font-size -- fails this, naming the
// element/theme/property/expected/actual. The machine-health card is rendered from the real
// healthCard() export, so this guard would have caught #3607.
//
// Update the baseline after an INTENDED style change, then REVIEW the JSON diff: it is written
// from the tree under test, so an unintended change there would be baked in. Canonical runner
// (its own isolated server + Chrome; the env var reaches the suite):
//   CSS_DRIFT_UPDATE=1 node run_cdp_suites.mjs --only css_drift_browser --shards 1
// or via run.sh:
//   CSS_DRIFT_UPDATE=1 CORAL_TEST_PORT=8488 CDP_PORT=9248 ONLY=css_drift_browser.test.js ./run.sh
'use strict';

const CDP = require('chrome-remote-interface');
const fs = require('node:fs');
const path = require('node:path');
const BASE = require('./test_base').BASE;

const BASELINE = path.join(__dirname, 'css_drift_baseline.json');
const PROPS = ['fontSize', 'fontWeight', 'color', 'backgroundColor',
    'paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft',
    'marginTop', 'marginRight', 'marginBottom', 'marginLeft'];
const THEMES = ['dark', 'light'];

// Two computed values match if equal, or both pixel lengths within 0.5px. A layout-derived value
// (an auto margin that centres content) differs by a sub-pixel between the snapshot machine and
// epoch's headless Linux Chrome, which isn't a real style change; a real token change -- font-size,
// padding, a margin token -- is at least 1px, so 0.5px tolerates the noise without masking drift
// (#3626, found by the epoch run: nav-tabs marginRight 863.5px vs 863.422px).
const PX_RE = /^(-?\d+(?:\.\d+)?)px$/;
function sameComputed(a, b) {
    if (String(a) === String(b)) return true;
    const ma = PX_RE.exec(String(a)), mb = PX_RE.exec(String(b));
    return !!(ma && mb && Math.abs(parseFloat(ma[1]) - parseFloat(mb[1])) < 0.5);
}

// Each element: a stable selector reached in one of a few ways. 'root' = the agents page; 'board' =
// /board; 'settings' = the Settings modal open; 'health-ok'/'health-unknown' = a card from the real
// healthCard() export (the #3607 element); 'make' = a representative node built from the class, for a
// component whose live instance needs an agent/session (its styling is class-based).
const ELEMENTS = [
    { name: 'nav-tabs',            where: 'root',           selector: '.top-nav-tabs' },
    { name: 'nav-tab',             where: 'root',           selector: '#nav-tab-agents' },
    { name: 'top-bar-btn',         where: 'root',           selector: '#panel-toggle-btn' },
    { name: 'machine-health-title',  where: 'health-ok',      selector: '.machine-health-title' },
    { name: 'machine-health-status', where: 'health-ok',      selector: '.machine-health-status' },
    { name: 'machine-health-grid',   where: 'health-ok',      selector: '.machine-health-grid' },
    { name: 'machine-health-note',   where: 'health-unknown', selector: '.machine-health-note' },
    { name: 'messageboard-view',   where: 'board',          selector: '#messageboard-view' },
    { name: 'mb-view-header',      where: 'board',          selector: '.mb-view-header' },
    { name: 'mb-action-btn',       where: 'board',          selector: '#mb-select-btn' },
    { name: 'settings-title',      where: 'settings',       selector: '#settings-title' },
    { name: 'chat-prose',          where: 'make',           selector: '#__cssdrift_chatprose',
      html: `<div id="__cssdrift_chatprose" class="chat-prose chat-messages"><p>Representative chat text for the drift guard.</p></div>` },
];
const WHERES = [...new Set(ELEMENTS.map(e => e.where))];

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const settle = () => ev(`new Promise(r => setTimeout(() => r(true), 250))`); // sleep-ok: style settles after a theme change
    const applyTheme = t => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars(); m.applyTheme('${t}'); return true; })()`);
    const removeSandbox = () => ev(`(() => { const s = document.getElementById('__cssdrift'); if (s) s.remove(); return true; })()`);

    // Build the per-'where' DOM state on the current page, then return. Each returns after the needed
    // element exists, so the capture is deterministic.
    const goRoot = async () => {
        const l = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await l; await coralReady();
        await waitFor(`!!document.querySelector('.top-nav-tabs') && !!document.getElementById('nav-tab-agents')`, 'the top nav', { timeout: 8000 });
    };
    const makeSandbox = () => ev(`(() => {
        document.getElementById('__cssdrift')?.remove();
        const box = document.createElement('div'); box.id = '__cssdrift';
        document.body.appendChild(box);
        return true; })()`);
    const renderHealth = ok => ev(`(async () => {
        const m = await import((window.__coralStatic || '/static/') + 'machine_health.js');
        let box = document.getElementById('__cssdrift'); if (!box) { box = document.createElement('div'); box.id = '__cssdrift'; document.body.appendChild(box); }
        box.replaceChildren(m.healthCard(${ok ? `{ status: 'ok', coral: {}, agents: {}, host: {}, disk: {} }` : 'null'}, { title: 'This machine' }));
        return !!box.querySelector('.machine-health-title'); })()`);
    const makeNode = html => ev(`(() => {
        let box = document.getElementById('__cssdrift'); if (!box) { box = document.createElement('div'); box.id = '__cssdrift'; document.body.appendChild(box); }
        box.insertAdjacentHTML('beforeend', ${JSON.stringify(html)});
        return true; })()`);

    const measure = selector => ev(`(() => {
        const el = document.querySelector(${JSON.stringify(selector)});
        if (!el) return null;
        const cs = getComputedStyle(el);
        const out = {};
        for (const p of ${JSON.stringify(PROPS)}) out[p] = cs[p];
        return out; })()`);

    const captured = {}; // name -> theme -> props|null
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await goRoot();

        for (const theme of THEMES) {
            // 'root' + 'make' + the two health cards all live on the agents page.
            await goRoot();
            await applyTheme(theme); await settle();
            await makeSandbox();
            for (const e of ELEMENTS.filter(e => e.where === 'make')) await makeNode(e.html);
            for (const e of ELEMENTS.filter(e => e.where === 'root' || e.where === 'make')) {
                (captured[e.name] ||= {})[theme] = await measure(e.selector);
            }
            for (const [where, ok] of [['health-ok', true], ['health-unknown', false]]) {
                if (!WHERES.includes(where)) continue;
                await renderHealth(ok); await settle();
                for (const e of ELEMENTS.filter(e => e.where === where)) (captured[e.name] ||= {})[theme] = await measure(e.selector);
            }
            await removeSandbox();

            // Settings modal.
            if (WHERES.includes('settings')) {
                await ev(`window.showSettingsModal && window.showSettingsModal()`);
                await waitFor(`(() => { const m = document.getElementById('settings-modal'); return m && getComputedStyle(m).display !== 'none' && !!document.getElementById('settings-title'); })()`, 'the settings modal', { timeout: 8000, soft: true });
                await applyTheme(theme); await settle();
                for (const e of ELEMENTS.filter(e => e.where === 'settings')) (captured[e.name] ||= {})[theme] = await measure(e.selector);
                await ev(`window.hideSettingsModal && window.hideSettingsModal()`);
            }

            // Board view.
            if (WHERES.includes('board')) {
                const l = Page.loadEventFired(); await Page.navigate({ url: BASE + '/board' }); await l; await coralReady();
                await waitFor(`!!document.getElementById('messageboard-view') && !!document.querySelector('.mb-view-header')`, 'the board view', { timeout: 8000, soft: true });
                await applyTheme(theme); await settle();
                for (const e of ELEMENTS.filter(e => e.where === 'board')) (captured[e.name] ||= {})[theme] = await measure(e.selector);
            }
        }

        if (process.env.CSS_DRIFT_UPDATE) {
            // Refuse to bake a null into the baseline: a selector broken at update time would make
            // that element fail forever with an unhelpful message (Lead's SHOULD, #3626).
            const missing = [];
            for (const e of ELEMENTS) for (const theme of THEMES) if (!captured[e.name]?.[theme]) missing.push(`${e.name} [${theme}]`);
            if (missing.length) {
                check('every element was captured before writing the baseline', false, `not found: ${missing.join(', ')} — fix the selector(s), don't bake a null`);
                console.log(`\n${results.filter(Boolean).length}/${results.length} passed`);
                await client.close();
                process.exit(1);
            }
            fs.writeFileSync(BASELINE, JSON.stringify(captured, null, 2) + '\n');
            check(`baseline updated (${ELEMENTS.length} elements x ${THEMES.length} themes)`, true, BASELINE);
            console.log(`\n${results.filter(Boolean).length}/${results.length} passed`);
            await client.close();
            process.exit(0);
        }

        if (!fs.existsSync(BASELINE)) {
            check('the baseline exists (run once with CSS_DRIFT_UPDATE=1)', false, BASELINE);
        } else {
            const base = JSON.parse(fs.readFileSync(BASELINE, 'utf8'));
            for (const e of ELEMENTS) {
                for (const theme of THEMES) {
                    const want = base[e.name]?.[theme];
                    const got = captured[e.name]?.[theme];
                    if (!want) { check(`${e.name} [${theme}]: in the baseline`, false, 'missing from baseline — update it'); continue; }
                    if (!got) { check(`${e.name} [${theme}]: element found`, false, `selector ${e.selector} not found`); continue; }
                    const diffs = PROPS.filter(p => !sameComputed(want[p], got[p]))
                        .map(p => `${p}: ${JSON.stringify(want[p])} -> ${JSON.stringify(got[p])}`);
                    check(`${e.name} [${theme}]: computed styles unchanged`, diffs.length === 0, diffs.length ? diffs.join('; ') : undefined);
                }
            }
            // A baseline entry with no element is drift too (an element removed/renamed).
            for (const name of Object.keys(base)) {
                if (!ELEMENTS.some(e => e.name === name)) check(`${name}: still in the element list`, false, 'in the baseline but not measured — remove from baseline or restore');
            }
        }
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
