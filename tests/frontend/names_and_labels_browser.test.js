// Names and labels (#297, QA's #278 rows 5-9), against an isolated dev server with stubbed artifact metadata:
//  5. a team name with a non-ASCII letter is refused with a rule that says which letters (English A-Z), and the text shows once;
//  6. the team's name, not its id (b-...), in the group's "More actions" label; no id in the composer's placeholder;
//  7. an artifact link on the board shows the name it was stored under (as text, without controls or reordering characters),
//     and the Inbox's links too;
//  8. a short (or 200%) window: a multi-line draft never runs under the Model and Default chips; the box scrolls instead;
//  9. a chat bubble keeps single line breaks, as the board does.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const HEX = 'a1'.repeat(32), HOSTILE = 'b2'.repeat(32), NONAME = 'c3'.repeat(32);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); true`);
        await ev(`(() => { const R = window.fetch; const json = b => new Response(JSON.stringify(b), { status: 200, headers: { 'content-type': 'application/json' } });
            const names = { ${JSON.stringify(HEX)}: 'Ñotas 日本語.txt', ${JSON.stringify(HOSTILE)}: '<img src=x onerror=alert(1)>\\u202efdp.exe', ${JSON.stringify(NONAME)}: '' };
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = /^\\/api\\/artifacts\\/([0-9a-f]{64})\\/meta$/.exec(url.pathname);
                if (m && m[1] in names) return Promise.resolve(json(names[m[1]] ? { digest: m[1], name: names[m[1]] } : { digest: m[1] }));
                if (url.pathname === '/api/agents/models') return Promise.resolve(json({ agent_type: 'claude', models: [{ value: 'claude-opus-5-5', display_name: 'Opus 5.5', efforts: ['low', 'high'] }], thinking: ['low', 'high'] }));
                return R(u, o); };
            return true; })()`);
        const S = `(window.__coralStatic || '/static/')`;

        console.log('5. a non-ASCII team name: the rule says which letters, once');
        const rule = await ev(`(async () => (await import(${S} + 'utils.js')).boardNameProblem('Ñandú-Team'))()`);
        assert.match(rule, /English letters \(A-Z\)/, rule);
        assert.match(rule, /no accents/, rule);
        assert.equal(await ev(`(async () => (await import(${S} + 'utils.js')).boardNameProblem('Sprint-42'))()`), '');

        console.log('6. the team name, not b-..., in the group menu label and the placeholder');
        await ev(`(async () => { const sb = await import(${S} + 'server_base.js'); sb.noteBoardLabel('', 'b-q7x2k9m4pz', 'Ñandú Team');
            window.__m.add('lbl-a', 'A', 3, { sleeping: false, agent_type: 'claude', board_project: 'b-q7x2k9m4pz' });
            localStorage.setItem('coral-group-by-team', 'true'); T.render.renderLiveSessions(T.state.liveSessions); return true; })()`);
        await waitFor(`!!document.querySelector('.group-kebab-btn[aria-label^="More actions for"]')`, 'the team card', 4000);
        const labels = await ev(`[...document.querySelectorAll('.group-kebab-btn')].map(b => b.getAttribute('aria-label')).concat([...document.querySelectorAll('.group-kebab .sidebar-kebab-menu')].map(m => m.getAttribute('aria-label')))`);
        assert.ok(labels.includes('More actions for Ñandú Team'), JSON.stringify(labels));
        assert.ok(labels.includes('Actions for Ñandú Team'), JSON.stringify(labels));
        assert.ok(!labels.some(l => /b-q7x2k9m4pz/.test(l)), 'no board id in the labels: ' + JSON.stringify(labels));
        const ph = await ev(`(async () => (await import(${S} + 'session_state.js')).commandPlaceholder('Dev', 'claude', 'b-q7x2k9m4pz'))()`);
        assert.ok(!ph.includes('b-q7x2k9m4pz'), 'no board id in the placeholder (#5991 dropped the board from it): ' + ph);

        console.log('7. artifact links show the stored name, as text');
        const body = await ev(`(async () => { const b = await import(${S} + 'board_message_text.js'); const host = document.createElement('div'); host.id = 'qa-board-body'; document.body.appendChild(host);
            host.innerHTML = b.renderBoardMessageBody({ id: 97001, content: 'report coral://artifacts/${HEX} and coral://artifacts/${HOSTILE} and coral://artifacts/${NONAME}' }); return host.querySelectorAll('a.mb-artifact-ref').length; })()`);
        assert.equal(body, 3, 'three artifact links');
        await waitFor(`[...document.querySelectorAll('#qa-board-body a.mb-artifact-ref')][0].textContent === 'Ñotas 日本語.txt'`, 'the stored name on the board', 4000);
        await waitFor(`[...document.querySelectorAll('#qa-board-body a.mb-artifact-ref')][1].textContent !== 'artifact b2b2b2b2'`, 'the second name', 4000);
        const links = await ev(`[...document.querySelectorAll('#qa-board-body a.mb-artifact-ref')].map(a => ({ text: a.textContent, title: a.title, imgs: a.querySelectorAll('img').length }))`);
        assert.equal(links[1].text, '<img src=x onerror=alert(1)>fdp.exe', 'a hostile name is text, without the reordering character: ' + JSON.stringify(links[1]));
        assert.equal(links[1].imgs, 0, 'no markup from a name');
        assert.equal(await ev(`document.querySelectorAll('#qa-board-body img').length`), 0);
        assert.equal(links[2].text, 'artifact c3c3c3c3', 'no stored name: the short id');
        assert.equal(links[0].title, 'coral://artifacts/' + '${HEX}'.replace('${HEX}', 'a1'.repeat(32)), 'the full reference is the tooltip');
        // a later render of the same body uses the name at once
        assert.match(await ev(`(async () => (await import(${S} + 'board_message_text.js')).renderBoardMessageBody({ id: 97002, content: 'again coral://artifacts/${HEX}' }))()`), /Ñotas 日本語\.txt/);
        const inbox = await ev(`(async () => { const p = await (await import(${S} + 'inbox_coral.js')).artifactLinkPolicy('see coral://artifacts/${HEX}'); return p('coral://artifacts/${HEX}'); })()`);
        assert.equal(inbox.shown, 'Ñotas 日本語.txt', 'the Inbox link (and the viewer it opens) shows the name');

        console.log('8. a short window: a long draft scrolls inside the box, never under the chips');
        await ev(`(() => { const a = window.__m.agents['lbl-a-id']; T.ws.handleCoralMessage({ type: 'coral_diff', changed: [{ ...window.__m.entry(a), agent_type: 'claude', model: 'claude-opus-5-5', thinking_actual: 'high' }] }); return true; })()`);
        await select('lbl-a');
        await waitFor(`!document.getElementById('model-chip-bar').hidden`, 'the chips', 6000);
        for (const [w, h, s] of [[1280, 400, 1], [960, 470, 2], [1440, 900, 1]]) {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: s, mobile: false });
            for (const lines of [3, 12]) {
                await ev(`(() => { const i = document.getElementById('command-input'); i.focus(); i.value = ${JSON.stringify(Array.from({ length: 12 }, (_, k) => 'line ' + (k + 1)).join('\n'))}.split('\\n').slice(0, ${lines}).join('\\n'); i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
                await ev(`new Promise(r => setTimeout(r, 250))`); // sleep-ok: the box sizes itself on the input event, then the layout settles
                const g = await ev(`(() => { const ta = document.getElementById('command-input').getBoundingClientRect(), tb = document.getElementById('command-toolbar').getBoundingClientRect(), chip = document.getElementById('model-chip').getBoundingClientRect();
                    const t = document.getElementById('command-input'); return { taBottom: ta.bottom, tbTop: tb.top, chipTop: chip.top, scrolls: t.scrollHeight > t.clientHeight + 1 }; })()`);
                assert.ok(g.taBottom <= g.tbTop + 1 && g.taBottom <= g.chipTop + 1, `${w}x${h}@${s}, ${lines} lines: the box ends above the chips ${JSON.stringify(g)}`);
            }
        }
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await ev(`(() => { const i = document.getElementById('command-input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);

        console.log('9. a chat bubble keeps single line breaks');
        const bubble = await ev(`(async () => { const c = await import(${S} + 'chat_messages.js'); const host = document.createElement('div'); document.body.appendChild(host);
            c.renderMessage({ type: 'user', content: 'first line\\nsecond line' }, host); c.renderMessage({ type: 'assistant', text: 'one\\ntwo' }, host);
            const out = [...host.querySelectorAll('.message-text')].map(e => ({ brs: e.querySelectorAll('br').length, text: e.innerText })); host.remove(); return out; })()`);
        assert.ok(bubble.length === 2 && bubble.every(b => b.brs === 1), 'each bubble keeps its line break: ' + JSON.stringify(bubble));
        console.log('PASS names and labels: team-name rule, labels by team name, artifact names, the short-window composer, chat line breaks');
    } finally {
        await client.close();
    }
})().catch(e => { console.error(e.stack || String(e)); process.exit(1); });
