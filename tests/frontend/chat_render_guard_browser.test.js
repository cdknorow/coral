// #3694: one chat message that can't be drawn no longer costs the rest of the history. The
// messages around it render as usual and it gets a note in its place, in the live chat and in the
// history Chat tab alike (both draw through renderMessage).
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { coralReady } = require('./helpers/wait').bind(ev);
    const MOD = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        const r = await ev(`import(${MOD} + 'live_chat.js').then(m => {
            const box = document.createElement('div'); document.body.appendChild(box);
            // A message whose text throws when it is read: whatever breaks a renderer, the same path.
            const bad = { type: 'assistant', get text() { throw new Error('qa: unreadable'); } };
            let threw = null;
            try {
                m.renderTranscript([
                    { type: 'user', content: 'first message' },
                    bad,
                    { type: 'assistant', text: 'third message <img src=x onerror=window.__qaXSS=1>' },
                ], box, 'claude');
            } catch (e) { threw = String(e); }
            const out = { threw, text: box.textContent, notes: box.querySelectorAll('.chat-message-unrendered').length,
                img: box.querySelectorAll('img[onerror]').length, xss: !!window.__qaXSS };
            box.remove();
            return out;
        })`);
        check('a message that can’t be drawn doesn’t stop the rest', r.threw === null && r.text.includes('first message') && r.text.includes('third message'), JSON.stringify(r));
        check('it gets a note in its place', r.notes === 1 && r.text.includes('This message could not be shown.'), JSON.stringify(r));
        check('the messages around it are still escaped (no handler from message text)', r.img === 0 && !r.xss, JSON.stringify(r));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
