// A chat turn's origin decides whether it is a Coral notice (#5482): the operator's own message that starts like one ("[Coral ...]", "[Task #4]", "You have tasks available", "Stop hook feedback:")
// was drawn as a purple Coral notice because the page read the words. The server says where a turn came from (coral_notice: true for what Coral delivered, false for what the Operator sent from the
// dashboard); only a turn with no recorded origin is read by its words. Rendered through the chat's own renderTranscript. Run against an isolated dev server.
const { connect } = require('./agent_view_harness');

const WORDS = [
    '[Coral health check] are you still there?',
    '[Coral UI panel request] open the diff',
    '[Task #4477 (high)] review this please',
    '[New Task #12] write the docs',
    'You have 3 unread messages on the message board. Read them.',
    'You have tasks available in Coral: run coral-board task list',
    'Stop hook feedback: the board has news',
    '[Wait resolved] the build finished',
];

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    const check = (label, ok, detail) => { console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); if (!ok) process.exitCode = 1; };
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired();
        const S = `(window.__coralStatic || '/static/')`;
        await waitFor(`!!window.__coralReady`, 'the page');
        const kinds = (msgs) => ev(`import(${S} + 'live_chat.js').then(m => { document.getElementById('cno-box')?.remove(); const box = document.createElement('div'); box.id = 'cno-box'; box.className = 'chat-messages chat-prose';
            document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'claude');
            return [...box.querySelectorAll('.chat-bubble.human, .chat-system')].map(e => e.classList.contains('chat-system') ? 'notice' : 'operator'); })`);
        const turn = (content, over = {}) => ({ type: 'user', content, key: 'k' + Math.random(), timestamp: '2026-10-20T10:00:00Z', ...over });

        for (const w of WORDS) {
            const [asFalse, asTrue, none] = await Promise.all([[turn(w, { coral_notice: false })], [turn(w, { coral_notice: true })], [turn(w)]].map(kinds));
            check(`"${w.slice(0, 34)}": the Operator's (coral_notice false) is a bubble of theirs`, asFalse.join() === 'operator', asFalse.join());
            check('  delivered by Coral (true) is a notice', asTrue.join() === 'notice', asTrue.join());
            check('  no origin recorded: read by its words, as before', none.join() === 'notice', none.join());
        }
        // an ordinary turn is the Operator's however it is flagged or not; a Coral notice with unfamiliar words is still a notice when the server says so
        check('an ordinary message, no origin: a bubble', (await kinds([turn('please review the diff')])).join() === 'operator');
        check('an ordinary message delivered by Coral (true): a notice', (await kinds([turn('Something Coral typed in other words', { coral_notice: true })])).join() === 'notice');
        // a mixed run keeps each turn's own reading
        const mixed = await kinds([turn(WORDS[0], { coral_notice: false }), turn(WORDS[2], { coral_notice: true }), turn('and a plain question'), turn(WORDS[6])]); // (notices in a row roll up into one)
        check(`a mixed transcript reads each turn by its own origin (${mixed.join(', ')})`, mixed.join() === 'operator,notice,operator,notice');
    } finally { try { await client.close(); } catch (_) {} }
})().catch(e => { console.error(e); process.exit(1); });
