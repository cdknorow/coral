// #3720 (from the #3714 audit): what an agent switch and the chat poll request.
//  - The pending tool and the options of an open prompt ride on the chat poll's reply (the GET /pending-tool and
//    GET /prompt-options routes are gone); the options are only in the reply while a prompt may be open.
//  - A switch no longer fetches the session detail (a hidden pane's capture), /tasks or /events: the first
//    GET /poll carries the tasks and events.
// Network counts come from a fetch wrapper installed in the page before the app loads; it also notes the keys
// of the latest chat reply.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { launchStandIn } = require('./helpers/stand_in_agent');

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

async function httpJson(method, path, body, headers = {}) {
    const r = await fetch(BASE + path, { method, headers: { 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body) });
    let data = null; try { data = await r.json(); } catch { /* no body */ }
    return { status: r.status, data };
}

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    let standIn = null;
    try {
        standIn = await launchStandIn(httpJson, { displayName: 'Switch Requests QA' });
        if (!standIn.sid) throw new Error('no stand-in agent: ' + standIn.diag);
        const sid = standIn.sid, name = standIn.name, tok = { 'X-Coral-Session-Token': standIn.token };
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__net = []; window.__chatKeys = [];
            const realFetch = window.fetch;
            window.fetch = async function (u, o) {
                const url = String((u && u.url) || u);
                window.__net.push({ t: Date.now(), url });
                const resp = await realFetch.apply(this, arguments);
                if (/\\/api\\/sessions\\/live\\/[^/]+\\/chat\\?/.test(url) && resp.ok) {
                    const data = await resp.clone().json().catch(() => null);
                    if (data) window.__chatKeys = Object.keys(data);
                }
                return resp;
            };
            localStorage.removeItem('coral-live-view-mode'); // Chat, the default
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        const count = (re, since) => ev(`window.__net.filter(r => r.t >= ${since} && new RegExp(${JSON.stringify(re)}).test(r.url)).length`);
        const now = () => ev(`Date.now()`);
        const chatKeys = () => ev(`window.__chatKeys`);
        const live = '/api/sessions/live/[^/?]+';
        const encName = encodeURIComponent(name);

        // ── The switch ─────────────────────────────────────────────────────
        await until(`!!document.querySelector('.session-group-item[data-session-id="${sid}"]')`, 'the agent in the list');
        const t0 = await now();
        await ev(`document.querySelector('.session-group-item[data-session-id="${sid}"]').click(); true`);
        await until(`window.__net.filter(r => r.t >= ${t0} && /\\/chat\\?/.test(r.url) && /after=/.test(r.url)).length >= 3`, 'the chat poll running');
        check('the chat reply carries "pending"', (await chatKeys()).includes('pending'), JSON.stringify(await chatKeys()));
        check('a poll with no hook event yet may have a prompt open, so the reply has prompt_options', (await chatKeys()).includes('prompt_options'));
        check('no request goes to /pending-tool or /prompt-options', (await count(live + '/(pending-tool|prompt-options)', t0)) === 0, String(await count(live + '/(pending-tool|prompt-options)', t0)));
        check('the switch fetches no session detail (the hidden pane\'s capture)', (await count(live + '(\\?|$)', t0)) === 0, String(await count(live + '(\\?|$)', t0)));
        check('...no /tasks and no /events', (await count(live + '/(tasks|events)\\?', t0)) === 0, String(await count(live + '/(tasks|events)\\?', t0)));
        check('...and the first /poll did come (it carries the tasks and events)', (await count(live + '/poll\\?', t0)) >= 1);

        // ── Hook events decide whether the options are read ────────────────
        const postEvent = type => httpJson('POST', `/api/sessions/live/${encName}/events`, { event_type: type, summary: type, session_id: sid }, tok);
        let r = await postEvent('prompt_submit');
        check('the stand-in\'s hook event is accepted', r.status === 200 || r.status === 201, String(r.status));
        await until(`!window.__chatKeys.includes('prompt_options')`, 'a working agent to lose prompt_options', 5000);
        check('a working agent (prompt_submit): the reply has no prompt_options', !(await chatKeys()).includes('prompt_options'), JSON.stringify(await chatKeys()));
        await postEvent('permission_request');
        await until(`window.__chatKeys.includes('prompt_options')`, 'prompt_options with a permission request', 5000);
        check('a permission request: the reply has prompt_options again', (await chatKeys()).includes('prompt_options'));
        await postEvent('stop');
        await until(`!window.__chatKeys.includes('prompt_options')`, 'prompt_options to go after stop', 5000);
        check('stop: no prompt_options again', !(await chatKeys()).includes('prompt_options'));

        // ── A pending tool shows its card from the reply alone ─────────────
        const t1 = await now();
        r = await httpJson('POST', `/api/sessions/live/${encName}/pending-tool`, { session_id: sid, tool_use_id: 'toolu_qa_1', tool_name: 'AskUserQuestion', tool_input: { questions: [{ question: 'Which colour?', options: [{ label: 'Red' }, { label: 'Blue' }] }] } }, tok);
        check('the pending tool is accepted', r.status === 200, String(r.status));
        await until(`!!document.querySelector('#live-history-messages .chat-needs-input')`, 'the needs-input card', 6000);
        check('a pending question shows its card', await ev(`!!document.querySelector('#live-history-messages .chat-needs-input')`));
        check('...with no GET /pending-tool at all', (await count(live + '/pending-tool', t1)) === 0, String(await count(live + '/pending-tool', t1)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        if (standIn) await standIn.stop().catch(() => {});
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
