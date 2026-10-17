// Only tool calls fold into a "N steps" group (#4105): the agent's own prose is never folded away. A turn that goes on after the agent's
// answer (a Stop hook continuation, or a message sent mid-turn, then more tool calls) used to demote the earlier answer into the next
// group, hiding it behind "5 steps". Now each answer stays on the page and splits the steps around it. Fixture: answer, a Stop hook
// feedback continuation, tool calls, a second answer, more tool calls, a final answer; and the same without any entry between.
// Run against an isolated dev server; set STEPS_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const tool = (id, name, summary, label) => ({ name, tool_use_id: id, input_summary: summary, description: label });
const call = (id, name, label) => [{ type: 'assistant', text: '', tool_uses: [tool(id, name, label, label)] }, { type: 'tool_result', tool_use_id: id, tool_name: name, content: 'ok' }];
const A1 = 'Yes, that is the bug Backend Dev has as task #4104. The cause is the bug report box in the agent terminal.';
const A2 = 'Checking the new ones myself before passing them on.';
const A3 = 'Done: the notification is held until the box closes.';
const fixture = withHook => [
    { type: 'user', content: 'Is this the same bug as the one in the screenshot?' },
    ...call('t1', 'Bash', 'Download the reposted mockups'), ...call('t2', 'Bash', 'Clarify the #4104 trigger for Backend'),
    { type: 'assistant', text: A1 },
    ...(withHook ? [{ type: 'user', content: 'Stop hook feedback:\n- [board check]: New board messages' }] : []),
    ...call('t3', 'Bash', 'Read new board messages'),
    { type: 'assistant', text: A2 },
    ...call('t4', 'Read', 'static/render.js'), ...call('t5', 'Grep', 'collapse'),
    { type: 'assistant', text: A3 },
];

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        const render = (msgs, id) => ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => {
            document.getElementById(${JSON.stringify(id)})?.remove();
            const box = document.createElement('div'); box.id = ${JSON.stringify(id)}; box.className = 'chat-messages chat-prose';
            box.style.cssText = 'position:fixed;left:0;top:0;width:860px;height:880px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
            document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'claude'); return true; })`);
        const shape = id => ev(`(() => { const box = document.getElementById(${JSON.stringify(id)});
            const vis = el => el.checkVisibility({ checkOpacity: false, checkVisibilityCSS: true });
            const top = [...box.children].map(el => el.classList.contains('work-group')
                ? { kind: 'group', open: el.open, steps: el.querySelector('.work-group-count').textContent, bubbles: el.querySelectorAll(':scope > .work-group-body > .chat-bubble').length }
                : { kind: el.classList.contains('human') ? 'user' : 'reply', text: (el.querySelector('.message-text') || el).innerText.trim(), visible: vis(el) });
            return top; })()`);
        const shot = async name => {
            if (!process.env.STEPS_SHOT_DIR) return;
            const { data } = await Page.captureScreenshot({ format: 'png' });
            require('node:fs').writeFileSync(`${process.env.STEPS_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
        };

        for (const withHook of [true, false]) { await render(fixture(withHook), withHook ? 'steps-hook' : 'steps-plain'); await shot(withHook ? 'hook' : 'plain'); } // pictures first, so a failing run still leaves them
        for (const withHook of [true, false]) {
            const id = withHook ? 'steps-hook' : 'steps-plain';
            await render(fixture(withHook), id);
            const t = await shape(id);
            const kinds = t.map(x => x.kind).join(',');
            assert.equal(kinds, withHook ? 'user,group,reply,user,group,reply,group,reply' : 'user,group,reply,group,reply,group,reply', `${id}: groups and answers alternate: ${kinds}`);
            const replies = t.filter(x => x.kind === 'reply' && x.text.startsWith('Yes') || x.text?.startsWith('Checking') || x.text?.startsWith('Done'));
            assert.equal(replies.length, 3, `${id}: all three answers are on the page, outside any group`);
            assert.ok(replies.every(r => r.visible), `${id}: every answer is visible with nothing expanded`);
            assert.ok(t.filter(x => x.kind === 'group').every(g => !g.open && g.bubbles === 0), `${id}: groups are collapsed and hold only tool calls`);
            const counts = t.filter(x => x.kind === 'group').map(g => g.steps);
            assert.deepEqual(counts, withHook ? ['2 steps', '1 step', '2 steps'] : ['2 steps', '1 step', '2 steps'], `${id}: step counts ${counts}`);
            console.log(`   ✓ ${id}: ${kinds}; counts ${counts.join(', ')}`);
        }

        // Today's look for a plain run of tool calls: one group, collapsed, then the answer.
        await render([{ type: 'user', content: 'go' }, ...call('a', 'Bash', 'one'), ...call('b', 'Read', 'two'), ...call('c', 'Grep', 'three'), { type: 'assistant', text: 'All done.' }], 'steps-run');
        const run = await shape('steps-run');
        assert.deepEqual(run.map(x => x.kind), ['user', 'group', 'reply']);
        assert.equal(run[1].steps, '3 steps'); assert.equal(run[1].open, false);
        console.log('   ✓ a plain run of tool calls is one collapsed group, as before');

        // A message sent mid-turn ends the group too.
        await render([{ type: 'user', content: 'go' }, ...call('a', 'Bash', 'one'), { type: 'assistant', text: 'Working on it.' }, { type: 'user', content: 'also check the tests' }, ...call('b', 'Bash', 'two'), { type: 'assistant', text: 'Both done.' }], 'steps-mid');
        assert.equal((await shape('steps-mid')).map(x => x.kind).join(','), 'user,group,reply,user,group,reply');
        console.log('   ✓ a user message sent mid-turn splits the groups and the earlier answer stays visible');
        console.log('\nPASS only tool calls fold into steps');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
