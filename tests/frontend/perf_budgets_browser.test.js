// Dashboard speed budgets (#4985): the work a keystroke, a scroll, a feed message and a chat page are not allowed to repeat. Counted, not timed, so they hold on any machine:
//  - a keystroke in the composer, a scroll of the chat, and a render of the agent list make NO selector call over the whole document (they walked every node of the page and of a long chat
//    each time: the chat's file-menu check on every key and every scroll of anything, the @file list asking whether the /command list was open, the Display menu looking up its boxes);
//  - a fenced block of a known language is coloured after DOMPurify has seen its text, so the sanitizer visits the few nodes of the plain block, not every coloured span;
//  - and the HTML that comes out is the same (markdown_render_golden_browser pins that).
// Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const { framesJs } = require('./helpers/wait');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        // count the scheduler.yield calls the startup makes (Chrome has it; the page falls back to a zero timer where it does not)
        const SPY = `;(() => { window.__yields = 0; if (globalThis.scheduler && typeof scheduler.yield === 'function') { const y = scheduler.yield.bind(scheduler); scheduler.yield = (...a) => { if (!window.__startupDone) window.__yields++; return y(...a); }; }
            })();`;
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD + SPY + `;localStorage.setItem('coral-live-view-mode', 'chat');` });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        // a chat with a few hundred messages on screen, so the page has something to walk
        await ev(`(() => { const a = window.__m.add('perf-a', 'P', 0, { sleeping: false, session_id: 'perf-a-id' }); const m = []; for (let i = 0; i < 400; i++) { m.push({ type: 'user', content: 'question ' + i, key: 'u' + i }); m.push({ type: 'assistant', text: 'answer ' + i + ' with **bold** and \`code\`', key: 'a' + i }); } a.msgs = m;
            window.selectLiveSession('perf-a', 'claude', 'perf-a-id', 'local'); return true; })()`);
        await waitFor(`!!document.getElementById('command-input') && !document.getElementById('command-input').disabled && document.querySelectorAll('#live-history-messages .chat-bubble').length > 20`, 'the chat and its composer', 10000);
        // count the selector calls made on the document itself (an element's own, narrow lookups are fine)
        await ev(`(() => { window.__docSel = []; for (const k of ['querySelector', 'querySelectorAll']) { const orig = Document.prototype[k]; Document.prototype[k] = function (s) { window.__docSel.push(String(s)); return orig.apply(this, arguments); }; } return true; })()`);
        const taken = () => ev(`window.__docSel.splice(0)`);
        
        console.log('1. Typing: no selector over the whole document per keystroke');
        await ev(`document.getElementById('command-input').focus(); true`); await taken();
        for (const ch of 'hello there, a few words to type') { await Input.dispatchKeyEvent({ type: 'keyDown', key: ch, text: ch }); await Input.dispatchKeyEvent({ type: 'keyUp', key: ch }); }
        const typed = await taken();
        const walkers = typed.filter(s => !/^#[\w-]+$/.test(s));
        assert.ok(walkers.length <= 32, `at most one page-wide lookup per keystroke (32 keys): ${walkers.length}: ${[...new Set(walkers)].join(' | ')}`);
        assert.ok(!typed.some(s => s.includes('chat-file-menu') || s.includes('command-mention-dropdown')), 'neither the file menu nor the /command list is looked up by selector: ' + [...new Set(typed)].join(' | '));

        console.log('2. Scrolling the chat: no selector over the whole document per scroll event');
        await taken();
        await ev(`(async () => { const c = document.getElementById('live-history-messages'); for (let i = 0; i < 20; i++) { c.scrollTop = c.scrollTop + (i % 2 ? 300 : -250); c.dispatchEvent(new Event('scroll')); document.dispatchEvent(new Event('scroll')); await ${framesJs(1)}; } return true; })()`);
        const scrolled = await taken();
        assert.ok(!scrolled.some(s => s.includes('chat-file-menu')), 'a scroll does not look for the file menu in every node: ' + [...new Set(scrolled)].join(' | '));

        console.log('3. A render of the agent list does not look up the Display menu, its summary line or the group toggle over the whole document');
        await taken();
        await ev(`(async () => { const T = window.T; for (let i = 0; i < 10; i++) { const e = window.__m.entry(window.__m.agents['perf-a-id']); e.summary = 'step ' + i; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [e], removed: [] }); await new Promise(r => setTimeout(r, 30)); } return true; })()`);
        await waitFor(`true`, 'a frame');
        const rendered = await taken();
        assert.ok(!rendered.some(s => /data-(display-(input|row|button|panel)|display-badge|group-toggle)|session-group-header/.test(s)), 'a render looks up the Display menu, its summary line and the group toggle inside the headers and the list, never over the whole document: ' + [...new Set(rendered)].join(' | '));

        console.log('4. Markdown: a coloured block is not walked span by span by the sanitizer');
        const m = await ev(`(async () => {
            const st = window.__coralStatic || '/static/'; const v = await import(st + 'vendor_loader.js'); await v.loadHighlight(); const u = await import(st + 'utils.js');
            let visited = 0; DOMPurify.addHook('uponSanitizeElement', () => { visited++; });
            const code = Array.from({ length: 40 }, (_, i) => '\\tif err := step' + i + '(ctx, "name", ' + i + '); err != nil { return fmt.Errorf("step ' + i + ': %w", err) }').join('\\n');
            const html = u.renderMarkdown('Fix:\\n\\n\`\`\`go\\nfunc run(ctx context.Context) error {\\n' + code + '\\n}\\n\`\`\`\\n\\ndone ' + Math.random(), undefined, 'chat');
            DOMPurify.removeHook('uponSanitizeElement');
            const box = document.createElement('div'); box.innerHTML = html;
            return { visited, spans: box.querySelectorAll('span').length, coloured: box.querySelectorAll('code.hljs').length };
        })()`);
        assert.equal(m.coloured, 1, 'the block is coloured'); assert.ok(m.spans > 100, 'with many spans: ' + m.spans);
        assert.ok(m.visited < 30, `the sanitizer visits the plain block's few nodes, not its ${m.spans} spans: ${m.visited}`);
        console.log('5. An agent switch touches the two rows whose highlight changes and draws no other');
        await ev(`(() => { const b = window.__m.add('perf-b', 'Q', 0, { sleeping: false, session_id: 'perf-b-id' }); b.msgs = [{ type: 'user', content: 'hi', key: 'b1' }, { type: 'assistant', text: 'hello', key: 'b2' }];
            window.T.ws.handleCoralMessage({ type: 'coral_update', servers: [], sessions: Object.values(window.__m.agents).map(x => window.__m.entry(x)) }); return true; })()`);
        await waitFor(`!!document.querySelector('#live-sessions-list li[data-session-id="perf-b-id"]')`, 'the second agent in the list');
        await ev(`window.selectLiveSession('perf-a', 'claude', 'perf-a-id', 'local'); true`);
        await waitFor(`document.querySelector('#live-sessions-list li.active')?.dataset.sessionId === 'perf-a-id'`, 'the first agent highlighted');
        await waitFor(`!!document.querySelector('#live-history-messages .chat-bubble')`, 'its chat');
        const sw = await ev(`(async () => { const list = document.getElementById('live-sessions-list'); const touched = new Set(); let childList = 0;
            const mo = new MutationObserver(rs => { for (const r of rs) { if (r.type === 'childList') childList++; else { const li = r.target.closest && r.target.closest('li'); if (li) touched.add(li.dataset.sessionId + ':' + r.attributeName); } } });
            mo.observe(list, { attributes: true, childList: true, subtree: true });
            window.selectLiveSession('perf-b', 'claude', 'perf-b-id', 'local');
            await new Promise(r => window.addEventListener('coral:agent-view-ready', e => { if (e.detail.name === 'perf-b') r(); }));
            await ${framesJs(3)}; mo.takeRecords().length; mo.disconnect();
            return { rows: [...new Set([...touched].map(x => x.split(':')[0]))].sort(), attrs: [...touched].sort(), childList, active: [...document.querySelectorAll('#live-sessions-list li.active')].map(li => li.dataset.sessionId) }; })()`);
        assert.deepEqual(sw.rows, ['perf-a-id', 'perf-b-id'], 'only the row that was active and the row that is now: ' + JSON.stringify(sw));
        assert.equal(sw.childList, 0, 'no row was replaced or added: ' + JSON.stringify(sw));
        assert.deepEqual(sw.active, ['perf-b-id'], 'the new agent is the highlighted one, and the only one');
        assert.equal(await ev(`document.querySelector('#live-sessions-list li.active').getAttribute('aria-current')`), 'true', 'and says so to a screen reader');
        assert.equal(await ev(`document.querySelectorAll('#live-sessions-list li[aria-current]').length`), 1, 'no other row says so');
        console.log('6. Startup: the page yields to the browser between creating the requests and wiring the rest, so no startup task is a long one (#4995)');
        const y = await ev(`({ yields: window.__yields, drawn: document.getElementById('agent-list-bar').childElementCount })`);
        assert.ok(y.yields >= 1, 'the startup handler called scheduler.yield (the only yield it makes; the zero timers the page sets for other things do not count): ' + JSON.stringify(y));
        console.log('7. A feed message writes only what changed: nothing at all when no drawn field did, one row and the header text when the viewed agent\'s summary did (#5010)');
        // The harness has no terminal attached, so with "Fit pane width" on (the page's default while the setting was never saved) syncPaneWidth measures a hidden span on every feed message: a real write, but not the feed's (a terminal answers its own column count). The setting is pinned off for this page so the budget is the same on a fresh server as after another suite saved it.
        await ev(`window.T.state.settings.fit_pane_width = false; true`);
        const feed = fields => ev(`(async () => { const T = window.T; const e = window.__m.entry(window.__m.agents['perf-a-id']); Object.assign(e, ${JSON.stringify(fields)});
            const seen = []; const mo = new MutationObserver(rs => { for (const r of rs) seen.push({ type: r.type, inList: !!r.target.closest?.('#live-sessions-list') || r.target.id === 'live-sessions-list', target: (r.target.id || r.target.className || r.target.nodeName) + '' }); });
            mo.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true });
            T.ws.handleCoralMessage({ type: 'coral_diff', changed: [e], removed: [] }); await ${framesJs(3)}; seen.push(...mo.takeRecords().map(r => ({ type: r.type, inList: false, target: 'late' }))); mo.disconnect(); return seen; })()`);
        await feed({ summary: 'budget settled' }); // the first message may still be settling what the last one changed
        const quiet = await feed({ staleness_seconds: 7 });
        assert.deepEqual(quiet, [], 'a message that changes nothing drawn writes nothing to the page: ' + JSON.stringify(quiet));
        const one = await feed({ summary: 'budget step ' + Math.random() });
        assert.ok(one.filter(r => r.inList && r.type === 'childList').length <= 2, 'one row is replaced, not the list: ' + JSON.stringify(one));
        assert.ok(one.length <= 8, 'a changed summary is a handful of writes (its row, its rail tile, the header text), not a rewrite of the header: ' + one.length + ' ' + JSON.stringify(one));
        console.log('PASS perf budgets: no page-wide lookups per key, scroll or render; the sanitizer skips coloured spans; a switch touches two rows');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
