// The "Agent is restarting..." overlay (#4481, coral-hub's report): one node over the agent's pane, shared by every agent and by
// both views. It cleared only when the Terminal view reattached, so in Chat view (the default, and the phone's) it never went
// away, and it stayed up when another agent was selected. The restart is now kept per session id: the overlay shows for the
// agent that is restarting, goes when that agent's restart ends or another agent is selected, and is back when the restarting
// agent is selected again. Desktop and a 390x844 phone.
//
// Drives Chrome over CDP through agent_view_harness.js; run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const BASE = require('./test_base').BASE;
const fs = require('node:fs');

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install, select } = await connect();

    // The overlay as a reader sees it: shown, and which words.
    const overlay = () => ev(`(() => {
        const o = document.getElementById('session-ended-overlay');
        const shown = id => { const e = document.getElementById(id); return !!e && getComputedStyle(e).display !== 'none'; };
        const r = o.getBoundingClientRect();
        return { shown: getComputedStyle(o).display !== 'none' && r.width > 0, restarting: shown('session-restarting'), ended: shown('session-ended-default'), size: [Math.round(r.width), Math.round(r.height)] };
    })()`);
    const barText = () => ev(`document.querySelector('#terminal-statusbar .terminal-statusbar-state-text').textContent`);
    const current = () => ev(`({ name: T.state.currentSession.name, id: T.state.currentSession.session_id })`);
    const restart = () => ev(`(() => { window.confirmRestart(); return true; })()`);
    const marked = id => ev(`import((window.__coralStatic || '/static/') + 'session_overlay.js').then(m => m.isRestarting(${JSON.stringify(id)}))`);

    // The restart route and the live-detail poll the restart waits on, answered here.
    const MOCK_RESTART = `(() => {
        window.__rs = { delay: 0, live: false, error: null, posts: 0 };
        const prev = window.fetch;
        const json = (b, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } });
        window.fetch = (url, opts = {}) => {
            const u = String(url);
            const rm = u.match(/\\/api\\/sessions\\/live\\/([^/?]+)\\/restart$/);
            if (rm && (opts.method || 'GET').toUpperCase() === 'POST') {
                window.__rs.posts++;
                const name = decodeURIComponent(rm[1]);
                return new Promise(res => setTimeout(() => res(window.__rs.error ? json({ error: window.__rs.error }) : json({ ok: true, session_id: name + '-new', session_name: name, agent_name: name })), window.__rs.delay));
            }
            if (/\\/api\\/sessions\\/live\\/[^/?]+\\?session_id=/.test(u)) return Promise.resolve(window.__rs.live ? json({ ok: true }) : json({ error: 'not yet' }, 404));
            return prev(url, opts);
        };
    })()`;

    const shot = async (name) => {
        if (!process.env.CORAL_SCREENSHOT_DIR) return;
        fs.mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
        fs.writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
    };

    const run = async (label) => {
        console.log(`${label}: a restart in Chat view leaves the overlay hidden afterwards`);
        await ev(`window.__rs.live = false; window.__rs.delay = 0; window.__rs.error = null`);
        await select('ro-a');
        assert.equal((await overlay()).shown, false, 'no overlay before');
        await restart();
        await waitFor(`getComputedStyle(document.getElementById('session-ended-overlay')).display !== 'none'`, 'the overlay while it restarts', 4000);
        let o = await overlay();
        assert.ok(o.shown && o.restarting && !o.ended, 'restarting words: ' + JSON.stringify(o));
        assert.ok(o.size[0] > 150 && o.size[1] > 100, 'covers the pane: ' + JSON.stringify(o));
        await shot('restart-overlay-' + label.split(' ')[0].toLowerCase());
        assert.equal(await barText(), 'Restarting', 'the bar over the chat says Restarting, not Session ended');
        await waitFor(`import((window.__coralStatic || '/static/') + 'session_overlay.js').then(m => m.isRestarting('ro-a-new'))`, "the restart's mark to be the replacement session's", 4000);
        assert.equal(await marked('ro-a-id'), false, 'not left on the old session id');
        await ev(`window.__rs.live = true`);
        await waitFor(`getComputedStyle(document.getElementById('session-ended-overlay')).display === 'none'`, 'the overlay to clear once the agent is live', 6000);
        assert.equal(await marked('ro-a-new'), false, 'the mark is gone');
        assert.equal(await marked('ro-a-id'), false);
        assert.doesNotMatch(await barText(), /Restarting|Session ended/, 'the bar no longer says so');
        console.log('   ✓ shown while restarting (the bar says Restarting), hidden once live');

        console.log(`${label}: a switch mid-restart hides it, and a switch back shows it again`);
        await ev(`window.__rs.live = false; window.__rs.delay = 60000`);
        await select('ro-a2');
        await restart();
        await waitFor(`getComputedStyle(document.getElementById('session-ended-overlay')).display !== 'none'`, 'the overlay for the second restart', 4000);
        await select('ro-b');
        o = await overlay();
        assert.equal(o.shown, false, 'another agent: no overlay ' + JSON.stringify(o));
        assert.deepEqual(await current(), { name: 'ro-b', id: 'ro-b-id' });
        assert.notEqual(await barText(), 'Restarting', 'the other agent is not restarting');
        await select('ro-a2');
        o = await overlay();
        assert.ok(o.shown && o.restarting, 'back on the restarting agent: restarting again ' + JSON.stringify(o));
        await select('ro-b');
        assert.equal((await overlay()).shown, false);
        console.log('   ✓ hidden on the other agent, back on the restarting one');
    };

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE });
        await Page.loadEventFired();
        await install();
        await ev(`localStorage.clear(); localStorage.setItem('coral-live-view-mode', 'chat')`);
        await ev(MOCK_RESTART);
        await ev(`(() => { const m = window.__m; for (const n of ['ro-a', 'ro-a2', 'ro-b', 'ro-c', 'ro-d']) m.add(n, n[3].toUpperCase(), 40, { sleeping: false }); T.render.renderLiveSessions(T.state.liveSessions); })()`);

        await run('Desktop');

        console.log('Another agent selected while one restarts, and the restart then ends: the screen is not touched');
        await ev(`window.__rs.live = false; window.__rs.delay = 800`);
        await select('ro-c');
        await restart();
        await select('ro-d');
        await sleep(1200); // sleep-ok: the restart answer arrives while ro-d is selected
        assert.deepEqual(await current(), { name: 'ro-d', id: 'ro-d-id' }, "the answer of ro-c's restart does not rename or re-id the agent on screen");
        assert.equal((await overlay()).shown, false);
        assert.equal(await marked('ro-c-new'), true, 'the mark moved to the replacement session');
        await ev(`window.__rs.live = true`);
        await waitFor(`import((window.__coralStatic || '/static/') + 'session_overlay.js').then(m => !m.isRestarting('ro-c-new'))`, 'the mark to end when the replacement is live', 6000);
        console.log('   ✓ ro-d untouched; the mark follows the replacement and ends');

        console.log('A restart the server refuses leaves no overlay');
        await ev(`window.__rs.live = false; window.__rs.delay = 0; window.__rs.error = 'cannot restart (mock)'`);
        await select('ro-a');
        await restart();
        await waitFor(`window.__rs.posts >= 3`, 'the refused restart to be asked', 4000);
        await waitFor(`getComputedStyle(document.getElementById('session-ended-overlay')).display === 'none'`, 'the overlay to go after a refusal', 4000);
        assert.equal(await marked('ro-a-id'), false);
        console.log('   ✓ refusal clears it');

        console.log('"Session ended" does not follow the Operator to another agent, and Chat view does not carry it');
        await ev(`window.__rs.error = null`);
        await select('ro-a');
        await ev(`(() => { const o = document.getElementById('session-ended-overlay'); o.style.display = ''; document.getElementById('session-ended-default').style.display = ''; document.getElementById('session-restarting').style.display = 'none'; })()`);
        assert.equal((await overlay()).ended, true);
        await select('ro-b');
        assert.equal((await overlay()).shown, false, 'the ended overlay of the previous agent is gone');
        await ev(`(() => { const o = document.getElementById('session-ended-overlay'); o.style.display = ''; document.getElementById('session-ended-default').style.display = ''; })()`);
        await ev(`import((window.__coralStatic || '/static/') + 'agent_view.js').then(m => m.fireAgentView('coral:terminal-unwanted'))`);
        assert.equal((await overlay()).shown, false, 'switching to Chat view takes "Session ended" off the transcript');
        console.log('   ✓ ended overlay gone on a switch and in Chat view');

        console.log('Terminal view as before: setRestarting shows and hides for the agent on screen only');
        await select('ro-a');
        const t = await ev(`(async () => {
            const x = await import((window.__coralStatic || '/static/') + 'xterm_renderer.js');
            const shown = () => getComputedStyle(document.getElementById('session-ended-overlay')).display !== 'none';
            const out = {};
            x.setRestarting(true, 'ro-a-id'); out.on = shown();
            x.setRestarting(false, 'someone-else'); out.otherEndKeeps = shown();
            x.setRestarting(false, 'ro-a-id'); out.off = shown();
            x.setRestarting(true, 'someone-else'); out.otherStartNothing = shown();
            x.setRestarting(false, 'someone-else');
            return out;
        })()`);
        assert.deepEqual(t, { on: true, otherEndKeeps: true, off: false, otherStartNothing: false });
        console.log('   ✓ start/end apply to the agent on screen only');

        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true });
        await ev(`window.__rs.error = null`);
        await sleep(300); // sleep-ok: the phone layout settles
        await run('Phone 390x844');
        await Emulation.clearDeviceMetricsOverride();
        await Emulation.setTouchEmulationEnabled({ enabled: false });

        console.log('\nAll restart overlay checks passed');
    } finally {
        await client.close();
    }
})().catch(err => {
    console.error(err);
    process.exit(1);
});
