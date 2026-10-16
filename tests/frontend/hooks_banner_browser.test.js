// Agents on an older hook set (#3665): Claude reads its hooks once, at launch, so an agent started
// before a hook-set change keeps the old one. The dashboard lists them and resumes each through the
// per-agent refresh-hooks route (Sleep then Wake, the conversation kept):
//  - the banner is one line (the count, Show agents, Not now, Resume all); Show agents opens the list below it,
//    bounded in height and scrollable, grouped by team, each agent with its state and a per-agent button (#3729);
//  - the states say what is true: "older hooks (rev N, current is M)", "older hooks (no version reported)" for
//    an agent whose events carry no revision, "started before hook versions were tracked" when nothing is known;
//  - the banner sits above the home content or the phone's list and never overlaps them, at desktop and 390 px;
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
        { session_id: '0b2f6c1e-3d4a-4b5c-8d6e-0000000036a1', name: 'Lead', team: 'core', status: 'older', rev: 2 },
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

        // ── The banner: one line, the list opens under it ──────────────────
        await until(`!document.getElementById('hooks-banner').hidden`, 'the banner');
        check('the banner counts the agents on an older hook set', (await ev(`document.getElementById('hooks-count').textContent`)) === '3 agents run an older hook set', await ev(`document.getElementById('hooks-count').textContent`));
        check('the list is closed by default, behind a Show agents button',
            await ev(`document.getElementById('hooks-panel').hidden && document.getElementById('hooks-toggle').textContent === 'Show agents' && document.getElementById('hooks-toggle').getAttribute('aria-expanded') === 'false'`));
        check('the closed banner is one line (not much taller than a button row)', (await ev(`document.getElementById('hooks-banner').getBoundingClientRect().height`)) < 90);
        await ev(`document.getElementById('hooks-toggle').click(); true`);
        check('Show agents opens the list and becomes Hide agents',
            await ev(`!document.getElementById('hooks-panel').hidden && document.getElementById('hooks-toggle').textContent === 'Hide agents' && document.getElementById('hooks-toggle').getAttribute('aria-expanded') === 'true'`));
        const view = await ev(`({ groups: [...document.querySelectorAll('#hooks-list .hooks-team h4')].map(h => h.textContent),
            rows: [...document.querySelectorAll('#hooks-list li')].map(li => li.textContent.replace(/\\s+/g, ' ').trim()) })`);
        check('the agents are grouped by team', JSON.stringify(view.groups) === JSON.stringify(['core (3)']), JSON.stringify(view.groups));
        check('each agent shows its name, what is true of its hooks and a button',
            view.rows.length === 3 && view.rows[0].includes('Lead') && view.rows[0].includes('older hooks (rev 2, current is 3)') && view.rows[0].includes('Resume')
            && view.rows[1].includes('Dev') && view.rows[1].includes('started before hook versions were tracked')
            && view.rows[2].includes('QA') && view.rows[2].includes('older hooks (no version reported)'),
            JSON.stringify(view.rows));
        check('no row claims an agent was "not heard from" (it cannot be told)', !view.rows.some(r => /not heard from/.test(r)));
        await ev(`document.getElementById('hooks-toggle').click(); true`);

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

        // ── Never over the home content (the operator's screenshot: 16 agents drawn across the home page) ──
        const MANY = Array.from({ length: 16 }, (_, i) => ({ session_id: '0b2f6c1e-3d4a-4b5c-8d6e-00000000' + String(3790 + i), name: ['Lead Developer', 'Backend Dev', 'Frontend Dev', 'QA Engineer'][i % 4],
            team: ['core', 'platform', 'mobile', 'docs'][i % 4], status: i % 3 === 1 ? 'older' : 'unknown', rev: i % 3 === 1 ? 2 : 0 }));
        for (const [label, width, height, mobile] of [['desktop', 1400, 900, false], ['390 px', 390, 844, true]]) {
            await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
            const loaded = Page.loadEventFired();
            await Page.navigate({ url: BASE + '/' });
            await loaded;
            await coralReady();
            await ev(`window.__hk.agents = ${JSON.stringify(MANY)}; import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.watchHooksStatus()).then(() => true)`);
            await until(`!document.getElementById('hooks-banner').hidden`, `the banner at ${label}`);
            // What is on screen under it: the home page on a desktop, the phone's agent list on a phone.
            const under = mobile ? 'mobile-agent-list' : 'welcome-screen';
            const geometry = () => ev(`(() => { const b = document.getElementById('hooks-banner').getBoundingClientRect(), u = document.getElementById(${JSON.stringify(under)}).getBoundingClientRect(),
                m = document.querySelector('.main-content').getBoundingClientRect(), panel = document.getElementById('hooks-list');
                return { bannerTop: Math.round(b.top), bannerBottom: Math.round(b.bottom), height: Math.round(b.height), underTop: Math.round(u.top), underBottom: Math.round(u.bottom),
                    mainTop: Math.round(m.top), mainBottom: Math.round(m.bottom), listClient: panel.clientHeight, listScroll: panel.scrollHeight,
                    buttonsInside: ['hooks-go', 'hooks-dismiss', 'hooks-toggle'].every(id => { const r = document.getElementById(id).getBoundingClientRect(); return r.left >= b.left - 1 && r.right <= b.right + 1 && r.top >= b.top - 1 && r.bottom <= b.bottom + 1; }) }; })()`);
            let g = await geometry();
            check(`${label}: the banner is above the content, not over it (banner bottom ${g.bannerBottom} <= content top ${g.underTop})`, g.bannerBottom <= g.underTop + 1, JSON.stringify(g));
            check(`${label}: closed it is compact (${g.height} px)`, g.height <= (mobile ? 130 : 80), String(g.height));
            check(`${label}: its buttons sit inside the banner`, g.buttonsInside);
            await ev(`document.getElementById('hooks-toggle').click(); true`);
            g = await geometry();
            check(`${label}: open, the banner stays above the content and inside the main area`, g.bannerBottom <= g.underTop + 1 && g.bannerBottom <= g.mainBottom + 1 && g.bannerTop >= g.mainTop - 1, JSON.stringify(g));
            check(`${label}: open, the list is bounded (${g.listClient} px) and scrolls (${g.listScroll} px of rows)`, g.listClient <= 330 && g.listScroll > g.listClient);
            check(`${label}: open, 4 team groups`, (await ev(`document.querySelectorAll('#hooks-list .hooks-team').length`)) === 4);
            check(`${label}: open, the content below it can still be reached (it is in the scroll area or visible)`, g.underBottom > g.underTop);
            if (!mobile) {
                // The home page's heading is reachable and not covered by the banner.
                const covered = await ev(`(() => { const h = document.querySelector('#welcome-screen h1'); h.scrollIntoView({ block: 'center' }); const r = h.getBoundingClientRect(); const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return !!hit && !hit.closest('#hooks-banner'); })()`);
                check('desktop: the home heading is not covered by the banner', covered);
            }
        }
    } catch (e) {
        check('no exception', false, e.message);
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
