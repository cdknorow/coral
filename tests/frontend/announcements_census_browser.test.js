// The screen-reader announcements census (#6213): in the agent chat on a desktop and on a phone, what the live regions say (helpers/announce_census.js records every change of every aria-live, role=status and
// role=alert region): an agent's reply is announced once when the turn is done, not per message or streamed step; a send says Sending and Sent once each; a failed send is said once, not by two regions; an agent
// that falls asleep and is woken says each state once; the toasts and notices of the Notifications column say their words once. ANNOUNCE_REPORT=<file> writes every scene's log to the file instead of failing.
// Drives Chrome over CDP through agent_view_harness.js with its mocked agents, against the isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { watch, clear, log, problems, said } = require('./helpers/announce_census');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const scenes = []; const found = [];
    const wait = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: announcements are timed; there is no event to wait for
    const scene = async (name, run, { must = [], forbid = [] } = {}) => {
        await clear(ev);
        try { await run(); } catch (e) { scenes.push({ name, entries: [] }); found.push(`${name}: the scene did not run (${String(e && e.message || e).slice(0, 160)})`); return; }
        await wait(600); // sleep-ok: the last announcement lands
        const entries = await log(ev);
        scenes.push({ name, entries });
        found.push(...problems(name, entries, { forbid }));
        for (const [re, label, count = 1] of must) { const n = said(entries, re).length; if (n !== count) found.push(`${name}: ${label} must be announced ${count === 1 ? 'once' : count + ' times'} (it was ${n})`); }
    };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, tag] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.__m.add('rep', 'R', 4, { sleeping: false }); window._coralSetLiveSessions && window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); true`);
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await select('rep');
            await waitFor(`window.__rendered().length >= 4`, `${tag}: the chat to render`, 15000);
            await watch(ev);
            const row = fn => ev(`(() => { const r = (window.T.state.liveSessions || []).find(s => s.session_id === 'rep-id'); if (!r) return false; ${fn}; return true; })()`);
            // a turn: the agent works, says four things (tool-step chatter between them), then stops
            await scene(`${tag}: an agent's turn of four replies`, async () => {
                await row(`r.working = true`);
                for (let i = 0; i < 4; i++) { await ev(`window.__m.agents['rep-id'].msgs.push({ type: 'assistant', text: 'Step ${i + 1}: I read the file and changed the second function so the build is green again.' }); true`); await wait(1200); } // sleep-ok: an announcement is timed; there is no event to wait for
                await row(`r.working = false`);
                await wait(2500); // sleep-ok: an announcement is timed; there is no event to wait for
            }, { must: [[/replied:/, 'the reply'], [/Working/i, 'Working', 1]] });
            // the same words said again are another turn
            await scene(`${tag}: a second turn`, async () => {
                await row(`r.working = true`); await wait(1200); // sleep-ok: an announcement is timed; there is no event to wait for
                await ev(`window.__m.agents['rep-id'].msgs.push({ type: 'assistant', text: 'Done: the build is green.' }); true`); await wait(1200); // sleep-ok: an announcement is timed; there is no event to wait for
                await row(`r.working = false`); await wait(2500); // sleep-ok: an announcement is timed; there is no event to wait for
            }, { must: [[/replied: Done/, 'the second reply']] });
            // a send
            await scene(`${tag}: a message is sent`, async () => {
                await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = 'Is the build green?'; i.dispatchEvent(new Event('input', { bubbles: true })); document.querySelector('[data-on-click="cmd-send"]').click(); return true; })()`);
                await wait(1500); // sleep-ok: an announcement is timed; there is no event to wait for
            }, { must: [[/Sending message/i, 'Sending message'], [/Message sent/i, 'Message sent']] });
            // a send that fails
            await scene(`${tag}: a message fails to send`, async () => {
                await ev(`(() => { const real = window.fetch; window.fetch = (u, o = {}) => /\\/send$/.test(String(u)) && (o.method || '').toUpperCase() === 'POST' ? Promise.resolve(new Response(JSON.stringify({ error: 'The agent is busy.', code: 'agent_busy' }), { status: 409, headers: { 'content-type': 'application/json' } })) : real(u, o); window.__realFetch = real; return true; })()`);
                await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = 'Again please'; i.dispatchEvent(new Event('input', { bubbles: true })); document.querySelector('[data-on-click="cmd-send"]').click(); return true; })()`);
                await wait(1500); // sleep-ok: an announcement is timed; there is no event to wait for
                await ev(`window.fetch = window.__realFetch; true`);
            }, { must: [[/not sent|busy/i, 'the failure']] });
            // asleep, then woken
            await scene(`${tag}: the agent falls asleep and is woken`, async () => {
                const feed = sleeping => ev(`(() => { window.__m.agents['rep-id'].sleeping = ${sleeping}; window.T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(window.__m.agents['rep-id']), session_id: 'rep-id' }] }); return true; })()`);
                await feed(true); await wait(2500); // sleep-ok: an announcement is timed; there is no event to wait for
                await feed(false); await wait(2500); // sleep-ok: an announcement is timed; there is no event to wait for
            });
            // notices
            await scene(`${tag}: warnings and the update notice`, async () => {
                await ev(`(async () => { const u = await import(${S} + 'update_check.js'); u.showSecurityWarning('The data folder can be read by Users: move it to one only you can read.'); u.showFirewallWarning('Windows Firewall lets other computers reach Coral on every port.'); return true; })()`);
                await wait(800); // sleep-ok: an announcement is timed; there is no event to wait for
            }, { must: [[/data folder can be read/, 'the security warning'], [/Windows Firewall lets/, 'the firewall warning']] });
            await scene(`${tag}: toasts and notices`, async () => {
                await ev(`(async () => { const n = await import(${S} + 'notifications.js'); n.showToast('Saved to the board.'); n.showToast('Could not reach the agent.', true); return true; })()`);
                await wait(800); // sleep-ok: an announcement is timed; there is no event to wait for
            }, { must: [[/Saved to the board/, 'a toast'], [/Could not reach the agent/, 'an error toast']] });
        }
    } finally { await client.close(); }
    if (process.env.ANNOUNCE_REPORT) { require('node:fs').writeFileSync(process.env.ANNOUNCE_REPORT, scenes.map(s => `## ${s.name}\n` + s.entries.map(e => `  ${String(e.t).padStart(5)} ms ${e.loud ? '!' : ' '} ${e.region}: ${e.text}${process.env.ANNOUNCE_HOW ? '   [' + e.how + ']' : ''}`).join('\n')).join('\n') + '\n--\n' + found.join('\n') + '\n'); process.exit(0); }
    assert.deepEqual(found, [], 'screen-reader announcement problems:\n  ' + found.join('\n  '));
    console.log(`PASS ${scenes.length} scenes: each state is announced once, nothing chatters`);
})().catch(e => { console.error(e); process.exit(1); });
