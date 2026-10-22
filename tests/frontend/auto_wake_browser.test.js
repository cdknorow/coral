// Waking the agents that were running when a pty Coral stopped (#3636):
//  - Settings (on a pty Coral only): "Wake agents that were running when Coral stopped" and the
//    cap, saved through the settings PUT (the server validates 1-50).
//  - More agents than the cap: a banner asks. Wake them wakes each through the per-agent wake
//    route, at least 2 s apart; Not now dismisses (the agents stay asleep).
// The server here runs tmux; the pty and pending states are served by a fetch wrapper.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

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
    const until = (expr, what, ms = 15000) => waitFor(expr, what, { timeout: ms, soft: true });
    const PENDING = [1, 2, 3].map(i => ({ session_id: `0b2f6c1e-3d4a-4b5c-8d6e-00000000000${i}`, name: `agent ${i}` }));
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        // The fake goes in before the page's own code runs, so watchAutoWake sees it.
        // The over-the-cap pass looks 6 s after a start and again every 5 s (terminal_backend.js watchAutoWake): two steps of this suite waited that
        // long. Those two timers (and only those) run in 0.2 s here; the 2 s plus up to 1 s stagger between wakes is not touched, so the
        // "each wake at least 2 s after the one before" check still measures the real stagger.
        await Page.addScriptToEvaluateOnNewDocument({ source: `{ const realSetTimeout = window.setTimeout.bind(window); window.setTimeout = (fn, ms, ...rest) => realSetTimeout(fn, ms === 6000 || ms === 5000 ? 200 : ms, ...rest); }` });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__aw = { pending: ${JSON.stringify(PENDING)}, wakes: [], dismissed: false, puts: [] };
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const u = String(url), m = (opts.method || 'GET').toUpperCase();
                const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                const aw = window.__aw;
                if (/\\/api\\/system\\/auto-wake$/.test(u)) return json({ state: aw.dismissed || !aw.pending.length ? 'done' : 'pending', pending: aw.dismissed ? [] : aw.pending, woken: 0, failed: [], waiting: 0 });
                if (/\\/api\\/system\\/auto-wake\\/dismiss$/.test(u)) { aw.dismissed = true; return json({ state: 'done', pending: [], woken: 0, failed: [], waiting: 0 }); }
                const w = u.match(/\\/api\\/sessions\\/live\\/([^/]+)\\/wake$/);
                if (w && m === 'POST') { aw.wakes.push({ id: decodeURIComponent(w[1]), t: performance.now() }); aw.pending = aw.pending.filter(p => p.session_id !== decodeURIComponent(w[1])); return json({ ok: true, sleeping: false }); }
                if (/\\/api\\/system\\/terminal-backend$/.test(u) && m === 'GET') return json({ saved: 'pty', running: 'pty', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 });
                if (/\\/api\\/settings$/.test(u) && m === 'PUT') aw.puts.push(opts.body);
                return inner(url, opts);
            };
        })();` });
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await l;
        await coralReady();

        // ── The banner ─────────────────────────────────────────────────────
        await until(`!document.getElementById('auto-wake-banner').hidden`, 'the banner (the pass runs 5 s after a start)');
        const banner = await ev(`document.getElementById('auto-wake-banner').textContent.replace(/\\s+/g, ' ').trim()`);
        check('more agents than the cap: a banner asks', /3 agents were running when Coral stopped\. Wake them\?/.test(banner), banner);
        await ev(`document.getElementById('auto-wake-go').click(); true`);
        await until(`window.__aw.wakes.length === 3 && document.getElementById('auto-wake-banner').hidden`, 'all three woken', 20000);
        const wakes = await ev(`window.__aw.wakes`);
        const gaps = wakes.slice(1).map((w, i) => Math.round(w.t - wakes[i].t));
        check('Wake them calls the per-agent wake route for each, in order', JSON.stringify(wakes.map(w => w.id)) === JSON.stringify(PENDING.map(p => p.session_id)), JSON.stringify(wakes.map(w => w.id)));
        check('each wake at least 2 s after the one before', gaps.length === 2 && gaps.every(g => g >= 1990), JSON.stringify(gaps));
        check('the banner goes once they are woken', await ev(`document.getElementById('auto-wake-banner').hidden`));

        // ── Not now ────────────────────────────────────────────────────────
        await ev(`window.__aw.pending = ${JSON.stringify(PENDING)}; window.__aw.dismissed = false; window.__aw.wakes = [];
            import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => { m.watchAutoWake(); return true; })`);
        await until(`!document.getElementById('auto-wake-banner').hidden`, 'the banner again');
        await ev(`document.getElementById('auto-wake-dismiss').click(); true`);
        await until(`document.getElementById('auto-wake-banner').hidden`, 'dismissed');
        check('Not now dismisses it and wakes nothing', await ev(`window.__aw.dismissed && window.__aw.wakes.length === 0 && document.getElementById('auto-wake-banner').hidden`));

        // ── Settings, on a pty Coral ───────────────────────────────────────
        await ev(`window.showSettingsModal(); true`);
        await until(`document.getElementById('tb-autowake')?.hidden === false`, 'the auto-wake part');
        const row = await ev(`({ on: document.getElementById('tb-autowake-on').checked, cap: document.getElementById('tb-autowake-cap').value,
            text: document.getElementById('tb-autowake').textContent.replace(/\\s+/g, ' ') })`);
        check('on pty, Settings shows the wake setting, on, cap 8, and says bypass-permission agents resume as they ran',
            row.on && row.cap === '8' && /Wake agents after a restart/.test(row.text) && /running when Coral stopped/.test(row.text) && /bypassed permissions/.test(row.text), JSON.stringify(row));
        await ev(`document.getElementById('tb-autowake-on').checked = false; document.getElementById('tb-autowake-cap').value = '12'; window.applySettings(); true`);
        await until(`window.__aw.puts.some(b => b.includes('auto_wake_cap'))`, 'the save');
        const put = await ev(`JSON.parse(window.__aw.puts.find(b => b.includes('auto_wake_cap')))`);
        check('Apply saves the two settings (a boolean and a number)', put.auto_wake_after_restart === false && put.auto_wake_cap === 12, JSON.stringify(put));
        const saved = await ev(`fetch('/api/settings').then(r => r.json()).then(j => { const s = j.settings || j; return { on: s.auto_wake_after_restart, cap: s.auto_wake_cap }; })`);
        check('the server stores them', saved.on === 'false' && saved.cap === '12', JSON.stringify(saved));
        const bad = await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ auto_wake_cap: 99 }) }).then(r => r.status)`);
        check('the server refuses a cap outside 1-50', bad === 400, String(bad));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        try { await ev(`fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ auto_wake_after_restart: true, auto_wake_cap: 8 }) }).then(() => true)`); } catch { /* best effort */ }
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
