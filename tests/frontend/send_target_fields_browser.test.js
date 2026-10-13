// Every /send and /keys request names its exact target (#3032): the server
// refuses a bare-name or half-named call (400 session_id_required), so each
// frontend path must send session_id and agent_type, and they must be the
// selected agent's. Driven through every send-capable control: Enter in the
// composer, each toolbar button whose handler sends (quick actions, macros,
// Esc/arrows/Enter, mode toggle, the Send menu) and the send functions, for a
// local and a hub-remote agent, at desktop and phone width.
//
// Drives Chrome over CDP (chrome-remote-interface) through agent_view_harness.js; run_cdp_suites.mjs
// finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;

// Handlers that reach /send or /keys (the same list popout.js uses to strip send controls).
const SEND_CAPABLE = 'sendCommand|sendCommandWithTeam|sendBoardProtocol|sendRawKeys|cycleModeToggle|sendModeToggle|sendQuickCommand|executeMacro|resendInputPrompt';

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install, select, reqs, clearReqs, firstPage } = await connect();

    // Fire every send path for the selected agent; returns how many controls were clicked.
    const driveAll = () => ev(`(async () => {
        const input = document.getElementById('command-input');
        const say = t => { input.value = t; input.dispatchEvent(new Event('input', { bubbles: true })); };
        const settle = () => new Promise(r => setTimeout(r, 60));
        say('hello from the composer');
        input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }));
        await settle();
        // Every visible control whose handler can send (the confirm modal, if any, is accepted).
        const re = new RegExp('(${SEND_CAPABLE})\\\\(');
        const controls = [...document.querySelectorAll('#command-pane [onclick]')].filter(b => re.test(b.getAttribute('onclick')));
        for (const b of controls) {
            say('typed before ' + (b.getAttribute('aria-label') || b.textContent.trim()).slice(0, 30));
            b.click();
            await settle();
            const modal = document.getElementById('confirm-modal');
            if (modal && getComputedStyle(modal).display !== 'none') { document.getElementById('confirm-modal-yes').click(); await settle(); }
        }
        // And the functions directly, for paths without a visible control here.
        say('direct send'); await window.sendCommand();
        say('quick'); window.sendQuickCommand('/compact'); await settle();
        window.executeMacro('/status && /help'); await new Promise(r => setTimeout(r, 1200));
        await window.sendRawKeys(['Escape']);
        await window.sendRawKeys(['Up'], { silent: true });
        window.cycleModeToggle(); await settle();
        say('board protocol'); await window.sendBoardProtocol?.(); await settle();
        return controls.length;
    })()`);

    const checkTargets = async (label, name, sid, server) => {
        const r = (await reqs()).filter(x => x.kind === 'send' || x.kind === 'keys');
        assert.ok(r.length >= 6, `${label}: send paths produced requests (${r.length})`);
        const bad = [];
        for (const x of r) {
            let body = null;
            try { body = JSON.parse(x.body); } catch { /* not JSON */ }
            const ok = body && body.session_id === sid && body.agent_type === 'claude' && x.agent === name
                && (server === 'local' ? x.remote === null : x.remote === server);
            if (!ok) bad.push({ kind: x.kind, remote: x.remote, body: x.body });
        }
        assert.deepEqual(bad, [], `${label}: every /send and /keys names session_id + agent_type of the selected agent`);
        const kinds = { send: r.filter(x => x.kind === 'send').length, keys: r.filter(x => x.kind === 'keys').length };
        assert.ok(kinds.send > 0 && kinds.keys > 0, `${label}: both /send and /keys exercised (${JSON.stringify(kinds)})`);
        return kinds;
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [label, width] of [['desktop', null], ['phone', 390]]) {
            if (width) await Emulation.setDeviceMetricsOverride({ width, height: 844, deviceScaleFactor: 2, mobile: true });
            await Page.navigate({ url: BASE });
            await Page.loadEventFired();
            await install();
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat')`);
            for (const [name, server] of [['qa-send-local', 'local'], ['qa-send-remote', 'qa-remote-1']]) {
                await ev(`window.__m.add('${name}', 'S', 30, { sleeping: false, server: '${server}', board_project: 'team-send' })`);
                await select(name, server);
                await firstPage();
                await clearReqs();
                const clicked = await driveAll();
                await sleep(200);
                const kinds = await checkTargets(`${label} ${server}`, name, name + '-id', server);
                console.log(`   ✓ ${label} ${server}: ${clicked} controls + direct calls → ${kinds.send} /send, ${kinds.keys} /keys, all with session_id + agent_type`);
            }
            if (width) await Emulation.clearDeviceMetricsOverride();
        }
        console.log('\nAll send-target checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
