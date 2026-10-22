// Save for Later (#4510): a bookmark button on each message of an agent's Chat view (next to Copy), filled when saved, with an accessible name and aria-pressed;
// the Saved tab of the right sidebar, opened by the top bar's button (Open and Done, newest saved first, Done, Remove); clicking an item opens that agent and pages up to the message, which is
// marked and focused; a message that is gone is said so, with its snippet kept. The bookmarks are the real server's (/api/saved, the isolated test server's
// database); the agent and its transcript are the harness's, with each message's key added the way the server adds it.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const fs = require('node:fs');
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
            // The real server marks a bookmark whose transcript it cannot read (#5802), and the harness's agents have none on disk: the marks are what the mock transcripts say.
            if (/^\\/api\\/saved(\\?.*)?$/.test(u) && (opts.method || 'GET').toUpperCase() === 'GET') {
                const resp = await prev(url, opts);
                const body = await resp.clone().json().catch(() => null);
                if (!body || !Array.isArray(body.items)) return resp;
                body.items = body.items.map(it => {
                    if (it.kind !== 'agent') return it;
                    if (it.ref === 'dead0000-0000-4000-8000-000000000002') return { ...it, reason: 'not_reachable' }; // the agent with no transcript (7c)
                    const idx = parseInt(it.key, 16);
                    let total = it.ref === 'dead0000-0000-4000-8000-000000000001' ? 150 : 0; // the ended agent's mock history (7c)
                    if (!total) { try { total = window.__m.byPathSeg(it.title).msgs.length; } catch (e) { total = 0; } }
                    return { ...it, reason: idx >= 0 && idx < total ? '' : 'gone' };
                });
                return new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } });
            }
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
    const btnState = idx => ev(`(() => { const b = ${C}.querySelector('[data-msg-key="${hex(idx)}"] .chat-save-btn'); return b ? { pressed: b.getAttribute('aria-pressed'), label: b.getAttribute('aria-label'), hidden: b.hidden } : null; })()`);

    let BOARD = ''; // the board this suite makes, taken away again in the finally (#4959)
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(MOCK);
        // A clean slate: whatever an earlier suite of this shard saved is removed.
        for (const it of (await saved()).items) await api('DELETE', `/api/saved/${it.id}`);
        await ev(`(() => { window.__m.add('sv-a', 'S', 260, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); })()`);
        await select('sv-a');
        await waitFor(`${C}.querySelectorAll('[data-msg-key]').length > 20`, 'the newest page', 8000);

        console.log('1. A bookmark button on each message, with its name and state');
        const first = await ev(`(() => { const bs = [...${C}.querySelectorAll('[data-msg-key]')]; const k = bs[bs.length - 1].dataset.msgKey; return { n: bs.length, k, human: !!${C}.querySelector('.chat-bubble.human .chat-save-btn'), assistant: !!${C}.querySelector('.chat-bubble.assistant .chat-save-btn'), copyAlso: !!${C}.querySelector('.chat-bubble.assistant .chat-copy-btn') }; })()`);
        assert.ok(first.human && first.assistant && first.copyAlso, 'on your messages and the agent\'s, beside Copy: ' + JSON.stringify(first));
        const lastIdx = parseInt(first.k, 16);
        let st = await btnState(lastIdx);
        assert.deepEqual({ ...st, label: st.label.split(':')[0] }, { pressed: 'false', label: 'Save for later', hidden: false }); // the name goes on with the message it is on

        console.log('2. Saving by the keyboard (a real Enter on the focused button): filled, named "Remove from saved", on the server');
        await ev(`${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn').focus(); true`);
        await key('Enter', 'Enter', 13);
        await waitFor(`${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn').getAttribute('aria-pressed') === 'true'`, 'the button to show saved', 6000);
        st = await btnState(lastIdx);
        assert.deepEqual({ ...st, label: st.label.split(':')[0] }, { pressed: 'true', label: 'Remove from saved', hidden: false });
        assert.equal(await ev(`document.activeElement.classList.contains('chat-save-btn')`), true, 'the focus stays on the button');
        const toast1 = await ev(`(() => { const t = document.querySelector('.toast'); return t ? { text: t.querySelector('.toast-message').textContent, action: (t.querySelector('.toast-action') || {}).textContent } : null; })()`);
        assert.deepEqual(toast1, { text: 'Saved.', action: 'View saved' }, 'a toast says so and offers the list');
        assert.equal(await ev(`!!${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn svg') && !${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn .material-icons')`), true, 'the bookmark is an inline SVG');
        await waitFor(`document.querySelector('#saved-btn .saved-count').textContent === '1'`, 'the top bar count', 6000);
        let list = await saved();
        assert.equal(list.items.length, 1); assert.equal(list.items[0].kind, 'agent'); assert.equal(list.items[0].key, hex(lastIdx));
        assert.match(list.items[0].snippet, /^[SU]?\d*|lorem/); assert.ok(list.items[0].title, 'the agent\'s name is kept');
        // A redraw keeps the state (the page of messages is drawn again from the cache).
        await select('sv-a');
        await waitFor(`${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn')?.getAttribute('aria-pressed') === 'true'`, 'the state after a redraw', 6000);
        console.log('   ✓');

        console.log('2b. Removing says so with Undo; Undo saves it again; the "View saved" toast opens the list');
        const sb = `${C}.querySelector('[data-msg-key="${hex(lastIdx)}"] .chat-save-btn')`;
        await ev(`${sb}.click(); true`);
        await waitFor(`${sb}.getAttribute('aria-pressed') === 'false'`, 'the bookmark to be removed', 6000);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => t.querySelector('.toast-message').textContent === 'Removed from saved.' && (t.querySelector('.toast-action') || {}).textContent === 'Undo')`, 'the Removed toast with Undo', 4000);
        assert.equal((await saved()).items.length, 0, 'removed on the server');
        await waitFor(`document.querySelector('#saved-btn .saved-count').hidden === true`, 'the count to go', 6000);
        await ev(`[...document.querySelectorAll('.toast')].find(t => t.querySelector('.toast-action').textContent === 'Undo').querySelector('.toast-action').click(); true`);
        await waitFor(`${sb}.getAttribute('aria-pressed') === 'true'`, 'Undo to save it again', 6000);
        assert.equal((await saved()).items.length, 1, 'saved again on the server'); assert.equal((await saved()).items[0].key, hex(lastIdx));
        await ev(`${sb}.click(); true`);
        await waitFor(`${sb}.getAttribute('aria-pressed') === 'false'`, 'removed again', 6000);
        await ev(`${sb}.click(); true`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => (t.querySelector('.toast-action') || {}).textContent === 'View saved')`, 'the Saved toast with View saved', 6000);
        await ev(`[...document.querySelectorAll('.toast')].find(t => (t.querySelector('.toast-action') || {}).textContent === 'View saved').querySelector('.toast-action').click(); true`);
        await waitFor(TAB_OPEN, 'View saved to open the list', 6000);
        console.log('   ✓');

        console.log('3. An old message saved, then the Saved tab of the right sidebar from the top bar');
        // Page up to an old message and save it by its button.
        const OLD = 12;
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'live_chat.js'); return m.jumpToLiveMessage(${JSON.stringify(hex(OLD))}, ${OLD}); })()`);
        await waitFor(`!!${C}.querySelector('[data-msg-key="${hex(OLD)}"] .chat-save-btn')`, 'the old message', 8000);
        await ev(`${C}.querySelector('[data-msg-key="${hex(OLD)}"] .chat-save-btn').click(); true`);
        await waitFor(`${C}.querySelector('[data-msg-key="${hex(OLD)}"] .chat-save-btn').getAttribute('aria-pressed') === 'true'`, 'the old message saved', 6000);
        list = await saved();
        assert.equal(list.items.length, 2); assert.equal(list.items[0].key, hex(OLD), 'newest saved first');
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`${TAB_OPEN} && document.querySelectorAll('#saved-list .saved-item').length === 2`, 'the Saved tab with two items', 6000);
        await waitFor(`document.getElementById('agentic-panel-saved').contains(document.activeElement)`, 'the focus to move into the Saved tab', 6000);
        const dlg = await ev(`(() => { const items = [...document.querySelectorAll('#saved-list .saved-item')]; return { first: items[0].querySelector('.snip').textContent, title: items[0].querySelector('.src').textContent, kind: items[0].querySelector('.kind').textContent, meta: items[0].querySelector('.meta').textContent,
            tabs: [...document.querySelectorAll('.saved-tab')].map(t => t.getAttribute('aria-selected')), count: document.getElementById('saved-count-open').textContent, inside: !!document.activeElement.closest('#agentic-panel-saved') }; })()`);
        assert.equal(dlg.title, 'sv-a'); assert.equal(dlg.kind, 'Agent'); assert.match(dlg.meta, /Saved /); assert.equal(await ev(`document.querySelector('#saved-list .saved-open').getAttribute('aria-label')`), 'Open chat: sv-a', 'the Open button is named with its source'); assert.equal(await ev(`[...document.querySelectorAll('#saved-list .saved-item:first-child .acts .btn')].map(b => b.lastChild.textContent.trim()).join('|')`), 'Open chat|Done|Remove'); assert.deepEqual(dlg.tabs, ['true', 'false']); assert.equal(dlg.count, '(2)'); assert.equal(dlg.inside, true, 'the focus moved into the Saved tab');
        await shot('saved-tab-desktop');

        console.log('4. Done moves an item to the Done tab; it can be opened again; Remove removes it');
        await ev(`document.querySelector('#saved-list .saved-item .saved-done').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 1 && document.getElementById('saved-count-done').textContent === '(1)'`, 'the item to leave Open', 6000);
        await ev(`document.getElementById('saved-tab-done').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 1 && document.querySelector('#saved-list .saved-done').lastChild.textContent === 'Reopen'`, 'the Done tab', 6000);
        await ev(`document.querySelector('#saved-list .saved-done').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 0`, 'Mark open to bring it back', 6000);
        await ev(`document.getElementById('saved-tab-open').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 2`, 'both open again', 6000);
        console.log('   ✓');

        console.log('5. Click an item: the agent opens, pages up to the message, which is marked and has the focus');
        await select('sv-a'); // the newest page again: the old message is not on screen
        await waitFor(`!${C}.querySelector('[data-msg-key="${hex(OLD)}"]')`, 'the old message to be off screen', 6000);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 2`, 'the Saved tab', 6000);
        await ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(li => li.querySelector('.snip').textContent && li.dataset.id === "local:" + ${list.items[0].id}).querySelector('.saved-open').click(); true`);
        await waitFor(`!!${C}.querySelector('[data-msg-key="${hex(OLD)}"].chat-search-target')`, 'the jump to the old message', 12000);
        const at = await ev(`(() => { const t = ${C}.querySelector('[data-msg-key="${hex(OLD)}"]'); const r = t.getBoundingClientRect(), c = ${C}.getBoundingClientRect(); return { focused: document.activeElement === t, visible: r.top >= c.top - 1 && r.bottom <= c.bottom + 1 }; })()`);
        assert.ok(at.focused, 'the focus is on the message'); assert.ok(at.visible, 'the message is in view');
        await waitFor(`!!document.getElementById('saved-jumpbar') && !!${C}.querySelector('[data-msg-key="${hex(OLD)}"].saved-jump')`, 'the jump bar and the Saved tag on the message', 6000);
        assert.equal(await ev(`document.querySelector('#saved-jumpbar .btn').textContent`), 'Back to Saved', 'a way back to the list');
        assert.equal(await ev(TAB_OPEN), true, 'the Saved tab stays open beside the conversation after the jump');
        assert.equal(await ev(`document.querySelectorAll('#saved-list .saved-item').length`), 2, 'with its list');
        await shot('saved-jump-desktop');
        await ev(`document.querySelector('#saved-jumpbar .btn').click(); true`);
        await waitFor(`${TAB_OPEN} && !document.getElementById('saved-jumpbar')`, 'Back to Saved to bring the Saved tab back', 6000);
        console.log('   ✓');

        console.log('6. A message that is gone: said so on the item, the snippet kept, Remove removes it');
        const gone = await api('POST', '/api/saved', { kind: 'agent', ref: list.items[0].ref, key: hex(9999), agent_type: 'claude', title: 'sv-a', snippet: 'A message the transcript no longer has', said_at: '2026-10-17T08:00:00Z' });
        assert.equal(gone.status, 200);
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 3`, 'three items', 6000);
        await waitFor(`!!document.querySelector('#saved-list .saved-item.gone .why:not([hidden])')`, 'the gone message to be found when the list opens', 8000);
        const g = await ev(`(() => { const li = document.querySelector('#saved-list .saved-item.gone'); return { text: li.querySelector('.why').textContent, chip: li.querySelector('.kind').textContent, actions: [...li.querySelectorAll('.acts .btn')].map(b => b.lastChild.textContent.trim()), snippet: li.querySelector('.snip').textContent, open: document.getElementById('agentic-panel-saved').classList.contains('active') }; })()`);
        assert.match(g.text, /no longer has this message|transcript is gone/); assert.equal(g.chip, 'Gone'); assert.deepEqual(g.actions, ['Remove'], 'a gone item can only be removed'); assert.match(g.snippet, /no longer has/); assert.ok(g.open, 'the Saved tab stays open');
        // #5802: the list's own answer marks the gone ones, so opening the tab asks nothing per bookmark (QA's #5796: it was one request per row, up to 200)
        const seen = await ev(`(async () => { const prev = window.fetch; const urls = []; window.fetch = (u, o) => { urls.push(String(u)); return prev(u, o); }; try { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); await new Promise(r => setTimeout(r, 800)); } finally { window.fetch = prev; } return urls; })()`); // sleep-ok: a negative check: for 800 ms after the list opens it must make no further request, so the wait IS the measurement
        assert.equal(seen.filter(u => /\/api\/saved(\?|$)/.test(u)).length, 1, 'the list is read once: ' + JSON.stringify(seen));
        assert.deepEqual(seen.filter(u => /messages\/all\?|find_key=|\/api\/sessions\/history\//.test(u)), [], 'no request per bookmark');
        assert.ok(await ev(`!!document.querySelector('#saved-list .saved-item.gone .why:not([hidden])')`), 'the gone one is still marked');
        await shot('saved-gone-desktop');
        await ev(`document.querySelector('#saved-list .saved-item.gone .saved-remove').click(); true`);
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 2`, 'the gone item removed', 6000);
        console.log('   ✓');

        console.log('7. The tab works with the keyboard: Enter on its tab shows it; the top bar button opens the Inbox with the focus inside; the link in the toast opens the sidebar on it');
        await ev(`document.getElementById('agentic-tab-files').click(); true`);
        await waitFor(`!document.getElementById('agentic-panel-saved').classList.contains('active')`, 'another tab to be shown', 4000);
        await ev(`document.getElementById('agentic-tab-saved').focus(); true`);
        await key('Enter', 'Enter', 13);
        await waitFor(TAB_OPEN, 'Enter on the Saved tab to show it', 6000, `({ active: document.activeElement && (document.activeElement.id || document.activeElement.tagName), tabCls: document.getElementById('agentic-tab-saved').className, tabDisp: getComputedStyle(document.getElementById('agentic-tab-saved')).display, panelCls: document.getElementById('agentic-panel-saved').className, collapsed: document.getElementById('agentic-state').className })`);
        assert.equal(await ev(`document.activeElement.id`), 'agentic-tab-saved', 'the focus stays on the tab, as with every tab');
        assert.equal(await ev(`document.getElementById('agentic-tab-saved').getAttribute('aria-label')`), 'Saved for later on this page'); // not the top bar button's name: that one opens everything's list
        await ev(`document.getElementById('agentic-tab-files').click(); true`);
        // The top bar's bookmark is the Inbox's, from an agent's page too (#4905): Enter on it opens the page with the focus inside, and pressing it again goes back to the agent.
        await ev(`document.getElementById('saved-btn').focus(); true`);
        await key('Enter', 'Enter', 13);
        await waitFor(`document.getElementById('saved-view').getClientRects().length > 0 && document.getElementById('saved-view').contains(document.activeElement)`, 'the Inbox at Saved with the focus inside it', 6000);
        await ev(`document.getElementById('saved-btn').click(); true`); // pressed again: back to the agent
        await waitFor(`document.getElementById('saved-view').getClientRects().length === 0`, 'the Inbox left', 6000);
        await ev(`document.getElementById('agentic-state').classList.add('collapsed'); true`); // the sidebar closed
        await ev(`document.getElementById('agentic-tab-saved').classList.add('tab-hidden'); true`); // and the user hid the Saved tab: the toast's link and Back to Saved still open it
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`);
        await waitFor(`${TAB_OPEN} && document.getElementById('agentic-panel-saved').contains(document.activeElement)`, 'the sidebar open on Saved with the focus inside it', 6000);
        assert.equal(await ev(`document.querySelector('#agentic-tab-saved .agentic-tab-count').textContent`), '2', 'the count is on the tab too');
        console.log('   ✓');

        console.log('7b. A board message: saved from its row, listed as a Board item, the jump opens the board at the message, a deleted one is gone');
        BOARD = 'saved-4510-board';
        await api('POST', `/api/board/${BOARD}/subscribe`, { subscriber_id: 'Saver', job_title: 'Saver' });
        for (const c of ['first board message', 'the one to save', 'the one to delete']) { const r = await api('POST', `/api/board/${BOARD}/messages`, { subscriber_id: 'Saver', content: c }); assert.equal(r.status, 200, 'post: ' + JSON.stringify(r)); }
        const got = await api('GET', `/api/board/${BOARD}/messages/all?limit=10`);
        assert.equal(got.status, 200, JSON.stringify(got));
        const bm = got.body.messages || got.body;
        const keep = bm.find(m => m.content === 'the one to save'), drop = bm.find(m => m.content === 'the one to delete');
        assert.ok(keep && drop, 'the board has its messages');
        await ev(`window.selectBoardProject(${JSON.stringify(BOARD)}); true`);
        await waitFor(`document.querySelectorAll('#mb-messages .mb-message').length === 3`, 'the board rows', 8000);
        const bb = await ev(`(() => { const b = document.querySelector('#mb-messages [data-message-id="${keep.id}"] .mb-save-btn'); return b ? { label: b.getAttribute('aria-label'), pressed: b.getAttribute('aria-pressed'), hidden: b.hidden } : null; })()`);
        assert.deepEqual({ ...bb, label: bb.label.split(':')[0] }, { label: 'Save for later', pressed: 'false', hidden: false });
        for (const id of [keep.id, drop.id]) {
            await ev(`document.querySelector('#mb-messages [data-message-id="${id}"] .mb-save-btn').click(); true`);
            await waitFor(`document.querySelector('#mb-messages [data-message-id="${id}"] .mb-save-btn').getAttribute('aria-pressed') === 'true'`, 'the board message saved', 6000);
        }
        const bl = await saved('?kind=board&conv=' + BOARD);
        assert.equal(bl.items.length, 2); assert.equal(bl.items.find(i => i.key === String(keep.id)).snippet, 'the one to save');
        assert.equal((await api('DELETE', `/api/board/${BOARD}/messages/${drop.id}`)).status, 200, 'the message is deleted from the board');
        await select('sv-a'); // away from the board
        await ev(`window.selectLiveSession('', '', '', 'local'); true`); // and home: the list is of the agent or team on screen (#4792), and this one reads all of them
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`[...document.querySelectorAll('#saved-list .src')].filter(e => e.textContent.toLowerCase().includes('saved-4510-board')).length === 2`, 'the board items', 6000, `({ n: document.querySelectorAll('#saved-list .saved-item').length, srcs: [...document.querySelectorAll('#saved-list .src')].map(e => e.textContent) })`);
        await waitFor(`!!document.querySelector('#saved-list .saved-item.gone .why:not([hidden])')`, 'the deleted board message to be found gone', 8000);
        assert.match(await ev(`document.querySelector('#saved-list .saved-item.gone .why').textContent`), /deleted from the board, or the board was/);
        await ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(li => li.textContent.includes('the one to save')).querySelector('.saved-open').click(); true`);
        await waitFor(`!!document.querySelector('#mb-messages [data-message-id="${keep.id}"].chat-search-target')`, 'the jump into the board', 10000, `({ view: getComputedStyle(document.getElementById('messageboard-view')).display, rows: document.querySelectorAll('#mb-messages .mb-message').length, targets: document.querySelectorAll('.chat-search-target').length, tip: document.getElementById('saved-status').textContent, gone: [...document.querySelectorAll('.why:not([hidden])')].map(e => e.textContent) })`);
        assert.equal(await ev(`document.activeElement === document.querySelector('#mb-messages [data-message-id="${keep.id}"]')`), true, 'the focus is on the message');
        await waitFor(`!!document.getElementById('saved-jumpbar') && !!document.querySelector('#mb-messages .saved-jump')`, 'the jump bar and the Saved tag', 6000);
        assert.equal(await ev(`document.querySelector('#saved-jumpbar .btn').textContent`), 'Back to Saved');
        await shot('saved-board-jump');
        for (const it of (await saved()).items) await api('DELETE', `/api/saved/${it.id}`);
        await ev(`window.showMessageBoardProjects && window.showMessageBoardProjects(); true`);
        await select('sv-a');
        // two items again for the phone
        await ev(`(async () => { for (const k of [${JSON.stringify(hex(3))}, ${JSON.stringify(hex(4))}]) await fetch('/api/saved', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'agent', ref: 'sv-a-id', key: k, agent_type: 'claude', title: 'sv-a', snippet: 'a saved message', said_at: '2026-10-17T08:00:00Z' }) }); })()`);
        console.log('   ✓');

        console.log('7c. An agent that has ended: the saved item opens it in History at the message; a conversation with no transcript is gone');
        const ENDED = 'dead0000-0000-4000-8000-000000000001', MISSING = 'dead0000-0000-4000-8000-000000000002';
        await ev(`(() => {
            const prev = window.fetch; const N = 150;
            window.fetch = async (url, opts = {}) => {
                const u = String(url); const m = u.match(/\\/api\\/sessions\\/history\\/([^/?]+)(\\?(.*))?$/);
                if (!m) return prev(url, opts);
                const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
                if (m[1] === ${JSON.stringify(MISSING)}) return json({ error: 'not found' }, 404);
                if (m[1] !== ${JSON.stringify(ENDED)}) return prev(url, opts);
                const p = new URLSearchParams(m[3] || '');
                const all = Array.from({ length: N }, (_, i) => ({ type: i % 2 ? 'assistant' : 'user', ...(i % 2 ? { text: 'ended message ' + i, tool_uses: [] } : { content: 'ended message ' + i }), timestamp: '2026-10-16T10:00:' + String(i % 60).padStart(2, '0') + 'Z', key: i.toString(16).padStart(20, '0') }));
                if (p.has('find_key')) { const idx = all.findIndex(x => x.key === p.get('find_key')); return json({ found: idx >= 0, index: Math.max(idx, 0), total: N }); }
                const limit = Number(p.get('limit') || N), offset = Number(p.get('offset') || 0);
                const end = Math.max(N - offset, 0), start = Math.max(end - limit, 0);
                return json({ session_id: ${JSON.stringify(ENDED)}, agent_type: 'claude', messages: all.slice(start, end), total: N, start });
            };
        })()`);
        const e1 = await api('POST', '/api/saved', { kind: 'agent', ref: ENDED, key: hex(20), agent_type: 'claude', title: 'ended agent', snippet: 'ended message 20', said_at: '2026-10-16T10:00:20Z' });
        const e2 = await api('POST', '/api/saved', { kind: 'agent', ref: MISSING, key: hex(1), agent_type: 'claude', title: 'no transcript', snippet: 'a message of a transcript that is gone', said_at: '2026-10-16T10:00:01Z' });
        assert.equal(e1.status, 200); assert.equal(e2.status, 200);
        await ev(`window.selectLiveSession('', '', '', 'local'); true`); // home: the list is of the agent or team on screen (#4792), and these are three agents'
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`document.querySelectorAll('#saved-list .saved-item').length === 4 && document.getElementById('saved-list').textContent.includes('transcript that is gone')`, 'the four items', 6000);
        await waitFor(`!!document.querySelector('#saved-list .saved-item.gone .why:not([hidden])')`, 'the missing transcript to be found gone', 8000);
        assert.match(await ev(`document.querySelector('#saved-list .saved-item.gone .why').textContent`), /transcript is gone/);
        await waitFor(`!!document.querySelector('#saved-list .saved-item .kind.ended')`, 'the ended agent chip', 8000);
        assert.equal(await ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(li => li.textContent.includes('ended message 20')).querySelector('.saved-open').lastChild.textContent.trim()`), 'Open in Chats');
        await ev(`[...document.querySelectorAll('#saved-list .saved-item')].find(li => li.textContent.includes('ended message 20')).querySelector('.saved-open').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('history-session-view')).display !== 'none'`, 'History to open', 8000);
        await waitFor(`!!document.querySelector('#history-messages [data-msg-key="${hex(20)}"]')`, 'the message in History', 8000);
        await waitFor(`!!document.querySelector('#history-messages .chat-search-target')`, 'the message to be marked', 8000);
        const hb = await ev(`(() => { const t = document.querySelector('#history-messages .chat-search-target'); const b = document.querySelector('#history-messages [data-msg-key="${hex(20)}"] .chat-save-btn'); return { text: t.textContent.trim().slice(0, 20), saveHidden: b ? b.hidden : null, pressed: b ? b.getAttribute('aria-pressed') : null }; })()`);
        assert.match(hb.text, /ended message 20/); assert.equal(hb.saveHidden, false, 'History messages have the button too');
        await waitFor(`document.querySelector('#history-messages [data-msg-key="${hex(20)}"] .chat-save-btn')?.getAttribute('aria-pressed') === 'true'`, 'the saved state in History', 6000, `({ btn: (() => { const b = document.querySelector('#history-messages [data-msg-key="${hex(20)}"] .chat-save-btn'); return b ? { pressed: b.getAttribute('aria-pressed'), hidden: b.hidden, cls: b.className } : null; })(), cur: T.state.currentSession && { type: T.state.currentSession.type, name: T.state.currentSession.name }, total: document.querySelectorAll('#history-messages .chat-save-btn').length })`);
        await waitFor(`(() => { const t = document.querySelector('#history-messages .chat-search-target'); const c = document.getElementById('history-messages'); if (!t) return false; const r = t.getBoundingClientRect(), cr = c.getBoundingClientRect(); return r.top >= cr.top - 1 && r.bottom <= cr.bottom + 1; })()`, 'the message to be scrolled into view in History', 8000);
        await shot('saved-history-jump');
        for (const it of (await saved()).items) await api('DELETE', `/api/saved/${it.id}`);
        await select('sv-a');
        await ev(`(async () => { for (const k of [${JSON.stringify(hex(3))}, ${JSON.stringify(hex(4))}]) await fetch('/api/saved', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kind: 'agent', ref: 'sv-a-id', key: k, agent_type: 'claude', title: 'sv-a', snippet: 'a saved message', said_at: '2026-10-17T08:00:00Z' }) }); })()`);
        console.log('   ✓');

        console.log('8. A phone (390 px): the side panel overlay opens on Saved, it fits, the controls are 44 px, nothing scrolls sideways');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        // The top bar keeps the Saved button on a phone, with its count.
        await ev(`document.dispatchEvent(new CustomEvent('coral:saved-changed')); true`); // the test changed the bookmarks behind the page's back
        await waitFor(`document.querySelector('#saved-btn .saved-count').textContent === '2'`, 'the count to follow the list', 6000);
        const tbtn = await ev(`(() => { const b = document.getElementById('saved-btn'); const r = b.getBoundingClientRect(); return { shown: getComputedStyle(b).display !== 'none' && r.width > 0, w: Math.round(r.width), h: Math.round(r.height), count: b.querySelector('.saved-count').textContent, name: b.getAttribute('aria-label'), page: document.documentElement.scrollWidth <= document.documentElement.clientWidth }; })()`);
        assert.ok(tbtn.shown && tbtn.w >= 44 && tbtn.h >= 44 && tbtn.page, 'the top bar button is there, 44 px, and the page does not scroll sideways: ' + JSON.stringify(tbtn));
        assert.equal(tbtn.count, '2'); assert.equal(tbtn.name, 'Saved for later, 2 open');
        await select('sv-a');
        await ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'saved.js'); await m.openSaved(); return true; })()`); // the page's own Saved tab; the top bar's bookmark is the Inbox's (#4905)
        await waitFor(`document.getElementById('agentic-state').classList.contains('mobile-panel-overlay') && ${TAB_OPEN} && document.querySelectorAll('#saved-list .saved-item').length === 2`, 'the side panel overlay open on Saved on the phone', 8000);
        const ph = await ev(`(() => { const vw = document.documentElement.clientWidth; const m = document.getElementById('agentic-state').getBoundingClientRect();
            const small = []; for (const e of document.querySelectorAll('#agentic-panel-saved button')) { const r = e.getBoundingClientRect(); if (r.width && (r.height < 43.5 || r.width < 43.5)) small.push((e.className || e.id) + ' ' + Math.round(r.width) + 'x' + Math.round(r.height)); }
            return { vw, left: Math.round(m.left), right: Math.round(m.right), page: document.documentElement.scrollWidth, small }; })()`);
        assert.ok(ph.left >= 0 && ph.right <= ph.vw && ph.page <= ph.vw, 'the panel fits the phone: ' + JSON.stringify(ph));
        assert.deepEqual(ph.small, [], 'every control is a 44 px target: ' + JSON.stringify(ph.small));
        await shot('saved-tab-phone');
        // Opening an item on a phone gives the overlay way, so the jump is seen.
        await ev(`document.querySelector('#saved-list .saved-open').click(); true`);
        await waitFor(`!document.getElementById('agentic-state').classList.contains('mobile-panel-overlay')`, 'the overlay to give way to the conversation', 12000);
        // The message button is a 44 px target on a phone too.
        await select('sv-a');
        await waitFor(`${C}.querySelectorAll('.chat-save-btn').length > 3`, 'the chat on the phone', 6000);
        const tb = await ev(`(() => { const b = ${C}.querySelector('.chat-bubble.assistant .chat-save-btn'); const r = b.getBoundingClientRect(); const cb = ${C}.querySelector('.chat-bubble.assistant .chat-copy-btn'); const c = cb.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), copyW: Math.round(c.width), copyH: Math.round(c.height), opacity: getComputedStyle(b).opacity, copyOpacity: getComputedStyle(cb).opacity, overlapsCopy: r.left < c.right - 1 && r.right > c.left + 1 && r.top < c.bottom - 1 && r.bottom > c.top + 1 }; })()`);
        assert.ok(tb.w >= 44 && tb.h >= 44, 'the Save button is 44 px on touch: ' + JSON.stringify(tb));
        assert.ok(tb.copyW >= 44 && tb.copyH >= 44, 'Copy is 44 px on touch too: ' + JSON.stringify(tb)); assert.equal(tb.opacity, '1', 'always visible on touch'); assert.equal(tb.copyOpacity, '1', 'Copy too');
        assert.equal(tb.overlapsCopy, false, 'it does not sit on Copy: ' + JSON.stringify(tb));
        // The 44 px Save and Copy sit in a gap of their own under a message, not over the next one (UX's phone finding).
        const gap = await ev(`(() => { const out = []; const bubbles = [...${C}.querySelectorAll('.chat-bubble')]; for (let i = 0; i < bubbles.length - 1; i++) { const b = bubbles[i], next = bubbles[i + 1]; if (!b.querySelector('.chat-save-btn')) continue;
            const nt = next.getBoundingClientRect().top; for (const btn of b.querySelectorAll('.chat-save-btn, .chat-copy-btn')) { const r = btn.getBoundingClientRect(); if (r.width && r.bottom > nt + 1) out.push(Math.round(r.bottom - nt)); } } return { over: out, checked: bubbles.length }; })()`);
        assert.ok(gap.checked > 4); assert.deepEqual(gap.over, [], 'no button hangs over the next message: ' + JSON.stringify(gap));
        await shot('saved-button-phone');
        console.log('9. A phone at 200% text: the Saved button gives way to a settings-menu entry (with its count), so the view selector keeps its name');
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        await waitFor(`document.body.classList.contains('saved-in-menu')`, 'the layout to move Saved into the menu', 6000);
        const big = await ev(`(() => { const sel = document.querySelector('.nav-select'); return { btn: getComputedStyle(document.getElementById('saved-btn')).display, sel: sel ? sel.textContent.trim().replace(/\\s+/g, ' ') : null, selW: sel ? Math.round(sel.getBoundingClientRect().width) : 0 }; })()`);
        assert.equal(big.btn, 'none', 'no top bar button at large text'); assert.ok(big.selW >= 100, 'the view selector keeps its room: ' + JSON.stringify(big));
        await ev(`document.querySelector('[data-on-click="m-toggle-top-bar-settings"]').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('top-bar-settings-menu')).display !== 'none'`, 'the settings menu', 4000);
        await waitFor(`document.querySelector('.saved-menu-item .saved-count').textContent === '2'`, 'the menu entry to show the count', 8000, `JSON.stringify({ menu: document.querySelector('.saved-menu-item .saved-count').textContent, bar: document.querySelector('#saved-btn .saved-count').textContent })`);
        const mi = await ev(`(() => { const e = document.querySelector('.saved-menu-item'); const r = e.getBoundingClientRect(); return { shown: getComputedStyle(e).display !== 'none' && r.width > 0, h: Math.round(r.height), count: e.querySelector('.saved-count').textContent, text: e.textContent.trim().replace(/\\s+/g, ' ') }; })()`);
        assert.ok(mi.shown && mi.h >= 43 && mi.count === '2' && /^Saved for later/.test(mi.text), 'the menu entry has the count and is a 44 px target: ' + JSON.stringify(mi));
        await ev(`document.querySelector('.saved-menu-item').click(); true`);
        await waitFor(`document.getElementById('saved-view').getClientRects().length > 0 && document.getElementById('saved-view').contains(document.getElementById('saved-ui'))`, 'the menu entry to open the Inbox at Saved (everything\'s, as the top bar button does)', 8000);
        await shot('saved-menu-phone-200');
        await ev(`document.documentElement.style.fontSize = ''; true`);
        await waitFor(`!document.body.classList.contains('saved-in-menu')`, 'the top bar button to come back at normal text', 6000);
        console.log('   ✓');

        await Emulation.setTouchEmulationEnabled({ enabled: false });
        await Emulation.clearDeviceMetricsOverride();
        console.log('\nAll Save for Later checks passed');
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {}); // the bookmarks this suite saved are taken off again whichever way it ended (#4916, #5083)
        await removeBoards(ev, [BOARD]);
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
