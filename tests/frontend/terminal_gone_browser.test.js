// When an agent's own terminal is gone the server answers 409 terminal_gone and
// sends nothing anywhere (#3211). The dashboard (#3218):
//   - says it UNDER THE COMPOSER, "The agent's terminal is gone. Restart it to
//     continue.", with a Restart button, for the composer, the keys and the prompt
//     bar's answer buttons; never as a toast; the draft stays;
//   - marks the agent "Terminal gone" on its row and in the header at once, both
//     restart it; the mark goes when the feed shows the agent asleep;
//   - says any other send error under the composer too (no Restart), cleared by a
//     successful send and by selecting another agent.
// Covered for the server's own body and for a body carrying only the code, at
// desktop and phone width.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { waitStable } = require('./helpers/wait');

const BASE = require('./test_base').BASE;
const MESSAGE = "The agent's terminal is gone. Restart it to continue.";

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select, firstPage } = await connect();

    // /send, /keys and /answer-prompt answer `status` with `body`; everything else goes to the harness mock.
    const failWrites = (status, body) => ev(`(() => {
        const inner = window.__innerFetch || (window.__innerFetch = window.fetch);
        window.__writeHits = [];
        window.fetch = (url, opts = {}) => {
            const u = String(url);
            // The prompt bar's options (#3622: only a card Coral itself shows can answer) come with the chat reply (#3720).
            if (window.__promptScreen && /\\/api\\/sessions\\/live\\/[^/?]+\\/chat\\?/.test(u)) return inner(url, opts).then(async r => {
                const d = await r.clone().json().catch(() => null);
                if (!d) return r;
                d.prompt_options = window.__promptScreen;
                return new Response(JSON.stringify(d), { status: r.status, headers: { 'content-type': 'application/json' } });
            });
            const m = u.match(/\\/api\\/sessions\\/live\\/[^/?]+\\/(send|keys|answer-prompt)(?:\\?|$)/);
            if (!m) return inner(url, opts);
            window.__writeHits.push(m[1]);
            return Promise.resolve(new Response(JSON.stringify(${JSON.stringify(body)}), { status: ${status}, headers: { 'content-type': 'application/json' } }));
        };
    })()`);
    const restoreWrites = () => ev(`window.fetch = window.__innerFetch || window.fetch; true`);
    const errorBox = () => ev(`(() => { const b = document.getElementById('composer-error'); if (!b || b.hidden) return null;
        const r = b.getBoundingClientRect(), input = document.getElementById('command-input').getBoundingClientRect();
        const restart = b.querySelector('.composer-error-restart');
        return { text: b.querySelector('.composer-error-text').textContent, restart: !restart.hidden,
            below: r.top >= input.bottom - 1, inView: r.bottom <= innerHeight + 1 && r.top >= 0, role: b.querySelector('.composer-error-text').getAttribute('role'), boxRole: b.getAttribute('role') }; })()`); // the alert is on the message's own span (#6213): the box holds buttons too
    const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
    const clearToasts = () => ev(`document.querySelectorAll('.toast').forEach(t => t.remove())`);
    const rowChip = name => ev(`!!document.querySelector('.session-group-item[data-session-id="${name}-id"] .terminal-gone-chip')`);
    const headerBadge = () => ev(`(() => { const b = document.getElementById('terminal-gone-header'); return b ? b.textContent : null; })()`);
    const restartModalOpen = () => ev(`(() => { const m = document.getElementById('restart-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`);
    const closeRestartModal = () => ev(`window.hideRestartModal(); true`);
    const type = text => ev(`(() => { const i = document.getElementById('command-input'); i.value = ${JSON.stringify(text)}; i.dispatchEvent(new Event('input', { bubbles: true })); })()`);
    // The prompt bar: the agent waits on a permission prompt, so Coral shows its own card with the
    // terminal's options, and its Yes is clicked (a hand-made card no longer answers, #3622).
    const WAITING = { waiting_for_input: true, waiting_summary: 'Notification: Claude needs your permission to use Bash' };
    const SCREEN = { question: 'Allow Bash?', options: [{ n: 1, label: 'Yes', action: 'select' }, { n: 2, label: 'No', action: 'select' }] };
    const setWaiting = on => ev(`import((window.__coralStatic || '/static/') + 'state.js').then(st => {
        const row = st.state.liveSessions.find(s => s.session_id === 'qa-gone-id');
        Object.assign(row, ${JSON.stringify(on ? WAITING : { waiting_for_input: false, waiting_summary: null })});
        window.__promptScreen = ${JSON.stringify(on ? SCREEN : null)};
        return true; })`);
    const answerCard = async () => {
        await setWaiting(true);
        await waitFor(`!!document.querySelector('#live-history-messages > .chat-needs-input .cni-answer[data-n="1"]:not([disabled])')`, 'the prompt card with Yes');
        await ev(`document.querySelector('#live-history-messages > .chat-needs-input .cni-answer[data-n="1"]').click(); true`);
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [width, label] of [[null, 'desktop'], [390, 'phone']]) {
            if (width) await Emulation.setDeviceMetricsOverride({ width, height: 844, deviceScaleFactor: 2, mobile: true });
            await Page.navigate({ url: BASE });
            await Page.loadEventFired();
            await install();
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
            await ev(`window.__m.add('qa-gone', 'G', 5, { sleeping: false, board_project: 'team-gone' })`);
            await ev(`window.__m.add('qa-other', 'O', 3, { sleeping: false, board_project: 'team-gone' })`);
            await select('qa-gone');
            await firstPage();
            // Let the page finish settling before the first send: a late re-render of the selected
            // agent (seen right after the switch to phone width) clears the line under the composer,
            // which the first case would then wait for in vain.
            await waitStable(ev, `[T.state.currentSession && T.state.currentSession.session_id, document.getElementById('composer-error').hidden, window.__m.requests.length, window.__rendered().length].join('|')`, `${label}: the selected agent settled`, 400);
            // The composer's font is the same sans font on a phone as on a desktop (#3218).
            const font = await ev(`(() => { const f = getComputedStyle(document.getElementById('command-input')).fontFamily;
                const sans = getComputedStyle(document.documentElement).getPropertyValue('--font-sans').trim(); return { f, sans }; })()`);
            const first = v => v.split(',')[0].replace(/["'\s]/g, '');
            assert.equal(first(font.f), first(font.sans), `${label}: the composer uses the sans font (${font.f})`);

            const bodies = [
                ['server body', { error: "The agent's terminal is gone", code: 'terminal_gone', not_sent: true }],
                ['code only', { code: 'terminal_gone' }],
            ];
            for (const [what, body] of bodies) {
                const tag = `${label} ${what}`;
                await failWrites(409, body);
                for (const [path, fire] of [
                    ['composer', async () => { await type('hello gone'); await ev(`window.sendCommand()`); }],
                    ['keys Escape', () => ev(`window.sendRawKeys(['Escape'])`)],
                    ['keys C-c', () => ev(`window.sendRawKeys(['C-c'])`)],
                    ['prompt bar', answerCard],
                ]) {
                    await ev(`document.getElementById('composer-error').hidden = true; true`);
                    await clearToasts();
                    await fire();
                    await waitFor(`!document.getElementById('composer-error').hidden`, `${tag} ${path}: the error under the composer`);
                    const box = await errorBox();
                    assert.equal(box.text, MESSAGE, `${tag} ${path}: the text`);
                    assert.equal(box.restart, true, `${tag} ${path}: Restart is offered`);
                    assert.equal(box.below, true, `${tag} ${path}: under the composer's text`);
                    assert.equal(box.inView, true, `${tag} ${path}: on screen`);
                    assert.equal(box.role, 'alert', `${tag} ${path}: announced (the message's own span is the alert)`);
                    assert.equal(box.boxRole, null, `${tag} ${path}: and the box with its buttons is not a second live region`);
                    assert.deepEqual((await toasts()).filter(t => /terminal|Sent/.test(t)), [], `${tag} ${path}: no toast, never "Sent"`);
                }
                assert.equal(await ev(`document.getElementById('command-input').value`), 'hello gone', `${tag}: the draft is kept`);
                const hits = await ev('window.__writeHits');
                for (const kind of ['send', 'keys', 'answer-prompt']) assert.ok(hits.includes(kind), `${tag}: ${kind} was requested (${hits})`);

                // The agent is marked at once: its row and the header, both restart it.
                await waitFor(`!!document.querySelector('.session-group-item[data-session-id="qa-gone-id"] .terminal-gone-chip')`, `${tag}: the row chip`);
                assert.equal(await rowChip('qa-other'), false, `${tag}: only that agent's row`);
                assert.equal(await headerBadge(), 'Terminal gone · Restart', `${tag}: the header badge`);
                for (const [where, click] of [
                    ['composer Restart', `document.querySelector('#composer-error .composer-error-restart').click()`],
                    ['header badge', `document.getElementById('terminal-gone-header').click()`],
                    ['row chip', `document.querySelector('.session-group-item[data-session-id="qa-gone-id"] .terminal-gone-chip').click()`],
                ]) {
                    await ev(`${click}; true`);
                    await waitFor(`(() => { const m = document.getElementById('restart-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, `${tag}: ${where} opens Restart`);
                    await closeRestartModal();
                    assert.equal(await restartModalOpen(), false);
                }
                assert.equal(await ev(`T.state.currentSession.session_id`), 'qa-gone-id', `${tag}: the row chip restarts that agent, still selected`);

                // Dismissed: it stays closed while the feed ticks.
                await ev(`document.querySelector('#composer-error .composer-error-dismiss').click(); true`);
                assert.equal(await errorBox(), null, `${tag}: dismissed`);
                await ev(`window.__feed('qa-gone-id', {})`);
                assert.equal(await errorBox(), null, `${tag}: a feed tick does not bring it back`);

                // The feed shows it asleep: its own state takes over, the marks go.
                await ev(`window.__feed('qa-gone-id', { sleeping: true })`);
                await waitFor(`!document.querySelector('.session-group-item[data-session-id="qa-gone-id"] .terminal-gone-chip')`, `${tag}: chip cleared once asleep`);
                assert.equal(await headerBadge(), null, `${tag}: header badge cleared`);
                await ev(`window.__feed('qa-gone-id', { sleeping: false })`);
                await select('qa-gone');
                await waitFor(`!document.getElementById('command-input').disabled && document.getElementById('sleeping-composer').hidden`, `${tag}: awake again`);
                console.log(`   ✓ ${tag}: composer, keys and prompt bar say it under the composer with Restart; row and header marked`);
            }

            // Any other send error: under the composer, no Restart; a successful send clears it.
            // First let the re-selected agent settle (as before the first send): a late re-render
            // after the select above clears the line under the composer, which this case would
            // then wait for in vain (seen under load, at both widths).
            await waitStable(ev, `[T.state.currentSession && T.state.currentSession.session_id, document.getElementById('composer-error').hidden, window.__m.requests.length, window.__rendered().length].join('|')`, `${label}: the re-selected agent settled`, 400);
            await failWrites(500, { error: 'tmux failed (mock)' });
            await clearToasts();
            await type('another try');
            await ev(`window.sendCommand()`);
            await waitFor(`!document.getElementById('composer-error').hidden`, `${label}: other error under the composer`);
            const other = await errorBox();
            assert.equal(other.text, 'tmux failed (mock)');
            assert.equal(other.restart, false, `${label}: no Restart for other errors`);
            assert.equal(await rowChip('qa-gone'), false, `${label}: other errors do not mark the agent`);
            await restoreWrites();
            await ev(`window.sendCommand()`);
            await waitFor(`document.getElementById('composer-error').hidden`, `${label}: a successful send clears it`);

            // Selecting another agent clears it.
            await failWrites(500, { error: 'boom' });
            await type('x'); await ev(`window.sendCommand()`);
            await waitFor(`!document.getElementById('composer-error').hidden`, `${label}: error shown`);
            await restoreWrites();
            await select('qa-other');
            assert.equal(await errorBox(), null, `${label}: selecting another agent clears it`);
            console.log(`   ✓ ${label}: other errors inline without Restart; cleared by a send or another agent`);
            if (width) await Emulation.clearDeviceMetricsOverride();
        }
        console.log('\nAll terminal-gone checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
