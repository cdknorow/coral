// Consecutive collapsed groups roll up into ONE bar (#4264, the operator: "multiple collapsed bars should be rolled up into a single one").
// Fixture: the shape of the operator's screenshot (four notice-started turns in a row), then a user message and a visible reply, one lone
// notice turn (stays a plain group), a notice with a LONG reply that stays on the page, and two more notice turns (a second roll-up).
// Checks the bars, their labels and counts, hidden/visible members, expand and collapse, a visible reply or a user message ending a run,
// and live append (new groups fold into a closed bar, show in an open one, and a reply splits the run). Set ROLLUP_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const call = (id, name, label) => [{ type: 'assistant', text: '', tool_uses: [{ name, tool_use_id: id, input_summary: label, description: label }] }, { type: 'tool_result', tool_use_id: id, tool_name: name, content: 'ok' }];
const NOTICE = "You have 2 unread messages on the message board. Run 'coral-board read' to see them.";
const HEALTH = '[Coral health check] Team health check for coral-go-team: 1 unread message include a health-monitor summary.';
const LONG = 'The health check shows two things that need a decision. First, the review of the notifier change has been waiting for a second reviewer for a long time and nobody has picked it up, so I am going to reassign it. Second, one agent has been idle since the last release and has no task queued, so I am going to give it the next item from the backlog. Neither needs the operator.';
const turn = (n, notice, label, reply) => [{ type: 'user', content: notice }, ...call('c' + n, 'Bash', label), { type: 'assistant', text: reply }];
const fixture = [
    { type: 'user', content: 'Watch the batch for me' },
    ...turn(1, NOTICE, 'Read new board messages', 'The Lead pushed the lint fix onto the batch branch.'),
    ...turn(2, HEALTH, 'Read health check', 'The Frontend Dev peer-reviewed the lint fix and approved it.'),
    ...turn(3, NOTICE, 'Read the board again', 'Team check: batch 1 now passes lint and the secret scan.'),
    { type: 'user', content: NOTICE }, ...call('c4a', 'Bash', 'Find DevOps deploy key request'), ...call('c4b', 'Bash', 'Read the thread'), { type: 'assistant', text: 'Found the request.' },
    { type: 'user', content: 'Thanks, what is left?' },
    { type: 'assistant', text: 'Only the release tag.' },
    ...turn(5, NOTICE, 'Read new board messages', 'Nothing for me.'),
    { type: 'user', content: 'Anything else?' },
    { type: 'user', content: NOTICE }, { type: 'assistant', text: LONG },
    ...turn(7, NOTICE, 'Read new board messages', 'Routine.'),
    ...turn(8, HEALTH, 'Read health check', 'All six agents are working.'),
];
const more = [ // the live stream: another notice turn, then a user message and its reply
    ...turn(9, NOTICE, 'Read new board messages', 'Nothing new.'),
    { type: 'user', content: 'Good. Ship it.' }, { type: 'assistant', text: 'Shipping.' },
];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 700, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => {
            window.__lc = m;
            const box = document.createElement('div'); box.id = 'ru-box'; box.className = 'chat-messages chat-prose';
            box.style.cssText = 'position:fixed;left:0;top:0;width:860px;height:680px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(box); m.renderTranscript(${JSON.stringify(fixture)}, box, 'claude'); return true; })`);
        const shape = () => ev(`(() => { const box = document.getElementById('ru-box');
            const vis = el => el.checkVisibility({ checkVisibilityCSS: true });
            return [...box.children].map(el => el.classList.contains('work-rollup')
                ? { kind: 'rollup', open: el.open, count: el.querySelector('.work-group-count').textContent, label: el.querySelector('.work-group-latest').textContent, visible: vis(el) }
                : el.classList.contains('work-group')
                ? { kind: 'group', open: el.open, visible: vis(el), count: el.querySelector('.work-group-count').textContent }
                : { kind: el.classList.contains('human') ? 'user' : 'reply', visible: vis(el) }); })()`);
        const kinds = t => t.map(x => x.kind).join(',');
        let top = await shape();
        // user, [rollup: g1..g4], user, reply, g5 (alone), user, g6 (a notice alone), LONG reply, [rollup: g7, g8]
        assert.equal(kinds(top), 'user,rollup,group,group,group,group,user,reply,group,user,group,reply,rollup,group,group', JSON.stringify(top, null, 1));
        assert.equal(top[1].count, '4 updates · 9 steps', 'the count of updates and the steps in all of them');
        assert.equal(top[1].label, 'Found the request.', 'the newest group\'s label');
        assert.equal(top[12].count, '2 updates · 4 steps'); assert.equal(top[12].label, 'All six agents are working.');
        assert.ok(top[1].visible && top[12].visible, 'the bars show');
        assert.ok(top.slice(2, 6).every(g => !g.visible) && top.slice(13).every(g => !g.visible), 'the groups in a closed bar are hidden');
        assert.ok(top[8].visible && top[10].visible, 'a group alone (no neighbour group) is a plain visible bar, not rolled up');
        assert.equal(await ev(`document.querySelectorAll('#ru-box > .work-rollup').length`), 2, 'exactly two roll-up bars: a run of one gets none');
        assert.ok(top.filter(x => x.kind === 'reply').every(r => r.visible), 'visible replies stay visible');
        console.log('   ✓ ' + kinds(top));
        if (process.env.ROLLUP_SHOT_DIR) require('node:fs').writeFileSync(`${process.env.ROLLUP_SHOT_DIR}/closed.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));

        // Expand the first bar: its groups show, each still collapsed and expandable with its Coral pill.
        await ev(`document.querySelector('#ru-box > .work-rollup').open = true`);
        await waitFor(`document.querySelectorAll('#ru-box > .work-rollup + .work-group')[0].checkVisibility({ checkVisibilityCSS: true })`, 'the groups to show when the bar opens');
        top = await shape();
        assert.ok(top.slice(2, 6).every(g => g.visible && !g.open), 'an open bar shows its groups, each collapsed as today');
        assert.ok(top.slice(13).every(g => !g.visible), 'the other bar is unaffected');
        assert.deepEqual(top.slice(2, 6).map(g => g.count), ['2 steps', '2 steps', '2 steps', '3 steps']);
        const pill = await ev(`(() => { const g = document.querySelectorAll('#ru-box > .work-group')[1]; g.open = true; const p = g.querySelector('.work-group-body > .chat-system');
            return { icon: p.querySelector('.material-icons').textContent.trim(), label: p.querySelector('.chat-system-label').textContent.trim(), visible: p.getBoundingClientRect().height > 0 }; })()`);
        assert.deepEqual(pill, { icon: 'health_and_safety', label: 'Coral · Health check', visible: true }, 'expanding an inner group shows its Coral pill');
        if (process.env.ROLLUP_SHOT_DIR) require('node:fs').writeFileSync(`${process.env.ROLLUP_SHOT_DIR}/open.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
        // Collapse the bar: everything inside hides again, including the group left expanded.
        await ev(`document.querySelectorAll('#ru-box > .work-group')[1].open = false; document.querySelector('#ru-box > .work-rollup').open = false`);
        await waitFor(`!document.querySelectorAll('#ru-box > .work-rollup + .work-group')[0].checkVisibility({ checkVisibilityCSS: true })`, 'the groups to hide when the bar closes');
        assert.ok((await shape()).slice(2, 6).every(g => !g.visible), 'a closed bar hides its groups');
        console.log('   ✓ expand and collapse');

        // Live: the run after the LONG reply is bar 2 (g7, g8). A new notice turn (g9) joins it, label and counts updated, the page not growing.
        const h0 = await ev(`document.getElementById('ru-box').scrollHeight`);
        await ev(`window.__lc.renderTranscript(${JSON.stringify(more.slice(0, 4))}, document.getElementById('ru-box'), 'claude')`);
        top = await shape();
        assert.equal(kinds(top).replace(/^.*,reply,/, ''), 'rollup,group,group,group', 'the new group joins the closed bar: ' + kinds(top));
        const bar2 = top[12];
        assert.equal(bar2.count, '3 updates · 6 steps'); assert.equal(bar2.label, 'Nothing new.');
        assert.ok(top.slice(13).every(g => !g.visible), 'the new group folds in hidden under the closed bar');
        assert.equal(await ev(`document.getElementById('ru-box').scrollHeight`), h0, 'a closed bar absorbs the new group: the page does not grow');
        // Open bar 2, then another group streams in: it shows at once.
        await ev(`document.querySelectorAll('#ru-box > .work-rollup')[1].open = true`);
        await waitFor(`document.querySelectorAll('#ru-box > .work-rollup')[1].nextElementSibling.checkVisibility({ checkVisibilityCSS: true })`, 'bar 2 to open');
        await ev(`window.__lc.renderTranscript(${JSON.stringify(turn(10, HEALTH, 'Read health check', 'Still fine.'))}, document.getElementById('ru-box'), 'claude')`);
        top = await shape();
        assert.equal(top[12].count, '4 updates · 8 steps'); assert.equal(top[12].label, 'Still fine.');
        assert.ok(top.slice(13).every(g => g.visible), 'an open bar shows the group that streams in: ' + JSON.stringify(top.slice(12)));
        assert.equal(top[12].open, true, 'the bar the operator opened stays open');
        // A user message and its reply end the run; the next notice turn starts a fresh group, alone: no bar.
        await ev(`window.__lc.renderTranscript(${JSON.stringify(more.slice(4))}, document.getElementById('ru-box'), 'claude')`);
        await ev(`window.__lc.renderTranscript(${JSON.stringify(turn(11, NOTICE, 'Read new board messages', 'Fine.'))}, document.getElementById('ru-box'), 'claude')`);
        top = await shape();
        assert.equal(kinds(top).replace(/^.*?,reply,group,user,group,reply,/, ''), 'rollup,group,group,group,group,user,reply,group', 'a user message ends the run; the next group is alone: ' + kinds(top));
        assert.equal(await ev(`document.querySelectorAll('#ru-box > .work-rollup').length`), 2, 'no bar for a single group after the user message');
        assert.ok(top[top.length - 1].visible, 'the lone group after the user message is visible');
        console.log('   ✓ live append');

        // Older page prepended: runs that meet at the seam become one bar (the rollUpGroups pass), with the bar's open state kept.
        await ev(`(() => { const box = document.createElement('div'); box.id = 'ru-box2'; box.className = 'chat-messages chat-prose';
            box.style.cssText = 'position:fixed;left:0;top:0;width:860px;height:400px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(box); const tmp = document.createElement('div');
            window.__lc.renderTranscript(${JSON.stringify([...turn(20, NOTICE, 'a', 'One.'), ...turn(21, NOTICE, 'b', 'Two.')])}, tmp, 'claude');
            window.__lc.renderTranscript(${JSON.stringify([...turn(22, NOTICE, 'c', 'Three.')])}, box, 'claude');
            const first = box.firstChild; while (tmp.lastChild) box.insertBefore(tmp.lastChild, first);
            return true; })()`);
        const seam = () => ev(`(() => { const b = document.querySelectorAll('#ru-box2 > .work-rollup'); return { bars: b.length, label: b[0] && b[0].querySelector('.work-group-count').textContent, hidden: [...document.querySelectorAll('#ru-box2 > .work-group')].map(g => g.hidden) }; })()`);
        assert.deepEqual(await seam(), { bars: 1, label: '2 updates \u00b7 4 steps', hidden: [true, true, false] }, 'before the seam pass: the older bar and a loose group');
        await ev(`import((window.__coralStatic || '/static/') + 'chat_content.js').then(m => m.rollUpGroups(document.getElementById('ru-box2')))`);
        assert.deepEqual(await seam(), { bars: 1, label: '3 updates \u00b7 6 steps', hidden: [true, true, true] }, 'after it: one bar over all three');
        console.log('   \u2713 older page joined at the seam');
        console.log('\nPASS consecutive collapsed groups roll up into one bar');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
