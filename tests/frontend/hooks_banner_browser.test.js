// Agents on an older hook set (#3665): Claude reads its hooks once, at launch, so an agent started
// before a hook-set change keeps the old one. The dashboard lists them and resumes each through the
// per-agent refresh-hooks route (Sleep then Wake, the conversation kept):
//  - the banner lists each agent with its state and a per-agent button;
//  - Resume all calls the route for each in order, at least 2 s apart; an agent in a turn (409 busy)
//    is skipped and stays listed;
//  - a per-agent button resumes that one; Not now hides the banner.
// The hooks status and the route are served by a fetch wrapper.
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
    const AGENTS = [
        { session_id: '0b2f6c1e-3d4a-4b5c-8d6e-0000000036a1', name: 'Lead', team: 'core', status: 'older' },
        { session_id: '0b2f6c1e-3d4a-4b5c-8d6e-0000000036a2', name: 'Dev', team: 'core', status: 'unknown' },
        { session_id: '0b2f6c1e-3d4a-4b5c-8d6e-0000000036a3', name: 'QA', team: 'core', status: 'older' },
    ];
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            try { sessionStorage.removeItem('coral.hooksBanner.dismissed'); } catch {}
            window.__hk = { agents: ${JSON.stringify(AGENTS)}, calls: [], busy: { '${AGENTS[1].session_id}': true } };
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const u = String(url), m = (opts.method || 'GET').toUpperCase();
                const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                const hk = window.__hk;
                if (/\\/api\\/system\\/hooks-status$/.test(u)) return json({ current: 3, agents: hk.agents });
                const r = u.match(/\\/api\\/sessions\\/live\\/([^/]+)\\/refresh-hooks$/);
                if (r && m === 'POST') {
                    const id = decodeURIComponent(r[1]);
                    hk.calls.push({ id, t: performance.now(), body: opts.body });
                    if (hk.busy[id] && !JSON.parse(opts.body || '{}').force) return json({ ok: false, code: 'busy', error: 'busy' }, 409);
                    hk.agents = hk.agents.filter(a => a.session_id !== id);
                    return json({ ok: true, hooks_rev: 3 });
                }
                return inner(url, opts);
            };
        })();` });
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await l;
        await coralReady();

        // ── The banner ─────────────────────────────────────────────────────
        await until(`!document.getElementById('hooks-banner').hidden`, 'the banner');
        const view = await ev(`({ count: document.getElementById('hooks-count').textContent,
            rows: [...document.querySelectorAll('#hooks-list li')].map(li => li.textContent.replace(/\\s+/g, ' ').trim()) })`);
        check('the banner counts the agents on an older hook set', view.count === '3 agents run an older hook set.', view.count);
        check('each agent is listed with its team, its state and a button',
            view.rows.length === 3 && ['Lead (core)', 'older hooks', 'Resume on current hooks'].every(x => view.rows[0].includes(x)) && view.rows[1].includes('not heard from since Coral started'),
            JSON.stringify(view.rows));

        // ── Resume all ─────────────────────────────────────────────────────
        await ev(`document.getElementById('hooks-go').click(); true`);
        await until(`window.__hk.calls.length === 3 && !document.getElementById('hooks-go').disabled`, 'all three tried', 20000);
        const calls = await ev(`window.__hk.calls`);
        const gaps = calls.slice(1).map((c, i) => Math.round(c.t - calls[i].t));
        check('Resume all calls the route for each, in order', JSON.stringify(calls.map(c => c.id)) === JSON.stringify(AGENTS.map(a => a.session_id)), JSON.stringify(calls.map(c => c.id)));
        check('each at least 2 s after the one before', gaps.length === 2 && gaps.every(g => g >= 1990), JSON.stringify(gaps));
        check('none forced', calls.every(c => !JSON.parse(c.body || '{}').force));
        await until(`document.querySelectorAll('#hooks-list li').length === 1`, 'only the busy one left');
        const left = await ev(`document.querySelector('#hooks-list li').textContent`);
        check('the agent in a turn is skipped and stays listed', /Dev/.test(left), left);

        // ── One agent ──────────────────────────────────────────────────────
        await ev(`window.__hk.busy = {}; window.__hk.calls = []; document.querySelector('#hooks-list button[data-session-id]').click(); true`);
        await until(`window.__hk.calls.length === 1 && document.getElementById('hooks-banner').hidden`, 'resumed, and the banner goes');
        check('a per-agent button resumes that agent, and an empty list hides the banner',
            (await ev(`window.__hk.calls[0].id`)) === AGENTS[1].session_id && await ev(`document.getElementById('hooks-banner').hidden`));

        // ── Not now ────────────────────────────────────────────────────────
        await ev(`window.__hk.agents = ${JSON.stringify(AGENTS.slice(0, 1))};
            import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.watchHooksStatus()).then(() => true)`);
        await until(`!document.getElementById('hooks-banner').hidden`, 'the banner again');
        await ev(`document.getElementById('hooks-dismiss').click(); true`);
        check('Not now hides it and resumes nothing', await ev(`document.getElementById('hooks-banner').hidden && window.__hk.calls.length === 1`));
    } catch (e) {
        check('no exception', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
