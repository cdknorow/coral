// A write the Coral Hub could not finish telling (#6342): the hub answers 502 {code: "outcome_unknown"} when it sent a write on to the computer and lost the answer, so the write may or may not have happened. Every write through the
// hub except a launch says ONE line, "That may have reached the computer. Check before trying again.", and nothing sends it again by itself. Driven here: the composer's send, the team board's message box, an upload's reply,
// the shared error text, and the hub's fetch wrapper (a task create, a reminder, any write); a launch keeps its own wording. Each path's POST is counted and counted again after a wait: one request, never a retry.
// The hub's wrapper is installed over a scripted fetch (installCloudFetch), as the Hub-served dashboard has it. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');

const LINE = 'That may have reached the computer. Check before trying again.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const { Input } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const board = `ou-board-${Date.now()}`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // the scripted hub: every write answers 502 outcome_unknown (a launch too), and counts; the hub's session for the CSRF token; everything else is the page's own scripted fetch
        await ev(`(() => {
            const under = window.fetch.bind(window); window.__posts = []; window.__mode = 'unknown';
            window.fetch = async (input, init = {}) => {
                const url = String(input && input.url ? input.url : input), method = String(init.method || 'GET').toUpperCase();
                if (/\\/auth\\/session/.test(url)) return new Response(JSON.stringify({ authenticated: true, email: 'o@example.invalid', csrf_token: 'x' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
                if (method !== 'GET' && /\\/api\\//.test(url)) {
                    window.__posts.push(method + ' ' + url.replace(/^https?:\\/\\/[^/]+/, ''));
                    if (window.__mode === 'unknown') return new Response(JSON.stringify({ error: 'bad gateway', code: 'outcome_unknown' }), { status: 502, headers: { 'Content-Type': 'application/json' } });
                    if (window.__mode === 'errorOnly') return new Response(JSON.stringify({ error: 'outcome_unknown' }), { status: 502, headers: { 'Content-Type': 'application/json' } });
                    if (window.__mode === 'five') return new Response(JSON.stringify({ error: 'server error', code: 'outcome_unknown' }), { status: 500, headers: { 'Content-Type': 'application/json' } });
                    if (window.__mode === 'plain') return new Response(JSON.stringify({ error: 'bad gateway' }), { status: 502, headers: { 'Content-Type': 'application/json' } });
                }
                return under(input, init);
            };
            return true; })()`);
        await ev(`import(${S} + 'cloud.js').then(m => { m.installCloudFetch(); return true; })`);
        const posts = () => ev('window.__posts.slice()');
        const reset = () => ev('window.__posts.length = 0; true');

        console.log('1. The shared pieces');
        const said = await ev(`(async () => {
            const call = async (method, url) => { const r = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: '{}' }); return { status: r.status, body: await r.json() }; };
            const m = await import(${S} + 'server_status.js');
            const task = await call('POST', '/api/board/x/tasks'), launch = await call('POST', '/api/sessions/launch'), team = await call('POST', '/api/sessions/launch-team'), remote = await call('POST', '/api/remote/box/api/sessions/launch');
            window.__mode = 'plain'; const plain = await call('POST', '/api/board/x/tasks'); window.__mode = 'unknown';
            const resp = await fetch('/api/board/x/messages', { method: 'POST', body: '{}' });
            return { task, launch, team, remote, plain, text: await m.responseErrorText(resp.clone(), 'local', 'fallback') };
        })()`);
        check('a write through the hub that answers outcome_unknown carries the one line as its error (any path that shows the server\'s words shows it), still 502 with its code', said.task.status === 502 && said.task.body.error === LINE && said.task.body.code === 'outcome_unknown', JSON.stringify(said.task));
        check('a launch keeps its own words (the launch and launch-team routes, direct or through a remote path)', [said.launch, said.team, said.remote].every(x => x.body.error === 'bad gateway'), JSON.stringify([said.launch.body.error, said.team.body.error, said.remote.body.error]));
        check('another 502 is not changed', said.plain.body.error === 'bad gateway' && !('code' in said.plain.body), JSON.stringify(said.plain));
        check('the shared error text says the line, not "try again in a moment"', said.text === LINE, said.text);
        const other = await ev(`(async () => {
            const m = await import(${S} + 'outcome_unknown.js');
            const r = await fetch('/api/board/x/tasks', { method: 'POST', body: '{}' }); // the scripted hub's 502 outcome_unknown, replaced
            window.__mode = 'five'; const res = await fetch('/api/board/x/tasks', { method: 'POST', body: '{}' }); window.__mode = 'unknown';
            return { is502: m.isOutcomeUnknown(502, { code: 'outcome_unknown' }, '/api/board/x/tasks'), is500: m.isOutcomeUnknown(500, { code: 'outcome_unknown' }, '/api/board/x/tasks'), is200: m.isOutcomeUnknown(200, { code: 'outcome_unknown' }, '/api/board/x/tasks'), status500: res.status, body500: await res.json(), r: r.status };
        })()`);
        check('the code only counts on a 502: the same body with another status (an ordinary 500, a 200) is not the line, and the wrapper leaves a 500 alone', other.is502 === true && other.is500 === false && other.is200 === false && other.status500 === 500 && other.body500.error === 'server error' && other.body500.code === 'outcome_unknown', JSON.stringify(other));
        const errOnly = await ev(`(async () => {
            const call = async (method, url) => { const r = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: '{}' }); return { status: r.status, body: await r.json() }; };
            window.__mode = 'errorOnly';
            const task = await call('POST', '/api/board/x/tasks'), launch = await call('POST', '/api/sessions/launch'), team = await call('POST', '/api/sessions/launch-team');
            const m = await import(${S} + 'server_status.js'); const resp = await fetch('/api/board/x/messages', { method: 'POST', body: '{}' }); const text = await m.responseErrorText(resp.clone(), 'local', 'fallback');
            window.__mode = 'unknown';
            return { task, launch, team, text };
        })()`);
        check('the hub\'s launch-style answer (an error of outcome_unknown and no code) on a non-launch write says the one line, with the code added', errOnly.task.status === 502 && errOnly.task.body.error === LINE && errOnly.task.body.code === 'outcome_unknown' && errOnly.text === LINE, JSON.stringify([errOnly.task, errOnly.text]));
        check('and a launch with that body keeps its own words (not replaced)', errOnly.launch.body.error === 'outcome_unknown' && errOnly.team.body.error === 'outcome_unknown' && !('code' in errOnly.launch.body), JSON.stringify([errOnly.launch, errOnly.team]));
        const up = await ev(`(async () => { const m = await import(${S} + 'image_attach.js'); return m.uploadCodeFailure('outcome_unknown'); })()`);
        check('an upload answered outcome_unknown says the line and offers no Retry', up && up.text === LINE && up.retry === false, JSON.stringify(up));

        console.log('2. The composer');
        await ev(`window.__m.add('ou-agent', 'A', 3, { sleeping: false, board_project: ${JSON.stringify(board)} }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('ou-agent', 'claude', 'ou-agent-id', 'local'); true`); await sleep(800); // sleep-ok: a selection settles over frames
        await reset();
        await ev(`(() => { const i = document.getElementById('command-input'); i.value = 'run the tests'; i.dispatchEvent(new Event('input', { bubbles: true })); i.focus(); return true; })()`);
        await ev(`document.getElementById('command-input').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); true`);
        await waitFor(`(document.getElementById('composer-error') && !document.getElementById('composer-error').hidden)`, 'the composer error', 6000);
        const composer = await ev(`({ err: document.querySelector('#composer-error .composer-error-text').textContent, draft: document.getElementById('command-input').value })`);
        check('a send answered outcome_unknown says the one line under the composer, and the draft is kept', composer.err === LINE && composer.draft === 'run the tests', JSON.stringify(composer));
        const sendPosts = (await posts()).filter(p => /\/send/.test(p)).length;
        check('and was sent once', sendPosts === 1, String(sendPosts));

        console.log('3. The team board\'s message box');
        await ev(`(async () => { const m = await import(${S} + 'render.js'); m.showBoardChatTab(${JSON.stringify(board)}); window.switchAgenticTab('board', 'top'); return true; })()`);
        await waitFor(`!!document.getElementById('board-panel-input') && !!document.getElementById('board-panel-input').offsetParent`, 'the box', 8000);
        await reset();
        await ev(`(() => { const i = document.getElementById('board-panel-input'); i.focus(); i.value = 'status please'; return true; })()`);
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' }); await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
        await waitFor(`[...document.querySelectorAll('.toast')].some(t => t.textContent.includes('That may have reached the computer'))`, 'the line', 6000);
        const box = await ev(`({ toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()), draft: document.getElementById('board-panel-input').value })`);
        check('a board message answered outcome_unknown says the one line, and the words are back in the box', box.toasts.some(t => t.startsWith(LINE)) && !box.toasts.some(t => /Could not send to the team/.test(t)) && box.draft === 'status please', JSON.stringify(box));
        const boardPosts = (await posts()).filter(p => /\/messages/.test(p)).length;
        check('and was sent once', boardPosts === 1, String(boardPosts));

        console.log('4. Nothing sends again by itself');
        const before = (await posts()).length;
        await sleep(3500); // sleep-ok: longer than any backoff the dashboard has (the poll and pause timers are seconds); a retry would show as another write
        const after = (await posts()).length;
        check('no write was repeated while the page waited', after === before, `${before} -> ${after}`);
    } finally { await removeBoards(ev, [board]); await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} outcome_unknown checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
