// First-run audit: a brand-new agent's empty chat says what it is waiting for and what to do next, by the agent's state.
//  - idle: a title and a hint ("Type below to start."); a role=status block, sized in rem
//  - not started: the "Waiting in the terminal" card already says it, so no empty state shows under it (nothing to add)
//  - asleep: it says so and points at waking it
//  - the words are written once: a later refresh does not rewrite them (a screen reader is not told again)
//  - the first message replaces it
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
// EMPTY_CHAT_SHOT=<dir> also writes a screenshot of each case there (not needed to pass).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        const shot = async (name) => {
            const dir = process.env.EMPTY_CHAT_SHOT;
            if (!dir) return;
            const { data } = await Page.captureScreenshot({ format: 'png' });
            fs.writeFileSync(`${dir}/${name}.png`, Buffer.from(data, 'base64'));
        };
        const words = () => ev(`(() => { const e = document.querySelector('#live-history-messages > .chat-empty'); return e ? { role: e.getAttribute('role'), title: (e.querySelector('.chat-empty-title') || {}).textContent || '', hint: (e.querySelector('.chat-empty-hint') || {}).textContent || '', size: getComputedStyle(e).fontSize } : null; })()`);
        const open = async (name, patch) => {
            await ev(`window.__m.add('${name}', '${name}', 0, { sleeping: ${patch.sleeping ? 'true' : 'false'} }); true`);
            await ev(`window.selectLiveSession('${name}', 'claude', '${name}-id', 'local'); true`);
            await waitFor(`!!document.querySelector('#live-history-messages > .chat-empty')`, `the empty state of ${name}`, 8000);
        };

        console.log('1. An idle new agent: a title, and what to do next');
        await open('ec-idle', {});
        let w = await words();
        assert.equal(w.role, 'status');
        assert.equal(w.title, 'No messages yet');
        assert.equal(w.hint, 'Type below to start.'); // #5991: the keys and what appears are said by the box itself
        assert.equal(w.size, '14px', 'sized in rem: 0.875rem is 14px at the default size');
        await shot('idle');

        console.log('2. The words are written once, not at every refresh');
        await ev(`window.__ecMuts = 0; new MutationObserver(m => { window.__ecMuts += m.length; }).observe(document.querySelector('#live-history-messages > .chat-empty'), { childList: true, characterData: true, subtree: true }); true`);
        await ev(`window.refreshLiveHistory ? window.refreshLiveHistory() : null; true`);
        await new Promise(r => setTimeout(r, 2500)); // sleep-ok: two poll rounds with nothing to change; the check is that nothing is rewritten
        assert.equal(await ev(`window.__ecMuts`), 0, 'a refresh with the same state rewrites nothing');

        console.log('3. A sleeping agent says so');
        await open('ec-sleep', { sleeping: true });
        await waitFor(`(document.querySelector('#live-history-messages > .chat-empty .chat-empty-title') || {}).textContent === 'This agent is asleep'`, 'the asleep words', 8000);
        assert.match((await words()).hint, /Wake below/);
        await shot('asleep');

        console.log('4. At 200% text the words grow with the root size');
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        assert.equal((await words()).size, '28px');
        await ev(`document.documentElement.style.fontSize = ''; true`);

        console.log('5. The first message replaces the empty state');
        await ev(`window.__m.agents['ec-idle-id'].msgs.push({ type: 'user', content: 'hello' }); true`);
        await ev(`window.selectLiveSession('ec-idle', 'claude', 'ec-idle-id', 'local'); true`);
        await waitFor(`!document.querySelector('#live-history-messages > .chat-empty') && document.querySelectorAll('#live-history-messages .chat-bubble, #live-history-messages [class*=bubble]').length > 0`, 'the message in place of the empty state', 8000);
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
