// #3722 (the #3714 agent-switch audit, fix 7): the board chat panel polled its board every 3 s
// for a count, and fetched its whole 50-message page again whenever that changed. Now it asks
// only for what follows the newest message it shows (after_id). Against a real board on the test
// server, with the panel's requests and their reply bodies read from the network:
//   - opening the panel loads the latest page once;
//   - a steady poll is one after_id request by the newest id shown, and its reply holds nothing;
//   - a message posted meanwhile comes back in the next poll's reply, alone, and shows once;
//   - showing the panel again for the same board (another agent of the team) loads no page.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Network } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    const board = `qa-chat-3722-${Date.now()}`;
    const api = `/api/board/${board}`;
    const post = content => ev(`fetch(${JSON.stringify(api + '/messages')}, { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ subscriber_id: 'qa-3722', as: 'QA', content: ${JSON.stringify(content)} }) }).then(r => r.status)`);

    // The panel's requests for this board, with their reply bodies once loaded.
    const seen = new Map();
    const reqs = [];
    Network.requestWillBeSent(({ requestId, request }) => {
        if (!request.url.includes(api + '/messages/all')) return;
        const r = { id: requestId, q: new URL(request.url).searchParams, body: null };
        seen.set(requestId, r);
        reqs.push(r);
    });
    Network.loadingFinished(async ({ requestId, encodedDataLength }) => {
        const r = seen.get(requestId);
        if (!r) return;
        try {
            const { body } = await Network.getResponseBody({ requestId });
            r.bytes = encodedDataLength;
            r.body = JSON.parse(body);
        } catch (e) { r.body = { error: String(e) }; }
    });
    const settled = () => reqs.every(r => r.body);
    const until = async (cond, what, ms = 12000) => {
        const end = Date.now() + ms;
        while (Date.now() < end) { if (cond()) return true; await new Promise(r => setTimeout(r, 100)); } // sleep-ok: polling node-side state
        console.log(`  (timed out waiting for ${what})`);
        return false;
    };
    const kind = r => r.q.has('after_id') ? 'after' : r.q.get('limit') === '1' ? 'count' : 'page';

    try {
        await Promise.all([Page.enable(), Runtime.enable(), Network.enable()]);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady();
        for (let i = 1; i <= 3; i++) check(`message ${i} is posted`, (await post(`before ${i}`)) === 200);

        // ── Opening the panel ──────────────────────────────────────────────
        await ev(`(async () => {
            // The panel is part of the agent view, whose polls keep only a slow beat while it is
            // off screen (#3719): show it, as when the panel is in use.
            const agentView = document.getElementById('live-session-view');
            if (agentView) agentView.style.display = '';
            if (!document.getElementById('agentic-panel-board')) {
                const p = document.createElement('div'); p.id = 'agentic-panel-board'; document.body.appendChild(p);
            }
            const m = await import(${S} + 'render.js');
            m.showBoardChatTab(${JSON.stringify(board)});
            return true;
        })()`);
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length === 3`, 'the panel’s messages', { timeout: 8000, soft: true });
        await until(() => reqs.length >= 1 && settled(), 'the first load');
        check('opening the panel loads the latest page, once', reqs.length === 1 && kind(reqs[0]) === 'page' && reqs[0].q.get('offset') === '0',
            JSON.stringify(reqs.map(kind)));
        const newest = reqs[0].body?.messages?.at(-1)?.id;
        check('with the board’s three messages', reqs[0].body?.messages?.length === 3 && Number.isInteger(newest));

        // ── A steady poll ──────────────────────────────────────────────────
        await until(() => reqs.length >= 3 && settled(), 'two polls');
        const quiet = reqs.slice(1);
        check('each poll is one after_id request', quiet.length >= 2 && quiet.every(r => kind(r) === 'after'), JSON.stringify(quiet.map(kind)));
        check('by the newest id shown', quiet.every(r => r.q.get('after_id') === String(newest)), JSON.stringify(quiet.map(r => r.q.get('after_id'))));
        // (none is "messages": null from the Go server, which the panel reads as none)
        check('and its reply holds no message', quiet.every(r => r.body && !r.body.error && (r.body.messages ?? []).length === 0),
            `${quiet.map(r => r.bytes).join(', ')} bytes (the page was ${reqs[0].bytes})`);

        // Tag the three messages' nodes: a redraw must keep them (#3881), not rebuild the page.
        await ev(`document.querySelectorAll('#board-panel-msgs .mb-message').forEach((n, i) => { n.__kept = 'm' + i; })`);

        // ── A new message ──────────────────────────────────────────────────
        const before = reqs.length;
        check('a new message is posted', (await post('the new one')) === 200);
        await until(() => reqs.slice(before).some(r => r.body?.messages?.length), 'the poll that brings it');
        const brought = reqs.slice(before).find(r => r.body?.messages?.length);
        check('the next poll’s reply holds the new message alone', brought && kind(brought) === 'after' && brought.body.messages.length === 1 &&
            brought.body.messages[0].content === 'the new one', brought && JSON.stringify(brought.body.messages.map(m => m.content)));
        await waitFor(`document.querySelectorAll('#board-panel-msgs .mb-message').length === 4`, 'the new message shown', { timeout: 5000, soft: true });
        const texts = await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message-body')].map(e => e.textContent.trim())`);
        check('it shows once, after the others', JSON.stringify(texts) === JSON.stringify(['before 1', 'before 2', 'before 3', 'the new one']), JSON.stringify(texts));
        const keptTags = await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message')].map(n => n.__kept || null)`);
        check('the earlier messages are the same nodes after the new one arrives (not rebuilt)', JSON.stringify(keptTags) === JSON.stringify(['m0', 'm1', 'm2', null]), JSON.stringify(keptTags));
        const newer = brought.body.messages[0].id;
        await until(() => reqs.some(r => r.q.get('after_id') === String(newer) && r.body), 'a poll after it');
        check('the next poll starts after it', reqs.some(r => r.q.get('after_id') === String(newer)));

        // ── The same board again (another agent of the team) ──────────────
        const mark = reqs.length;
        await ev(`import(${S} + 'render.js').then(m => { m.showBoardChatTab(${JSON.stringify(board)}); return true; })`);
        const shownAtOnce = await ev(`document.querySelectorAll('#board-panel-msgs .mb-message').length`);
        check('showing the panel again for the same board shows its messages at once', shownAtOnce === 4, String(shownAtOnce));
        const keptAgain = await ev(`[...document.querySelectorAll('#board-panel-msgs .mb-message')].map(n => n.__kept || null)`);
        check('and keeps the nodes it had', JSON.stringify(keptAgain) === JSON.stringify(['m0', 'm1', 'm2', null]), JSON.stringify(keptAgain));
        await until(() => reqs.length > mark && settled(), 'its first request');
        check('and loads no page: only what is new', reqs.slice(mark).every(r => kind(r) === 'after'), JSON.stringify(reqs.slice(mark).map(kind)));
        check('no request for a count or a page after the first load', reqs.slice(1).every(r => kind(r) === 'after'), JSON.stringify(reqs.map(kind)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        try {
            await ev(`import(${S} + 'render.js').then(m => { m.hideBoardChatTab(); return true; })`);
            await ev(`fetch(${JSON.stringify(api)}, { method: 'DELETE' }).then(r => r.status)`);
        } catch { /* the page may be gone */ }
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
