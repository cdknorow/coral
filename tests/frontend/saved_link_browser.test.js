// The link to one saved message (#4596): /?saved=<key>&kind=agent|board&ref=<conversation>[&server=<id>], for Coral Hub's /saved page to link into a machine's dashboard. It runs the
// same jump the Saved tab's Open does (pages up to the message, outlines and focuses it, the jump bar); a message that is gone says so in a toast; a link with any part
// malformed is ignored and asks the server for nothing. The agent and its transcript are the harness's (each message keyed the way the server keys it); boards are the real server's.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const { removeBoards } = require('./helpers/board_cleanup');
const hex = i => i.toString(16).padStart(20, '0');

(async () => {
    const { client, Page, Emulation, Input, ev, waitFor, sleep, PRELOAD, install, select } = await connect();
    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };
    const key = async (k, code, vk) => { for (const type of ['keyDown', 'keyUp']) await client.Input.dispatchKeyEvent({ type, key: k, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, ...(type === 'keyDown' && k === 'Enter' ? { text: '\r' } : {}) }); };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const saved = async (q = '') => (await api('GET', '/api/saved' + q)).body;
    const C = `document.getElementById('live-history-messages')`;
    // The Saved tab of the right sidebar is shown: its panel is the active one and the sidebar is not collapsed.
    const TAB_OPEN = `(() => { const p = document.getElementById('agentic-panel-saved'), a = document.getElementById('agentic-state'); return p.classList.contains('active') && getComputedStyle(p).display !== 'none' && !a.classList.contains('collapsed') && p.getBoundingClientRect().width > 0; })()`;

    // The server adds "key" to each chat message (the harness's mock does not): a key from the message's absolute index, and ?find_key= answered from it.
    const MOCK = `(() => {
        const prev = window.fetch;
        window.fetch = async (url, opts = {}) => {
            const u = String(url);
            const m = u.match(/\\/api\\/sessions\\/live\\/([^/?]+)\\/chat\\?(.*)$/);
            if (!m) return prev(url, opts);
            const p = new URLSearchParams(m[2]);
            const total = window.__m.byPathSeg(m[1]).msgs.length;
            if (p.has('find_key')) {
                const k = p.get('find_key'); const idx = /^[0-9a-f]{20}$/.test(k) ? parseInt(k, 16) : -1;
                return new Response(JSON.stringify({ found: idx >= 0 && idx < total, index: Math.max(idx, 0), total }), { status: 200, headers: { 'content-type': 'application/json' } });
            }
            const resp = await prev(url, opts);
            const body = await resp.clone().json().catch(() => null);
            if (!body || !body.messages) return resp;
            const start = Number.isInteger(body.start) ? body.start : (Number(p.get('after') || 0));
            body.messages = body.messages.map((x, i) => ({ ...x, key: (start + i).toString(16).padStart(20, '0') }));
            return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
        };
    })()`;

    const OLD = 5;
    const SEEN = `(() => { const prev = window.fetch; window.__urls = []; window.fetch = (u, o) => { window.__urls.push(String(u)); return prev(u, o); }; })()`;
    const SAVED_URLS = `window.__urls.filter(u => /\\/api\\/(sessions\\/history\\/|sessions\\/live\\/[^/?]+\\/chat\\?.*find_key|board\\/[^/]+\\/messages\\/all\\?id=)/.test(u))`;
    const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent)`);
    const link = (o) => '/?' + new URLSearchParams(o).toString();

    let BOARD = ''; // the board this suite makes, taken away again in the finally (#4959)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(MOCK);
        await ev(`(() => { window.__m.add('sv-a', 'S', 260, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); })()`);
        await select('sv-a');
        await waitFor(`${C}.querySelectorAll('[data-msg-key]').length > 20`, 'the newest page', 8000);
        const REF = await ev(`T.state.liveSessions.find(s => s.name === 'sv-a').session_id`);
        const open = item => ev(`import((window.__coralStatic || '/static/') + 'saved.js').then(m => m.openSavedLink(${JSON.stringify(item)}))`);

        console.log('1. An agent message that is paged out: the link opens the agent, pages up to it, outlines and focuses it, and shows the jump bar');
        assert.equal(await ev(`!${C}.querySelector('[data-msg-key="${hex(OLD)}"]')`), true, 'the message is not on screen to start with');
        assert.equal(await open({ kind: 'agent', ref: REF, key: hex(OLD), server: 'local' }), true);
        await waitFor(`!!${C}.querySelector('[data-msg-key="${hex(OLD)}"].chat-search-target')`, 'the jump to the message', 12000);
        const at = await ev(`(() => { const t = ${C}.querySelector('[data-msg-key="${hex(OLD)}"]'); const r = t.getBoundingClientRect(), c = ${C}.getBoundingClientRect(); return { focused: document.activeElement === t, visible: r.top >= c.top - 1 && r.bottom <= c.bottom + 1 }; })()`);
        assert.ok(at.focused && at.visible, JSON.stringify(at));
        await waitFor(`!!document.getElementById('saved-jumpbar') && !!${C}.querySelector('[data-msg-key="${hex(OLD)}"].saved-jump')`, 'the jump bar and the Saved tag', 6000);

        console.log('2. A key the transcript no longer has: said so in a toast, nothing else moves');
        await select('sv-a');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); document.getElementById('saved-jumpbar')?.remove(); true`);
        assert.equal(await open({ kind: 'agent', ref: REF, key: hex(9999), server: 'local' }), false);
        const g = await toasts();
        assert.equal(g.length, 1, JSON.stringify(g)); assert.match(g[0], /no longer has this message/);
        assert.equal(await ev(`!document.getElementById('saved-jumpbar')`), true);

        console.log('3. A board message: the link opens the board at the message; a deleted one is gone');
        BOARD = 'saved-4596-board';
        await api('POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: 'Saver', job_title: 'Saver' });
        for (const c of ['first board message', 'the one to find']) { const r = await api('POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'Saver', content: c }); assert.equal(r.status, 200, JSON.stringify(r)); }
        const bm = (await api('GET', `/api/board/${BOARD}/messages/all?limit=10`)).body; const msgs = bm.messages || bm;
        const keep = msgs.find(m => m.content === 'the one to find');
        assert.ok(keep);
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        assert.equal(await open({ kind: 'board', ref: BOARD, key: String(keep.id), server: 'local' }), true);
        await waitFor(`!!document.querySelector('#mb-messages [data-message-id="${keep.id}"].chat-search-target')`, 'the jump into the board', 12000);
        await waitFor(`!!document.getElementById('saved-jumpbar') && !!document.querySelector('#mb-messages .saved-jump')`, 'the jump bar and the Saved tag', 6000);
        assert.equal(await open({ kind: 'board', ref: BOARD, key: String(keep.id + 9999), server: 'local' }), false);
        assert.match((await toasts()).pop(), /deleted from the board/);

        console.log('4. Opened as a URL: a link to a message that is gone says so; a malformed link is ignored and asks for nothing');
        await Page.addScriptToEvaluateOnNewDocument({ source: SEEN });
        await Page.navigate({ url: BASE + link({ saved: hex(1), kind: 'agent', ref: 'no-such-conversation' }) }); await Page.loadEventFired();
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /transcript is gone|no longer has/.test(t.textContent))`, 'the toast for a conversation that is gone', 15000, `({ toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent), search: location.search })`);
        assert.equal(await ev(`location.search`), '', 'the dashboard wrote its own URL');
        for (const bad of [{ saved: 'zz', kind: 'agent', ref: 'x' }, { saved: hex(1), kind: 'board', ref: 'x' }, { saved: hex(1), kind: 'agent', ref: '../../etc' }, { saved: hex(1), kind: 'agent', ref: 'a\nb' }, { saved: hex(1), kind: 'nope', ref: 'x' }]) {
            await Page.navigate({ url: BASE + link(bad) }); await Page.loadEventFired();
            await ev(`window.__coralReady.then(() => true)`);
            await sleep(1200); // sleep-ok: a link that is ignored does nothing to wait for; the absence of a toast and a request is the assertion
            assert.deepEqual(await toasts(), [], 'ignored: ' + JSON.stringify(bad));
            assert.deepEqual(await ev(SAVED_URLS), [], 'no request for ' + JSON.stringify(bad));
        }
        console.log('\nPASS saved link: the jump through paging, gone, a board message, a URL, malformed links ignored');
    } finally { await removeBoards(ev, [BOARD]); await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
