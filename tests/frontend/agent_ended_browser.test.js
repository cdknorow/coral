// An agent whose CLI exited (#6058, from #6050's report): the server says so with sleep_reason "agent_exited" (the reconciler saw the pane at a plain shell). The sidebar row stays in view with the text "Ended" (not folded into
// "N sleeping agents"), the state label is "Ended", and the composer says "<name> stopped before answering" when a message of the person's is still waiting ("has ended" otherwise), once, with Restart.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired(); await install();
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await ev(`(() => {
            window.__m.add('ended-a', 'E', 3, { sleeping: true, sleep_reason: 'agent_exited' });
            window.__m.add('nap-b', 'N', 3, { sleeping: true });
            window.__m.add('arch-c', 'A', 3, { archived: true, sleep_reason: 'agent_exited' });
            window.T.render.renderLiveSessions(window.T.state.liveSessions); return true; })()`);
        const rows = await ev(`(() => { const r = id => document.querySelector('.session-group-item[data-session-id="' + id + '"]');
            const info = id => { const e = r(id); return e ? { chip: (e.querySelector('.terminal-gone-chip') || {}).textContent || '', aria: (e.querySelector('.terminal-gone-chip') || document.body).getAttribute('aria-label'), title: (e.querySelector('.terminal-gone-chip') || {}).title || '' } : null; };
            return { ended: info('ended-a-id'), nap: info('nap-b-id'), arch: info('arch-c-id') }; })()`);
        assert.ok(rows.ended, 'the ended agent is in view, not folded into the sleeping row: ' + JSON.stringify(rows));
        assert.equal(rows.ended.chip, 'Ended'); assert.equal(rows.ended.aria, 'Ended. Restart', 'the chip is named for what it does'); assert.match(rows.ended.title, /CLI exited.*restart/i);
        assert.equal(rows.nap, null, 'a plain sleeping agent stays folded');
        assert.equal(!!(rows.arch && rows.arch.chip), false, 'an archived agent is just archived');
        const label = await ev(`(async () => { const r = await import((window.__coralStatic||'/static/') + 'render.js'); const s = id => window.T.state.liveSessions.find(x => x.session_id === id);
            return { ended: r.sessionStateInfo(s('ended-a-id')), nap: r.sessionStateInfo(s('nap-b-id')) }; })()`);
        assert.deepEqual([label.ended.key, label.ended.label, label.ended.pill, label.nap.label], ['sleeping', 'Ended', 'Ended', 'Sleeping'], 'still asleep (counts and filters unchanged), and the word is Ended');
        const composer = id => ev(`(async () => { window.selectLiveSession('ended-a', 'claude', '${id}', 'local'); await window.__coralReady.agentSelected('${id}');
            const bar = document.getElementById('sleeping-composer'); const t = document.getElementById('sleeping-composer-text');
            return { shown: !bar.hidden, role: bar.getAttribute('role'), text: t.textContent, restart: !document.getElementById('sleeping-restart-btn').hidden, tab: document.getElementById('sleeping-restart-btn').tabIndex }; })()`);
        let c = await composer('ended-a-id');
        assert.deepEqual([c.shown, c.role, c.restart, c.tab], [true, 'status', true, 0]);
        assert.match(c.text, /has ended$/, 'no message waiting: just that it ended: ' + c.text);
        await ev(`(window.T.state.lastSendAt ||= new Map()).set('ended-a-id', Date.now()); window.T.render.renderLiveSessions(window.T.state.liveSessions); true`);
        c = await composer('ended-a-id');
        assert.match(c.text, /stopped before answering$/, 'a message was sent and nothing came back: ' + c.text);
        assert.equal(await ev(`(() => { const t = document.getElementById('sleeping-composer-text'); let n = 0; new MutationObserver(m => { n += m.length; }).observe(t, { childList: true, characterData: true, subtree: true }); window.__endedMuts = () => n; return true; })()`), true);
        await ev(`window.T.render.renderLiveSessions(window.T.state.liveSessions); true`); await new Promise(r => setTimeout(r, 1500)); // sleep-ok: two refreshes with nothing to change
        assert.equal(await ev(`window.__endedMuts()`), 0, 'the line is written once: a refresh with the same words rewrites nothing');
        // A message that did not go because the CLI had exited (the server marked the agent Ended and says agent_exited): the composer names who stopped, keeps the draft and offers Restart.
        await ev(`window.__m.add('live-x', 'X', 3, {}); window.T.render.renderLiveSessions(window.T.state.liveSessions); true`);
        const err = await ev(`(async () => { window.selectLiveSession('live-x', 'claude', 'live-x-id', 'local'); await window.__coralReady.agentSelected('live-x-id');
            const i = document.getElementById('command-input'); i.value = 'please answer'; i.dispatchEvent(new Event('input', { bubbles: true }));
            const m = await import((window.__coralStatic || '/static/') + 'terminal_gone.js'); m.reportSendError({ name: 'live-x', display_name: 'Xavier' }, 'The agent did not confirm your message.', 'not_confirmed', true);
            const e = document.getElementById('composer-error');
            return { hidden: e.hidden, text: e.querySelector('.composer-error-text').textContent, restart: !e.querySelector('.composer-error-restart').hidden, draft: i.value }; })()`);
        assert.equal(err.hidden, false); assert.match(err.text, /^Xavier stopped before answering/); assert.equal(err.restart, true, 'with Restart'); assert.equal(err.draft, 'please answer', 'the draft is kept');
        console.log('PASS agent ended: the row says Ended, the composer says it once with Restart');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
