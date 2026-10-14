// An agent whose server is unreachable (#3151). On a hub, a remote that stops
// answering makes the proxy return 502 {"error":"remote unreachable","server":id},
// and the live feed marks its agents stale. The dashboard must: back off its polls
// on gateway errors; show plain text naming the server (never raw JSON); for a
// stale agent show a banner, refuse sends with the draft kept (no 'Sent') and stop
// polling; resume at once when the server is back; and not poll an agent that is
// not on screen.
//
// Drives Chrome over CDP (chrome-remote-interface); run_cdp_suites.mjs finds CDP suites by that name.
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

// The remote 'box-b' through the hub proxy: down (502) or up, counted per endpoint.
const STUB = `(() => {
    window.__remote = { up: false, n: { poll: 0, ui: 0, send: 0 } };
    const real = window.fetch.bind(window);
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } }));
    window.fetch = (url, opts) => {
        const p = new URL(String(url), location.origin).pathname;
        if (!p.startsWith('/api/remote/box-b/')) return real(url, opts);
        const r = window.__remote;
        const kind = /\\/poll$/.test(p) ? 'poll' : /\\/api\\/agent\\/ui$/.test(p) ? 'ui' : /\\/send$/.test(p) ? 'send' : 'other';
        if (kind in r.n) r.n[kind]++;
        if (!r.up) return json({ error: 'remote unreachable', server: 'box-b' }, 502);
        if (kind === 'poll') return json({ capture: { capture: 'remote prompt $' }, tasks: [], events: [] });
        if (kind === 'ui') return json([]);
        if (kind === 'send') return json({ ok: true });
        return json({});
    };
})();`;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async x => {
        const r = await Runtime.evaluate({ expression: x, awaitPromise: true, returnByValue: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const consoleErrors = [];
    Runtime.consoleAPICalled(m => { if (m.type === 'error') consoleErrors.push(m.args.map(a => a.value ?? a.description ?? '').join(' ')); });
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const counts = () => ev(`({ ...window.__remote.n })`);
    const banner = () => ev(`(() => { const b = document.getElementById('server-unreachable-banner'); return { shown: !!b && !b.hidden && b.getClientRects().length > 0, text: b ? b.textContent : null }; })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast')].map(t => t.textContent.replace(/×$/, '').trim())`);
    // The hub's real shapes: a down server's agents carry stale:true and the server
    // 'unreachable'; on recovery the hub OMITS stale (it does not send stale:false)
    // and reports the server 'online' (#3151, QA's end-to-end finding).
    const feed = down => ev(`(async () => {
        const ws = await import((window.__coralStatic || '/static/') + 'websocket.js');
        ws.handleCoralMessage({ type: 'coral_diff', changed: [${JSON.stringify({ name: 'rb', session_id: 'rb-1', server: 'box-b', agent_type: 'terminal' })}].map(s => ${down ? '({ ...s, stale: true })' : 's'}),
            servers: [{ id: 'box-b', label: 'box-b.lan', status: ${down ? "'unreachable'" : "'online'"} }] });
        return true;
    })()`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await coralReady('startup', 'liveList', 'settings');
        // A hub with one remote agent, fed through the real feed handler (the real feed socket detached).
        await ev(`(async () => {
            const st = window.__coralStatic || '/static/';
            const { state } = await import(st + 'state.js');
            const ws = await import(st + 'websocket.js');
            if (state.coralWs) { state.coralWs.onclose = null; state.coralWs.onmessage = null; try { state.coralWs.close(); } catch {} }
            state.hub = true;
            localStorage.setItem('coral-live-view-mode', 'terminal');
            ws.handleCoralMessage({ type: 'coral_update', servers: [{ id: 'box-b', label: 'box-b.lan', status: 'online' }],
                sessions: [{ name: 'rb', session_id: 'rb-1', agent_type: 'terminal', server: 'box-b', display_name: 'Remote shell', status: 'working', stale: false }] });
            await window.selectLiveSession('rb', 'terminal', 'rb-1', 'box-b');
            return true;
        })()`);

        // 1. Gateway errors (server down, not yet marked stale): the poll backs off, no console spam.
        const c0 = await counts();
        await sleep(6000); // sleep-ok: observation window for the poll rate (500 ms beat without back-off = ~12)
        const c1 = await counts();
        const polls = c1.poll - c0.poll;
        assert.ok(polls >= 1 && polls <= 5, `polls back off on 502 (${polls} in 6 s; ~12 without back-off)`);
        assert.equal(consoleErrors.filter(e => /Failed to refresh capture/.test(e)).length, 0, 'no console error per failed poll');
        console.log(`   ✓ gateway errors back off: ${polls} polls in 6 s, no console spam`);

        // 2. A failed send names the server in plain text, under the composer (#3218); no raw JSON, no 'Sent', draft kept.
        await ev(`(() => { const i = document.getElementById('command-input'); i.value = 'echo plain'; return true; })()`);
        await ev(`window.sendCommand()`);
        await waitFor(`(() => { const b = document.getElementById('composer-error'); return !b.hidden && /box-b\\.lan is unreachable/.test(b.textContent); })()`, 'the plain unreachable error under the composer', 5000);
        const e2 = await ev(`document.querySelector('#composer-error .composer-error-text').textContent`);
        assert.ok(!/[{}]|Server error|502/.test(e2), 'no raw JSON or status code: ' + e2);
        assert.equal(await ev(`document.querySelector('#composer-error .composer-error-restart').hidden`), true, 'no Restart: the agent is fine, its server is not');
        const t2 = await toasts();
        assert.ok(!t2.some(t => /^Sent/.test(t)), 'nothing says Sent');
        assert.equal(await ev(`document.getElementById('command-input').value`), 'echo plain', 'the draft is kept');
        console.log('   ✓ failed send: plain text naming the server, draft kept, no "Sent"');

        // 3. The feed marks the agent stale: banner, polls stop, sends refused before any request.
        await feed(true);
        await waitFor(`(() => { const b = document.getElementById('server-unreachable-banner'); return b && !b.hidden; })()`, 'the unreachable banner', 3000);
        const b3 = await banner();
        assert.ok(b3.shown && /box-b\.lan is unreachable/.test(b3.text), 'banner shown with the reason: ' + JSON.stringify(b3));
        const c3 = await counts();
        await sleep(3000); // sleep-ok: proves a negative, no polls while the server is unreachable
        const c4 = await counts();
        assert.equal(c4.poll - c3.poll, 0, 'no capture polls while stale');
        assert.equal(c4.ui - c3.ui, 0, 'no agent UI polls while stale');
        await ev(`(() => { document.querySelectorAll('.toast').forEach(t => t.remove()); const i = document.getElementById('command-input'); i.value = 'echo guarded'; return true; })()`);
        await ev(`window.sendCommand()`);
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => /box-b\\.lan is unreachable/.test(t.textContent))`, 'the refusal toast', 3000);
        const c5 = await counts();
        assert.equal(c5.send - c4.send, 0, 'a send to an unreachable agent makes no request');
        assert.equal(await ev(`document.getElementById('command-input').value`), 'echo guarded', 'the draft is kept');
        console.log('   ✓ stale: banner with the reason, no polls, send refused locally with the draft kept');

        // 4. The server is back: banner gone, polling resumes at once (back-off reset).
        await ev(`window.__remote.up = true; true`);
        const c6 = await counts();
        await feed(false); // stale OMITTED, as the hub sends it
        await waitFor(`(() => { const b = document.getElementById('server-unreachable-banner'); return b && b.hidden; })()`, 'the banner clearing', 3000);
        await waitFor(`window.__remote.n.poll > ${c6.poll}`, 'a poll after recovery', 2500);
        await waitFor(`document.getElementById('pane-capture').textContent.includes('remote prompt')`, 'the capture from the recovered server', 3000);
        console.log('   ✓ recovered: banner cleared, polling resumed within ~1 s');

        // 5. Not on screen: no polls while another top tab is shown.
        await ev(`window.switchNavTab('jobs'); true`);
        const c7 = await counts();
        await sleep(2000); // sleep-ok: proves a negative, no polls while the agent view is not shown
        const c8 = await counts();
        assert.equal(c8.poll - c7.poll, 0, 'no capture polls while the agent view is off screen');
        await ev(`window.switchNavTab('agents'); true`);
        console.log('   ✓ off screen: no polls');
        console.log('PASS unreachable_server_browser');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e); process.exit(1); });
