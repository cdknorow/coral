// The shared list bar sends its search text and its text fields trimmed, whatever the panel's model does with them (#4813, after #4806: the server refuses a q with a tab or a space at either end).
// Built directly in a page with a model whose clean() does NOT trim (so only the bar can): a q typed or pasted with a tab and spaces around it, and a text field the same, reach onChange trimmed,
// by the debounced input, by Enter, and from a text field; inner spaces are kept. Run against an isolated dev server.
'use strict';
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(async () => {
            const LB = await import((window.__coralStatic || '/static/') + 'list_bar.js');
            window.__got = [];
            const model = { EMPTY: { q: '', who: '' }, clean: f => ({ q: String((f && f.q) || ''), who: String((f && f.who) || '') }), chips: () => [], isFiltered: f => !!(f.q || f.who), without: (f, k) => ({ ...f, [k]: '' }) };
            const spec = { idp: 'zt', noun: 'x' + 'y', label: 'x', searchLabel: 'x', placeholder: 'x', sorts: null, fields: [{ type: 'text', key: 'who', id: 'zt-who', className: 'list-bar-by', label: 'Who', max: 40, placeholder: 'who' }], model };
            const bar = LB.buildListBar(spec, f => window.__got.push(JSON.stringify(f)));
            document.body.append(bar.node); window.__bar = bar; return true; })()`);
        const type = async (sel, text) => { await ev(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.focus(); e.select && e.select(); return true; })()`); await Input.insertText({ text }); };
        const last = () => ev(`JSON.parse(window.__got[window.__got.length - 1] || 'null')`);

        console.log('1. A typed search: trimmed after the pause');
        await type('#zt-search', '\t  hello  world \t');
        await waitFor(`window.__got.length > 0`, 'the debounced change', 4000);
        assert.deepEqual(await last(), { q: 'hello  world', who: '' }, 'trimmed, the inner spaces kept');

        console.log('2. Enter sends at once, trimmed');
        await ev(`window.__got.length = 0; true`);
        await type('#zt-search', '  \tworld\t ');
        await ev(`document.querySelector('#zt-search').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); true`);
        assert.equal((await last()).q, 'world');

        console.log('3. A text field of the Filters menu is trimmed too');
        await ev(`window.__got.length = 0; const w = document.querySelector('#zt-who'); w.value = '  Dev One \t'; w.dispatchEvent(new Event('input', { bubbles: true })); true`);
        await waitFor(`window.__got.length > 0`, 'the debounced change', 4000);
        assert.equal((await last()).who, 'Dev One');

        console.log('4. Only blanks is no search');
        await ev(`window.__got.length = 0; true`);
        await type('#zt-search', ' \t ');
        await ev(`document.querySelector('#zt-search').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); true`);
        assert.equal((await last()).q, '');
        console.log('PASS list bar trim');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
