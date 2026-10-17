// Two follow-ups to the chat roll-up's round 2 (#4286, review #4294): a bar the reader closed stays closed when another group streams in
// (even over a group they had expanded inside it), and chat find counts one match as one (a group's bar repeats text that is in its body, so
// only the body is searched). Drives the real dashboard over CDP; run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const call = (id, label) => [{ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: id, input_summary: label, description: label }] }, { type: 'tool_result', tool_use_id: id, tool_name: 'Bash', content: 'ok' }];
const NOTICE = "You have 2 unread messages on the message board. Run 'coral-board read' to see them.";
const turn = (n, label, reply) => [{ type: 'user', content: NOTICE }, ...call('c' + n, label), { type: 'assistant', text: reply }];
const fixture = [{ type: 'user', content: 'Watch' }, ...turn(1, 'first-label', 'One.'), ...turn(2, 'second-label unique-xyzzy', 'Two.'), ...turn(3, 'third-label', 'Three.')];

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const ok = name => console.log(`   ✓ ${name}`);
    const vis = sel => `document.querySelector('${sel}').checkVisibility({ checkVisibilityCSS: true })`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('ru-bot', 'A', 3, { sleeping: false }); true`);
        await sleep(600); // sleep-ok: the first live list renders from several answers; no single completion event
        await ev(`window.selectLiveSession('ru-bot', 'claude', 'ru-bot-id', 'local'); true`);
        await waitFor(`(() => { const c = document.getElementById('live-history-messages'); return !!c && c.offsetParent !== null; })()`, 'the live chat', 10000);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => { window.__lc = m; const c = document.getElementById('live-history-messages'); c.innerHTML = ''; m.renderTranscript(${JSON.stringify(fixture)}, c, 'claude'); return true; })`);
        const C = '#live-history-messages';

        // 1. closed by the reader, with an inner group expanded: a streamed group does not reopen it
        await ev(`document.querySelector('${C} .work-rollup').open = true; true`);
        await waitFor(vis(`${C} .work-group`), 'the groups show when the bar opens');
        await ev(`document.querySelector('${C} .work-group').open = true; document.activeElement.blur(); document.querySelector('${C} .work-rollup').open = false; true`);
        await waitFor(`!${vis(`${C} .work-group`)}`, 'the groups hide when the bar closes');
        await ev(`import((window.__coralStatic || '/static/') + 'chat_content.js').then(m => { const c = document.querySelector('${C}'); const u = document.createElement('div'); u.className = 'chat-bubble human'; u.textContent = ${JSON.stringify(NOTICE)}; m.appendWork(c, u, { newGroup: true }); const d = document.createElement('div'); d.className = 'chat-bubble'; d.textContent = 'x'; m.appendWork(c, d); return true; })`);
        assert.equal(await ev(`document.querySelector('${C} .work-rollup').open`), false, 'the reader closed the bar; a streamed group must not reopen it');
        assert.match(await ev(`document.querySelector('${C} .work-rollup .work-group-count').textContent`), /^4 updates/);
        ok('a bar the reader closed stays closed when another group streams in, and counts it');

        // 2. a bar newly formed around a group the reader had open is still open (round 2's rule)
        await ev(`(() => { const c = document.querySelector('${C}'); c.innerHTML = ''; window.__lc.renderTranscript(${JSON.stringify(fixture.slice(0, 5))}, c, 'claude'); const g = c.querySelector('.work-group'); g.open = true; g.querySelector('summary').focus(); return true; })()`);
        await ev(`import((window.__coralStatic || '/static/') + 'chat_content.js').then(m => { const c = document.querySelector('${C}'); const u = document.createElement('div'); u.className = 'chat-bubble human'; u.textContent = ${JSON.stringify(NOTICE)}; m.appendWork(c, u, { newGroup: true }); const d = document.createElement('div'); d.className = 'chat-bubble'; d.textContent = 'x'; m.appendWork(c, d); return true; })`);
        assert.equal(await ev(`document.querySelector('${C} .work-rollup').open`), true, 'a new bar around an open, focused group starts open');
        assert.equal(await ev(`document.activeElement === document.querySelector('${C} .work-group summary')`), true, 'focus kept');
        ok('a bar formed around an open, focused group starts open and keeps focus');

        // 3. find: one match is "1 of 1", the reveal shows it
        await ev(`(() => { const c = document.querySelector('${C}'); c.innerHTML = ''; window.__lc.renderTranscript(${JSON.stringify(fixture)}, c, 'claude'); return true; })()`);
        await ev(`window.toggleChatFind(); true`);
        await ev(`(() => { const i = document.querySelector('.chat-find-input'); i.value = 'unique-xyzzy'; i.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
        await waitFor(`/of/.test(document.querySelector('.chat-find-count').textContent)`, 'the count');
        assert.equal(await ev(`document.querySelector('.chat-find-count').textContent`), '1 of 1', 'one match in a rolled group reads 1 of 1');
        await ev(`window.closeChatFind(); true`);
        ok('chat find counts a match in a rolled group once');
    } catch (e) {
        console.error(e);
        process.exitCode = 1;
    } finally {
        await client.close();
    }
})();
