// Roll-up bar, round 2 (#4286, Frontend's review of #4264): (1) chat find reveals a match inside a rolled-up group, (2) a bar formed around an
// open or focused group is open and stays open, (3) a tool run split across a page boundary next to a .work-rollup bar is one group.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const call = (id, label) => [{ type: 'assistant', text: '', tool_uses: [{ name: 'Bash', tool_use_id: id, input_summary: label, description: label }] }, { type: 'tool_result', tool_use_id: id, tool_name: 'Bash', content: 'ok' }];
const NOTICE = "You have 2 unread messages on the message board. Run 'coral-board read' to see them.";
const turn = (n, reply) => [{ type: 'user', content: NOTICE }, ...call('c' + n, 'Read ' + n), { type: 'assistant', text: reply }];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 700, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => { window.__lc = m; return true; })`);
        const vis = sel => ev(`(() => { const el = document.querySelector(${JSON.stringify(sel)}); return !!el && el.checkVisibility({ checkVisibilityCSS: true }); })()`);
        // A fresh transcript box under the id chat find searches (the dashboard's own is in a hidden view).
        const fresh = id => ev(`(() => { document.getElementById(${JSON.stringify(id)})?.remove(); const box = document.createElement('div'); box.id = ${JSON.stringify(id)};
            box.className = 'chat-messages chat-prose'; box.style.cssText = 'position:absolute;left:0;top:0;width:860px;height:600px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(box); return true; })()`);
        const render = (box, msgs) => ev(`window.__lc.renderTranscript(${JSON.stringify(msgs)}, document.getElementById(${JSON.stringify(box)}), 'claude')`);

        // (1) Chat find (it also counts the group's own hidden summary, which repeats the reply's first sentence): a match inside a group folded under a closed bar opens the bar (and shows the match).
        await fresh('live-history-messages');
        await render('live-history-messages', [{ type: 'user', content: 'Watch the batch' }, ...turn(1, 'First reply.'), ...turn(2, 'The zanzibar deploy key was rotated.'), ...turn(3, 'Third reply.')]);
        assert.equal(await ev(`document.querySelectorAll('#live-history-messages > .work-rollup').length`), 1, 'one bar over the three groups');
        assert.equal(await vis('#live-history-messages > .work-rollup + .work-group'), false, 'the groups start hidden under the closed bar');
        await ev(`window.toggleChatFind()`);
        await ev(`(() => { const i = document.querySelector('.chat-find-input'); i.value = 'zanzibar'; i.dispatchEvent(new Event('input')); return true; })()`);
        await waitFor(`/^1 of \\d+$/.test(document.querySelector('.chat-find-count').textContent)`, 'find to count the match inside the closed bar');
        await waitFor(`document.querySelector('#live-history-messages > .work-rollup').open === true`, 'find to open the roll-up bar in front of the match');
        const shown = await ev(`(() => { const hit = [...document.querySelectorAll('#live-history-messages .message-text')].find(e => e.textContent.includes('zanzibar'));
            return { visible: hit.checkVisibility({ checkVisibilityCSS: true }), groupHidden: hit.closest('.work-group').hidden }; })()`);
        assert.deepEqual(shown, { visible: true, groupHidden: false }, 'the match is on screen: ' + JSON.stringify(shown));
        await ev(`window.toggleChatFind()`);
        console.log('   ✓ find reveals a match inside a rolled-up group');

        // (2) A lone group the reader opened keeps the bar open when a second group streams in; so does a group holding focus.
        await fresh('ru-box');
        await render('ru-box', [{ type: 'user', content: 'Go' }, ...turn(4, 'Alone.')]);
        const bar = () => ev(`(() => { const b = document.querySelector('#ru-box > .work-rollup'); return b ? { open: b.open, hidden: [...document.querySelectorAll('#ru-box > .work-group')].map(g => g.hidden) } : null; })()`);
        assert.equal(await bar(), null, 'a lone group has no bar');
        await ev(`document.querySelector('#ru-box > .work-group').open = true`);
        await render('ru-box', turn(5, 'Second.'));
        assert.deepEqual(await bar(), { open: true, hidden: [false, false] }, 'the group the reader had open stays shown when the bar forms around it');
        await render('ru-box', turn(6, 'Third.'));
        assert.deepEqual(await bar(), { open: true, hidden: [false, false, false] }, 'and the bar stays open while the next group streams in');
        await fresh('ru-box');
        await render('ru-box', [{ type: 'user', content: 'Go' }, ...turn(7, 'Alone.')]);
        await ev(`(() => { const s = document.querySelector('#ru-box > .work-group > .work-group-summary'); s.tabIndex = 0; s.focus(); return document.activeElement === s; })()`);
        await render('ru-box', turn(8, 'Second.'));
        assert.deepEqual(await bar(), { open: true, hidden: [false, false] }, 'a group holding focus is not hidden when the bar forms');
        await fresh('ru-box');
        await render('ru-box', [{ type: 'user', content: 'Go' }, ...turn(9, 'Alone.')]);
        await render('ru-box', turn(10, 'Second.'));
        assert.deepEqual(await bar(), { open: false, hidden: [true, true] }, 'control: nothing open or focused, the bar is created closed');
        console.log('   ✓ a bar formed around an open or focused group is open');

        // (3) A tool run split across a page boundary, the newer page opening with a roll-up bar: the split run is one group again.
        // What loadMoreHistory does with an older page: prepend it, stitch the seam, then roll the runs up again.
        // The newer page opens with a notice turn (its first group is the rest of the older page's tool run), then two more, so it opens with a bar.
        const out = await ev(`(async () => { const m = window.__lc; const box = document.createElement('div'); document.body.appendChild(box);
            m.renderTranscript(${JSON.stringify([{ type: 'user', content: NOTICE }, ...call('n1', 'Step three'), ...call('n2', 'Step four'), { type: 'assistant', text: 'Fine.' }, ...turn(12, 'Fine again.'), ...turn(13, 'Fine once more.')])}, box, 'claude');
            const tmp = document.createElement('div');
            m.renderTranscript(${JSON.stringify([{ type: 'user', content: 'Go' }, ...call('o1', 'Step one'), ...call('o2', 'Step two')])}, tmp, 'claude');
            const lastOlder = tmp.lastElementChild; const firstNewer = box.firstChild;
            while (tmp.firstChild) box.insertBefore(tmp.firstChild, firstNewer);
            m.stitchPages(lastOlder); (await import((window.__coralStatic || '/static/') + 'chat_content.js')).rollUpGroups(box);
            const shape = [...box.children].map(c => c.classList.contains('work-rollup') ? 'bar' : c.classList.contains('work-group') ? 'group:' + [...c.querySelectorAll('.tool-call-label')].map(l => l.textContent).join('|') : 'other');
            box.remove(); return shape; })()`);
        assert.deepEqual(out, ['other', 'bar', 'group:Step one|Step two|Step three|Step four', 'group:Read 12', 'group:Read 13'], 'one group for the split run, then the rest, under one bar: ' + JSON.stringify(out));
        console.log('   ✓ stitchPages across a page boundary next to a roll-up bar');
        console.log('\nPASS roll-up round 2');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
