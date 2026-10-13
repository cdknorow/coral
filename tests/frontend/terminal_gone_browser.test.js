// When an agent's own terminal is gone the server answers 409 terminal_gone and
// sends nothing anywhere (#3211: it used to fall back to another agent's pane).
// The composer, the keys and the prompt bar's answer buttons must say "The
// agent's terminal is gone", keep the draft, and never report it as sent.
// Covered for the server's own body and for a body carrying only the code.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const MESSAGE = "The agent's terminal is gone";

(async () => {
    const { client, Page, ev, waitFor, PRELOAD, install, select, firstPage } = await connect();

    // /send, /keys and /answer-prompt answer 409 terminal_gone; everything else goes to the harness mock.
    const goneFetch = body => ev(`(() => {
        const inner = window.__innerFetch || (window.__innerFetch = window.fetch);
        window.__goneHits = [];
        window.fetch = (url, opts = {}) => {
            const u = String(url);
            const m = u.match(/\\/api\\/sessions\\/live\\/[^/?]+\\/(send|keys|answer-prompt)(?:\\?|$)/);
            if (!m) return inner(url, opts);
            window.__goneHits.push(m[1]);
            return Promise.resolve(new Response(JSON.stringify(${JSON.stringify(body)}), { status: 409, headers: { 'content-type': 'application/json' } }));
        };
    })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
    const clearToasts = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove())`);
    const lastToast = async what => {
        await waitFor(`document.querySelectorAll('.toast .toast-message').length > 0`, `a toast for ${what}`);
        const all = await toasts();
        return all[all.length - 1];
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(`window.__m.add('qa-gone', 'G', 5, { sleeping: false, board_project: 'team-gone' })`);
        await select('qa-gone');
        await firstPage();

        const bodies = [
            ['server body', { error: MESSAGE, code: 'terminal_gone', not_sent: true }],
            ['code only', { code: 'terminal_gone' }],
        ];
        for (const [label, body] of bodies) {
            await goneFetch(body);

            // Composer: the draft stays, the toast names the gone terminal, nothing says "Sent".
            await clearToasts();
            await ev(`(() => { const i = document.getElementById('command-input'); i.value = 'hello gone'; i.dispatchEvent(new Event('input', { bubbles: true })); })()`);
            await ev(`window.sendCommand()`);
            assert.equal(await lastToast('the composer'), MESSAGE, `${label}: composer toast`);
            assert.equal(await ev(`document.getElementById('command-input').value`), 'hello gone', `${label}: the draft is kept`);
            assert.ok(!(await toasts()).some(t => /^Sent/.test(t)), `${label}: never reported as sent`);

            // Keys (Escape, and an unmapped key that always goes over HTTP).
            for (const keys of [['Escape'], ['C-c']]) {
                await clearToasts();
                await ev(`window.sendRawKeys(${JSON.stringify(keys)})`);
                assert.equal(await lastToast(`keys ${keys}`), MESSAGE, `${label}: keys ${keys} toast`);
                assert.ok(!(await toasts()).some(t => /^Sent/.test(t)), `${label}: keys never reported as sent`);
            }

            // Prompt bar: an answer button on the needs-input card.
            await clearToasts();
            await ev(`(() => {
                const c = document.getElementById('live-history-messages');
                c.querySelectorAll('.qa-gone-card').forEach(e => e.remove());
                const card = document.createElement('div');
                card.className = 'chat-needs-input qa-gone-card';
                card.innerHTML = '<button class="cni-answer" data-n="1" data-label="Yes" data-action="option">Yes</button>';
                c.appendChild(card);
                card.querySelector('.cni-answer').click();
            })()`);
            assert.equal(await lastToast('the prompt bar'), MESSAGE, `${label}: prompt bar toast`);
            await waitFor(`!document.querySelector('.qa-gone-card .cni-answer').disabled`, 'the answer button to re-enable');
            assert.equal(await ev(`document.querySelector('.qa-gone-card .cni-answer').classList.contains('sending')`), false, `${label}: the answer is not left sending`);

            const hits = await ev('window.__goneHits');
            for (const kind of ['send', 'keys', 'answer-prompt']) assert.ok(hits.includes(kind), `${label}: ${kind} was requested (${hits})`);
            console.log(`   ✓ ${label}: composer, keys and prompt bar say "${MESSAGE}" and keep the draft`);
        }
        console.log('\nAll terminal-gone checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
