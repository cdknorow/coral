// CSP stage 2, first slice (#3645): the delegated data-on-* dispatcher (actions.js) and the Cost
// view moved onto it.
//  - The dispatcher: actions run innermost first; stopPropagation() in an action stops the walk
//    and every later document listener (as an inline handler's did); change events work; an
//    unknown name does nothing; a name can't be registered twice.
//  - The Cost view has no inline handler left, and each of its actions still works: time range,
//    refresh, column sort, column filter (without sorting, and a second filter replaces the first),
//    an agent's link to its live session or its history, and a task row's detail.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

const SUMMARY = {
    totals: { input_tokens: 10, output_tokens: 20, cost_usd: 3 },
    by_model: [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 2, cost_usd: 3 }],
    by_agent: [
        { session_id: 'aaaaaaaa-0000-4000-8000-000000000001', agent_name: 'Ended Agent', board_name: 'team-x', folder: 'proj', cost_usd: 2, requests: 5 },
        { session_id: 'aaaaaaaa-0000-4000-8000-000000000002', agent_name: 'Other Agent', board_name: 'team-y', folder: 'proj', cost_usd: 1, requests: 3 },
    ],
};
const TASKS = { tasks: [{ id: 4242, title: 'A costly task', status: 'completed', priority: 'high', cost_usd: 1.5, assigned_to: 'Ended Agent' }] };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 10000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__cost = { summary: 0 };
            const inner = window.fetch;
            window.fetch = (url, opts) => {
                const u = String(url && url.url || url);
                const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
                if (u.includes('/api/token-usage/summary')) { window.__cost.summary++; return json(${JSON.stringify(SUMMARY)}); }
                if (u.endsWith('/api/board/tasks')) return json(${JSON.stringify(TASKS)});
                if (u.includes('/api/token-usage/')) return json({});
                return inner(url, opts);
            };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // ── The dispatcher ─────────────────────────────────────────────────
        const d = await ev(`(async () => {
            const { registerActions, hasAction } = await import(${MOD} + 'actions.js');
            const log = [];
            registerActions({
                't-inner': () => log.push('inner'),
                't-outer': () => log.push('outer'),
                't-stop': (el, e) => { log.push('stop'); e.stopPropagation(); },
                't-change': el => log.push('change:' + el.value),
            });
            let twice = false;
            try { registerActions({ 't-inner': () => {} }); } catch { twice = true; }
            const host = document.createElement('div');
            host.innerHTML = '<div data-on-click="t-outer"><span data-on-click="t-inner"><b id="t-b">x</b></span></div>'
                + '<div data-on-click="t-outer"><span data-on-click="t-stop"><i id="t-i">y</i></span></div>'
                + '<select id="t-sel" data-on-change="t-change"><option>a</option><option>b</option></select>'
                + '<span id="t-unknown" data-on-click="t-not-registered">z</span>';
            document.body.appendChild(host);
            let later = 0;
            const after = () => later++;
            document.addEventListener('click', after);
            document.getElementById('t-b').click();
            const walk = log.splice(0);
            const laterAfterWalk = later;
            document.getElementById('t-i').click();
            const stopped = log.splice(0);
            const laterAfterStop = later;
            const sel = document.getElementById('t-sel'); sel.value = 'b'; sel.dispatchEvent(new Event('change', { bubbles: true }));
            const changed = log.splice(0);
            document.getElementById('t-unknown').click();
            document.removeEventListener('click', after);
            host.remove();
            return { walk, stopped, changed, twice, laterAfterWalk, laterAfterStop, has: hasAction('t-inner') };
        })()`);
        check('actions run innermost first, then their ancestors', JSON.stringify(d.walk) === '["inner","outer"]', JSON.stringify(d.walk));
        check('stopPropagation() stops the walk', JSON.stringify(d.stopped) === '["stop"]', JSON.stringify(d.stopped));
        check('...and later document listeners, as an inline handler did', d.laterAfterWalk === 1 && d.laterAfterStop === 1, `${d.laterAfterWalk}/${d.laterAfterStop}`);
        check('change events run their action', JSON.stringify(d.changed) === '["change:b"]', JSON.stringify(d.changed));
        check('a name can be registered once only', d.twice && d.has);

        // ── The Cost view ──────────────────────────────────────────────────
        await ev(`window.switchNavTab('tokens'); true`);
        await until(`document.querySelectorAll('#cost-by-agent th.cost-th-sort').length > 0 && document.querySelectorAll('#cost-by-task tr[data-on-click]').length > 0`, 'the Cost tables');
        const inline = await ev(`[...document.querySelectorAll('#cost-dashboard-view *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + ' ' + a.name))`);
        check('the Cost view has no inline handler left', inline.length === 0, JSON.stringify(inline.slice(0, 5)));

        let before = await ev(`window.__cost.summary`);
        await ev(`document.querySelector('#cost-dashboard-view [data-on-click="cost-refresh"]').click(); true`);
        await until(`window.__cost.summary > ${before}`, 'a refresh');
        check('Refresh reloads the numbers', await ev(`window.__cost.summary > ${before}`));
        before = await ev(`window.__cost.summary`);
        await ev(`(() => { const s = document.getElementById('cost-time-range'); s.value = s.options[s.options.length - 1].value; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
        await until(`window.__cost.summary > ${before}`, 'a reload for the new range');
        check('changing the time range reloads', await ev(`window.__cost.summary > ${before}`));

        const header = name => `[...document.querySelectorAll('#cost-by-agent th.cost-th-sort')].find(th => th.textContent.trim().startsWith(${JSON.stringify(name)}))`;
        await ev(`${header('Agent')}.click(); true`);
        await until(`/[\\u25B2\\u25BC]/.test(${header('Agent')}.textContent)`, 'the sort arrow');
        const first = await ev(`${header('Agent')}.textContent`);
        await ev(`${header('Agent')}.click(); true`);
        await until(`${header('Agent')}.textContent !== ${JSON.stringify(first)}`, 'the arrow flipped');
        const second = await ev(`${header('Agent')}.textContent`);
        check('a column header sorts, and again reverses', /▼/.test(first) && /▲/.test(second), `${first.trim()} | ${second.trim()}`);

        await ev(`${header('Team')}.querySelector('[data-on-click="cost-filter"]').click(); true`);
        await until(`document.querySelectorAll('.cost-filter-dropdown').length === 1`, 'the filter dropdown');
        const stillAgent = await ev(`/[\\u25B2\\u25BC]/.test(${header('Agent')}.textContent) && !/[\\u25B2\\u25BC]/.test(${header('Team')}.textContent)`);
        check('the filter icon opens its dropdown and does not sort the column', stillAgent);
        await ev(`${header('Folder')}.querySelector('[data-on-click="cost-filter"]').click(); true`);
        await new Promise(r => setTimeout(r, 200)); // sleep-ok: let any close-on-outside-click run
        check('a second filter icon replaces the open dropdown (one stays open)', await ev(`document.querySelectorAll('.cost-filter-dropdown').length === 1`));
        await ev(`document.body.click(); true`);
        await until(`document.querySelectorAll('.cost-filter-dropdown').length === 0`, 'closed by an outside click');
        check('an outside click closes it', await ev(`document.querySelectorAll('.cost-filter-dropdown').length === 0`));

        const nav = await ev(`(() => {
            const calls = [];
            const keep = { switchNavTab: window.switchNavTab, selectHistorySession: window.selectHistorySession };
            window.switchNavTab = t => calls.push('tab:' + t);
            window.selectHistorySession = id => calls.push('history:' + id);
            const a = document.querySelector('#cost-by-agent a[data-on-click="cost-open-history"]');
            const ev = new MouseEvent('click', { bubbles: true, cancelable: true });
            a.dispatchEvent(ev);
            Object.assign(window, keep);
            return { calls, prevented: ev.defaultPrevented, sid: a.dataset.sessionId };
        })()`);
        check('an ended agent opens its history (the link does not navigate)',
            JSON.stringify(nav.calls) === JSON.stringify(['tab:history', 'history:' + nav.sid]) && nav.prevented, JSON.stringify(nav));

        const task = await ev(`(() => {
            const keep = window.showTaskDetailModal;
            let shown = null;
            window.showTaskDetailModal = id => { shown = id; };
            document.querySelector('#cost-by-task tr[data-on-click="cost-task"]').click();
            window.showTaskDetailModal = keep;
            return shown;
        })()`);
        check('a task row opens its detail', task === 4242, String(task));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
