// The agent chat view on a phone for a person using the on-screen keyboard, touch or only the keyboard (#6727, the phone chat audit #6712 findings 1 to 3): an approval question's answer buttons stay on
// screen when the keyboard opens (the card scrolls its options into view; the card was left with only its title showing in a 216 px pane); every answer is a 44 px target ("Yes" was 37 px high, shorter than
// the longer options); and the per-message Copy buttons are one Tab stop with the arrow keys moving between them, each named for its message, not one same-named stop per bubble before the message box.
// Drives Chrome over CDP through agent_view_harness.js with its mocked agents (a chat of 20 messages) and the page's own needs-input card.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout and scroll settle after a resize
    const key = async (k, vk) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: vk }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: vk }); await sleep(150); }; // sleep-ok: the handler runs
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        for (const [w, h, tag] of [[390, 844, '390'], [360, 800, '360']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true });
            await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`);
            await ev(`window.__m.add('rep', 'R', 20, { sleeping: false }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await select('rep');
            await waitFor(`window.__rendered && window.__rendered().length >= 15`, `${tag}: the chat`, 15000);
            await sleep(600); // sleep-ok: the roving stop and the names are set on the frame after the chat is drawn

            // the per-message Copy buttons: one Tab stop, named for the message, arrows between them
            const copies = await ev(`(() => { const bs = [...document.querySelectorAll('#live-history-messages .chat-copy-btn')]; return { n: bs.length, tabbable: bs.filter(b => b.tabIndex >= 0).length, names: bs.map(b => b.getAttribute('aria-label')) }; })()`);
            assert.ok(copies.n >= 10, `${tag}: the chat has its Copy buttons (${copies.n})`);
            assert.equal(copies.tabbable, 1, `${tag}: the Copy buttons are one Tab stop, not ${copies.tabbable} (one per bubble before the message box)`);
            assert.equal(new Set(copies.names).size, copies.names.length, `${tag}: no two Copy buttons share a name (${JSON.stringify(copies.names.slice(0, 3))})`);
            assert.ok(copies.names.every(n => /^Copy message: \S/.test(n)), `${tag}: each says which message it copies (${JSON.stringify(copies.names.slice(0, 2))})`);
            await ev(`document.querySelector('#live-history-messages .chat-copy-btn[tabindex="0"]').focus(); true`);
            const stop = await ev(`[...document.querySelectorAll('#live-history-messages .chat-copy-btn')].indexOf(document.activeElement)`);
            await key('ArrowUp', 38);
            const up = await ev(`[...document.querySelectorAll('#live-history-messages .chat-copy-btn')].indexOf(document.activeElement)`);
            assert.equal(up, stop - 1, `${tag}: ArrowUp moves to the message before (${stop} to ${up})`);
            assert.equal(await ev(`document.querySelectorAll('#live-history-messages .chat-copy-btn[tabindex="0"]').length`), 1, `${tag}: and the one Tab stop goes with it`);
            await key('Home', 36); assert.equal(await ev(`[...document.querySelectorAll('#live-history-messages .chat-copy-btn')].indexOf(document.activeElement)`), 0, `${tag}: Home goes to the first`);
            await key('End', 35); assert.equal(await ev(`[...document.querySelectorAll('#live-history-messages .chat-copy-btn')].indexOf(document.activeElement)`), copies.n - 1, `${tag}: End goes to the last`);

            // the group is named: its Tab stop is described by a line that mentions the arrow keys
            const desc = await ev(`(() => { const b = document.querySelector('#live-history-messages .chat-copy-btn[tabindex="0"]'); const id = b && b.getAttribute('aria-describedby'); return id ? (document.getElementById(id) || {}).textContent || '' : ''; })()`);
            assert.ok(/arrow keys/i.test(desc), `${tag}: the Tab stop is described by a line that mentions the arrow keys (${JSON.stringify(desc)})`);
            // a streaming chat does no work for a change that adds no button, and little for one that does
            const marks = await ev(`(async () => { const c = document.getElementById('live-history-messages'); let n = 0; const mo = new MutationObserver(r => { n += r.filter(x => x.type === 'attributes').length; }); mo.observe(c, { attributes: true, attributeFilter: ['aria-label', 'tabindex', 'aria-describedby'], subtree: true });
                const t = c.querySelector('.chat-bubble.assistant .message-text'); t.append(document.createTextNode(' and more words arrive'));
                await new Promise(r => setTimeout(r, 250)); /* sleep-ok: counting writes over a fixed quiet window */ const quiet = n; n = 0;
                const b = document.createElement('div'); b.className = 'chat-bubble assistant'; b.innerHTML = '<div class="message-text">A new reply</div><button type="button" class="chat-copy-btn" aria-label="Copy message"></button>'; c.append(b);
                await new Promise(r => setTimeout(r, 250)); /* sleep-ok: the observer batches in a frame */ const added = n; mo.disconnect(); b.remove(); return { quiet, added }; })()`);
            assert.equal(marks.quiet, 0, `${tag}: a change that adds no button writes nothing (${marks.quiet} attribute writes)`);
            assert.ok(marks.added > 0 && marks.added <= 6, `${tag}: a new message's button costs a few writes, not one per bubble (${marks.added})`);
            // an approval question, as the stand-in terminal reports it (kept by the chat's own polls)
            await ev(`(() => { const inner = window.__innerFetch || (window.__innerFetch = window.fetch); window.fetch = (url, opts = {}) => { const u = String(url); if (/\\/api\\/sessions\\/live\\/[^/?]+\\/chat\\?/.test(u)) return inner(url, opts).then(async r => { const d = await r.clone().json().catch(() => null); if (!d) return r; d.prompt_options = { title: 'Bash command', question: 'Allow Bash: npm test -- --ci?', options: [{ n: 1, label: 'Yes', selected: true, action: 'select' }, { n: 2, label: "Yes, and don't ask again for npm test commands in this folder", action: 'select' }, { n: 3, label: 'No, and tell Claude what to do differently', action: 'select' }] }; d.pending = { tool_name: 'Bash', input: { command: 'npm test -- --ci' } }; return new Response(JSON.stringify(d), { status: r.status, headers: { 'content-type': 'application/json' } }); }); return inner(url, opts); };
                const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), waiting_for_input: true, waiting_kind: 'permission', waiting_summary: 'Claude needs your permission to use Bash: npm test -- --ci' }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); return true; })()`);
            await waitFor(`!!document.querySelector('.chat-needs-input .cni-answer')`, `${tag}: the approval card`, 20000);
            await sleep(800); // sleep-ok: the card settles in its pane
            const answers = () => ev(`(() => { const pane = document.getElementById('live-history-messages').getBoundingClientRect(); const bs = [...document.querySelectorAll('.chat-needs-input .cni-answer')].map(b => { const r = b.getBoundingClientRect(); return { n: b.textContent.replace(/\\s+/g, ' ').trim().slice(0, 14), h: Math.round(r.height), inside: r.top >= pane.top - 1 && r.bottom <= pane.bottom + 1 }; }); return { pane: Math.round(pane.height), bs }; })()`);
            let a = await answers();
            assert.ok(a.bs.length === 3 && a.bs.every(b => b.h >= 44), `${tag}: every answer is a 44 px target (${JSON.stringify(a.bs.map(b => b.n + ' ' + b.h))})`);
            assert.ok(a.bs.every(b => b.inside), `${tag}: with the keyboard closed every answer is inside the chat pane (${JSON.stringify(a)})`);
            // the keyboard opens: the viewport loses 45% of its height
            await Emulation.setDeviceMetricsOverride({ width: w, height: Math.round(h * 0.55), deviceScaleFactor: 2, mobile: true });
            await sleep(900); // sleep-ok: the card scrolls its answers into view after the resize (120 ms debounce)
            a = await answers();
            assert.ok(a.bs.every(b => b.inside), `${tag}: with the keyboard open every answer is still inside the chat pane (${JSON.stringify(a)})`);
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile: true });
            console.log(`   ✓ ${tag}`);
        }
        console.log('PASS the phone chat: the approval answers stay on screen with the keyboard open and are 44 px, the Copy buttons are one Tab stop named for their message');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
