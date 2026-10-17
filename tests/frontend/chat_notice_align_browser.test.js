// #3592 (the operator's request): in the agent chat view, Coral's notices (board and task nudges,
// health checks; steps inside the folded groups since #4171) all share one left edge, and the Esc "Interrupted" note starts at the message column's left edge, where the
// agent's reply text starts, instead of being centred: a short and a long notice share the same
// left x, and each pill still shrinks to its text. Checked at a wide window (the 780px column is
// centred) and a narrow one (the column fills the pane).
const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');

const BASE = require('./test_base').BASE;

const MESSAGES = [
    { type: 'user', content: 'Please tidy the modal spacing.' },
    { type: 'assistant', text: 'Done: the modal sections now have 16 px between them.' },
    { type: 'user', content: 'You have 1 unread message on the message board.' },
    { type: 'assistant', text: 'Reading the board.' },
    { type: 'user', content: "[Task #3592 available] Run 'coral-board task claim 3592' to claim it when your current task is done." },
    { type: 'assistant', text: 'Claimed it.' },
    { type: 'user', content: '[Coral health check] You have been idle for 20 minutes with an assigned task.' },
    { type: 'assistant', text: 'Back on it.' },
    { type: 'user', content: '[Request interrupted by user]' },
    { type: 'assistant', text: 'Stopped.' },
];

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const result = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
        return result.result.value;
    };
    const { waitFor, coralReady, nextFrame } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic||'/static/')`;
    const measure = () => ev(`(() => {
        const C = document.getElementById('history-messages');
        const left = el => Math.round(el.getBoundingClientRect().left);
        C.querySelectorAll(':scope > .work-group').forEach(g => { g.open = true; }); // the notices are steps inside the groups now (#4171)
        const pills = [...C.querySelectorAll('.work-group-body > .chat-system')];
        const short = pills.find(p => p.textContent.includes('unread message'));
        const long = pills.find(p => p.textContent.includes('[Task #3592'));
        const health = C.querySelector('.work-group-body > .chat-system-health');
        const note = C.querySelector(':scope > .chat-note');
        const prose = C.querySelector('.chat-bubble.assistant .message-text');
        return { pane: C.clientWidth, prose: left(prose), short: left(short), long: left(long), health: left(health), note: left(note),
            shortW: Math.round(short.getBoundingClientRect().width), longW: Math.round(long.getBoundingClientRect().width),
            noteAlign: getComputedStyle(note).textAlign };
    })()`);
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        for (const [width, label] of [[1400, 'wide window (centred column)'], [760, 'narrow window (the column fills the pane)']]) {
            await Emulation.setDeviceMetricsOverride({ width, height: 900, deviceScaleFactor: 1, mobile: false });
            await Page.navigate({ url: BASE }); await Page.loadEventFired();
            await coralReady();
            await ev(`(async () => {
                (await import(${S}+'utils.js')).showView('history-session-view');
                (await import(${S}+'render.js')).renderHistoryChat(${JSON.stringify(MESSAGES)});
                return true; })()`);
            await waitFor(`document.querySelectorAll('#history-messages .work-group-body > .chat-system').length === 3 && !!document.querySelector('#history-messages > .chat-note')`, 'the notices');
            await nextFrame();
            const m = await measure();
            console.log(`#3592 ${label}: pane ${m.pane}px`);
            assert.equal(m.short, m.long, `a short and a long notice share the same left x (${JSON.stringify(m)})`);
            assert.equal(m.health, m.short, `the health notice too (${JSON.stringify(m)})`);
            assert.ok(m.short > m.prose, `the notices are steps inside their groups, indented under the group's label (${JSON.stringify(m)})`);
            assert.equal(m.note, m.prose, `the Interrupted note too (${JSON.stringify(m)})`);
            assert.equal(m.noteAlign, 'left', 'the note is left-aligned text');
            assert.ok(m.shortW < m.longW, `each pill shrinks to its text (${m.shortW} < ${m.longW})`);
            console.log('   ✓ short, long and health notices share one left edge inside their groups; the note is at the prose\'s left edge; pills shrink to fit');
        }
        console.log('PASS chat_notice_align_browser');
    } finally {
        await client.close();
    }
})().catch(err => { console.error(err); process.exit(1); });
