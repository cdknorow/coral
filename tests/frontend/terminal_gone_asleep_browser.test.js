// #3344: an agent Coral put to sleep because its terminal session is gone (sleep_reason
// 'terminal_gone' from the server) says "Terminal gone", not "Sleeping": the row's label and
// a "Terminal gone" chip that restarts it (as #3218's), and the composer says the terminal is
// gone. It stays in view rather than folded into the "N sleeping agents" row. A plain
// sleeping agent and an archived one are unchanged.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await install();

        await ev(`(async () => {
            window.__m.add('gone-a', 'G', 3, { sleeping: true, sleep_reason: 'terminal_gone' });
            window.__m.add('nap-b', 'N', 3, { sleeping: true });
            window.__m.add('arch-c', 'A', 3, { archived: true, sleep_reason: 'terminal_gone' });
            window.T.render.renderLiveSessions(window.T.state.liveSessions);
            return true;
        })()`);
        const row = id => `document.querySelector('.session-group-item[data-session-id="${id}"]')`;
        const rows = await ev(`(() => { const r = id => document.querySelector('.session-group-item[data-session-id="' + id + '"]');
            const info = id => { const e = r(id); return e ? { chip: !!e.querySelector('.terminal-gone-chip'), text: e.textContent.replace(/\\s+/g, ' '), aria: e.getAttribute('aria-label') || '' } : null; };
            return { gone: info('gone-a-id'), nap: info('nap-b-id'), arch: info('arch-c-id') }; })()`);
        assert.ok(rows.gone, 'the gone agent is in view, not folded into the sleeping row: ' + JSON.stringify(rows));
        assert.equal(rows.nap, null, 'a plain sleeping agent stays folded into "1 sleeping agent"');
        assert.equal(rows.gone.chip, true, 'the gone agent has the Terminal gone chip');
        assert.equal(!!(rows.arch && rows.arch.chip), false, 'an archived agent does not, whatever the reason');
        const labels = await ev(`(async () => { const r = await import((window.__coralStatic||'/static/') + 'render.js');
            const s = id => window.T.state.liveSessions.find(x => x.session_id === id);
            return { gone: r.sessionStateInfo(s('gone-a-id')), nap: r.sessionStateInfo(s('nap-b-id')) }; })()`);
        assert.equal(labels.gone.key, 'sleeping', 'still asleep (counts, filters and Wake are unchanged)');
        assert.equal(labels.gone.label, 'Terminal gone');
        assert.equal(labels.nap.label, 'Sleeping');
        console.log('   ✓ row: "Terminal gone" label and chip for the gone agent only');

        // The composer of the selected gone agent says so; a plain sleeping agent still says sleeping.
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        const composer = async (name, id) => ev(`(async () => { window.selectLiveSession('${name}', 'claude', '${id}', 'local');
            await window.__coralReady.agentSelected('${id}');
            const bar = document.getElementById('sleeping-composer');
            return { shown: !bar.hidden, text: document.getElementById('sleeping-composer-text').textContent, gone: bar.classList.contains('is-terminal-gone'),
                restart: !document.getElementById('sleeping-restart-btn').hidden, wake: !document.getElementById('sleeping-wake-btn').hidden }; })()`);
        const g = await composer('gone-a', 'gone-a-id');
        assert.deepEqual(g, { shown: true, text: "This agent's terminal is gone", gone: true, restart: true, wake: false }, 'Restart only: there is no terminal to wake');
        const n = await composer('nap-b', 'nap-b-id');
        assert.deepEqual(n, { shown: true, text: 'This agent is sleeping', gone: false, restart: true, wake: true });
        console.log("   ✓ composer: says the terminal is gone, with Restart; a plain sleeper is unchanged");

        // The chip restarts the agent (as #3218's), not a row select.
        const restarted = await ev(`(async () => { window.__restarts = []; const orig = window.restartDirect;
            window.restartDirect = (...a) => { window.__restarts.push(a); };
            window.T.render.renderLiveSessions(window.T.state.liveSessions);
            ${row('gone-a-id')}.querySelector('.terminal-gone-chip').click();
            window.restartDirect = orig; return window.__restarts; })()`);
        assert.deepEqual(restarted, [['gone-a', 'claude', 'gone-a-id']]);
        console.log('   ✓ the chip restarts it');
        console.log('PASS terminal_gone_asleep_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
