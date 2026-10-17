// Coral's own notices fold into the steps group, and a short reply to a turn a notice started folds with them (#4171, operator). Fixture in
// the shape of the operator's screenshot: a reply to the user, then (unread-messages notice, a board read, a short reply), (health check, a
// read, a short reply), (a notice and a LONG reply), (stop-hook feedback, a read, a short reply), a notice inside the user's own turn, and a
// user message with a short reply. Checks what is folded, what shows, the group labels, the order inside an expanded group, and that
// user messages never fold. Set FOLD_SHOT_DIR to save a screenshot. Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const call = (id, name, label) => [{ type: 'assistant', text: '', tool_uses: [{ name, tool_use_id: id, input_summary: label, description: label }] }, { type: 'tool_result', tool_use_id: id, tool_name: name, content: 'ok' }];
const NOTICE = "You have 2 unread messages on the message board. Run 'coral-board read' to see them.";
const HEALTH = '[Coral health check] Team health check for coral-go-team: 1 unread message include a health-monitor summary.';
const HOOK = 'Stop hook feedback:\n- [board check]: New board messages';
const LONG = 'The health check shows two things that need a decision. First, the review of the notifier change has been waiting for a second reviewer for a long time and nobody has picked it up, so I am going to reassign it. Second, one agent has been idle since the last release and has no task, so I will queue the next item for it as well.';
const fixture = [
    { type: 'user', content: 'Is the Windows failure gone?' },
    ...call('t1', 'Bash', 'Check the release notes'),
    { type: 'assistant', text: 'DevOps confirmed the Windows timing-test fix is in v1.4.71, so that failure goes away once the release lands.' },
    { type: 'user', content: NOTICE },
    ...call('t2', 'Bash', 'Read new board messages'),
    { type: 'assistant', text: 'Both were routine task-queue notices. Nothing needs action.' },
    { type: 'user', content: HEALTH },
    ...call('t3', 'Bash', 'Read health check and list open tasks'),
    { type: 'assistant', text: 'All six agents are working, and each has its next task queued. Nothing is blocked.' },
    { type: 'user', content: NOTICE },
    { type: 'assistant', text: LONG },
    { type: 'user', content: HOOK },
    ...call('t4', 'Bash', 'Read the board'),
    { type: 'assistant', text: 'Nothing new for me.' },
    { type: 'user', content: 'Please check the build' },
    ...call('t5', 'Bash', 'Run the build'),
    { type: 'user', content: NOTICE },
    ...call('t6', 'Bash', 'Read new board messages'),
    { type: 'assistant', text: 'Build is green.' },
    { type: 'user', content: 'Thanks. Anything else?' },
    { type: 'assistant', text: 'No, that is all.' },
];

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => {
            const box = document.createElement('div'); box.id = 'fold-box'; box.className = 'chat-messages chat-prose';
            box.style.cssText = 'position:fixed;left:0;top:0;width:860px;height:880px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(box); m.renderTranscript(${JSON.stringify(fixture)}, box, 'claude'); return true; })`);
        const top = await ev(`(() => { const box = document.getElementById('fold-box');
            const vis = el => el.checkVisibility({ checkVisibilityCSS: true });
            return [...box.children].map(el => el.classList.contains('work-group')
                ? { kind: 'group', open: el.open, count: el.querySelector('.work-group-count').textContent, label: el.querySelector('.work-group-latest').textContent,
                    inside: [...el.querySelectorAll(':scope > .work-group-body > *')].map(c => c.classList.contains('chat-system') ? 'notice' : c.classList.contains('tool-call') ? 'tool' : c.classList.contains('folded-reply') ? 'reply' : c.className) }
                : { kind: el.classList.contains('human') ? 'user' : 'reply', text: (el.querySelector('.message-text') || el).innerText.trim().slice(0, 40), visible: vis(el) }); })()`);
        if (process.env.FOLD_SHOT_DIR) require('node:fs').writeFileSync(`${process.env.FOLD_SHOT_DIR}/chat.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
        const kinds = top.map(x => x.kind).join(',');
        // user, group(t1), reply(DevOps), group[notice,t2,reply], group[health,t3,reply], group[notice], reply(LONG), group[hook,t4,reply], user, group[t5,notice,t6], reply(Build), user, reply
        assert.equal(kinds, 'user,group,reply,group,group,group,reply,group,user,group,reply,user,reply', kinds + '\n' + JSON.stringify(top, null, 1));
        const g = top.filter(x => x.kind === 'group');
        assert.deepEqual(g[1].inside, ['notice', 'tool', 'reply'], 'unread notice, read, reply folded in order');
        assert.equal(g[1].count, '2 steps'); assert.equal(g[1].label, 'Both were routine task-queue notices.');
        assert.deepEqual(g[2].inside, ['notice', 'tool', 'reply']); assert.equal(g[2].count, '2 steps'); assert.equal(g[2].label, 'All six agents are working, and each has its next task queued.');
        assert.deepEqual(g[3].inside, ['notice']); assert.equal(g[3].count, '1 step'); assert.ok(/^You have 2 unread messages/.test(g[3].label), 'a notice alone labels its group with its text');
        assert.deepEqual(g[4].inside, ['notice', 'tool', 'reply']); assert.equal(g[4].label, 'Nothing new for me.'); // stop-hook feedback folds as a notice
        assert.deepEqual(g[5].inside, ['tool', 'notice', 'tool']); assert.equal(g[5].count, '3 steps'); // a notice inside the user's own turn is one of its steps
        assert.ok(g.every(x => !x.open), 'groups are collapsed');
        const replies = top.filter(x => x.kind === 'reply');
        assert.ok(replies.every(r => r.visible), 'every reply on the page is visible: ' + JSON.stringify(replies));
        assert.deepEqual(replies.map(r => r.text.slice(0, 12)), ['DevOps confi', 'The health c', 'Build is gre', 'No, that is '], 'reply to the user, the LONG reply after a notice, the reply inside the user turn, the reply to the next user message');
        assert.equal(top.filter(x => x.kind === 'user').length, 3, 'user messages (not notices) stay');
        // no notice or hook text stands on its own
        assert.equal(await ev(`[...document.getElementById('fold-box').children].some(el => el.classList.contains('chat-system'))`), false, 'no notice pill outside a group');
        assert.equal(await ev(`/Stop hook feedback/.test([...document.querySelectorAll('#fold-box > .chat-bubble.human')].map(e => e.innerText).join('|'))`), false, 'stop-hook feedback is not shown as a user message');
        // Expanded: the notice, the step and the reply, in order
        const opened = await ev(`(() => { const gs = document.querySelectorAll('#fold-box > .work-group'); gs[1].open = true; return gs[1].innerText; })()`);
        const i1 = opened.indexOf('unread messages'), i2 = opened.indexOf('Read new board messages'), i3 = opened.lastIndexOf('Both were routine');
        assert.ok(i1 >= 0 && i2 > i1 && i3 > i2, 'expanding shows the notice, the step, the reply in order: ' + JSON.stringify(opened));
        console.log('   ✓ ' + kinds);
        console.log('\nPASS notices and short notice-turn replies fold into steps');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
