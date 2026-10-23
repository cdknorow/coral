// The thinking chip is there the moment an agent is selected (#6221, the operator's bug found in Codex Test's walk): a Codex agent on Default effort (no level stored yet) had no thinking chip until a menu was opened, because the chip
// looked at the last loaded list (another agent type's, e.g. Claude's) instead of its own type's. Each agent type's levels are asked as soon as an agent of that type is selected: Claude then Codex-on-Default, Codex then Claude,
// a CLI with no levels (no chip, asked once), a list that cannot be read (no chip, no storm of asks). The chip code's own module and the list route stood in; no menu is opened anywhere. Drives Chrome over CDP through
// agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const real = window.fetch; window.__asks = []; window.fetch = async (u, o = {}) => { const p = String(u), m = /\\/api\\/agents\\/models\\?type=([a-z]+)/.exec(p);
            if (m) { window.__asks.push(m[1]); const t = m[1]; const j = b => new Response(JSON.stringify(b), { headers: { 'Content-Type': 'application/json' } });
                if (t === 'claude') return j({ models: [{ value: 'claude-sonnet-5-5', label: 'Sonnet' }], thinking: ['low', 'medium', 'high'], refreshing: false });
                if (t === 'codex') return j({ models: [{ value: 'gpt-5', label: 'GPT-5' }], thinking: ['minimal', 'low', 'medium', 'high'], refreshing: false });
                if (t === 'pi') return j({ models: [{ value: 'pi-1', label: 'Pi 1' }], thinking: [], refreshing: false });
                return new Response('x', { status: 500 }); }
            return real(u, o); }; return true; })()`);
        const row = (type, extra = {}) => `{ session_id: 's-${type}', name: '${type}-agent', agent_type: '${type}', model: ${type === 'codex' ? "'gpt-5'" : "'x'"}, sleeping: false, archived: false, ...${JSON.stringify(extra)} }`;
        const show = type => ev(`import(${S} + 'model_chip.js').then(m => { m.updateModelChip(${row(type)}, 'local'); return true; })`);
        const wrapShown = () => ev(`(() => { const w = document.getElementById('thinking-chip-wrap'), b = document.getElementById('model-chip-bar'); return !!w && !w.hidden && !!b && !b.hidden; })()`);
        const menusClosed = () => ev(`['model-chip-menu', 'thinking-chip-menu'].every(id => { const m = document.getElementById(id); return !m || m.hidden; })`);
        // Claude: always has levels
        await show('claude');
        check('Claude: the thinking chip is there', await wrapShown());
        // then Codex on Default: its own list is asked and the chip appears with no menu opened
        await show('codex');
        await waitFor(`(() => { const w = document.getElementById('thinking-chip-wrap'), b = document.getElementById('model-chip-bar'); return !!w && !w.hidden && !!b && !b.hidden; })()`, 'the Codex thinking chip', 5000);
        check('Codex on Default, after Claude: the thinking chip is there without opening a menu', await wrapShown() && await menusClosed(), JSON.stringify(await ev('window.__asks')));
        // the reverse
        await show('claude');
        check('Claude again after Codex: the thinking chip is there', await wrapShown());
        await show('codex');
        check('Codex again: the chip stays (its levels are known, not asked every time)', await wrapShown() && (await ev(`window.__asks.filter(a => a === 'codex').length`)) === 1, JSON.stringify(await ev('window.__asks')));
        // a CLI whose list has no levels: no chip, asked once however often the row redraws
        await show('pi'); await waitFor(`window.__asks.includes('pi')`, 'the Pi list asked', 5000);
        for (let i = 0; i < 4; i++) await show('pi');
        check('a CLI with no effort levels has no thinking chip and its list is asked once', !(await wrapShown()) && (await ev(`window.__asks.filter(a => a === 'pi').length`)) === 1, JSON.stringify(await ev('window.__asks')));
        // a list that cannot be read: no chip, and not asked again on every redraw
        await show('agy');
        for (let i = 0; i < 5; i++) await show('agy');
        await waitFor(`window.__asks.includes('agy')`, 'the agy list asked', 5000);
        check('a list that cannot be read: no chip, and not asked again on every redraw', !(await wrapShown()) && (await ev(`window.__asks.filter(a => a === 'agy').length`)) === 1, JSON.stringify(await ev('window.__asks')));
        // an agent that already reports a level keeps its chip (nothing asked for it)
        await ev(`window.__asks.length = 0; true`);
        await ev(`import(${S} + 'model_chip.js').then(m => { m.updateModelChip(${row('agy', { thinking_actual: 'high' })}, 'local'); return true; })`);
        check('an agent that reports its level keeps its chip', await wrapShown());
    } finally { /* the harness closes the page */ }
    const failed = results.filter(r => !r).length;
    console.log(`model_chip_levels_on_select: ${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})().catch(e => { console.error(e); process.exit(1); });
