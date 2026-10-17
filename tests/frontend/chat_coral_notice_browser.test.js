// Coral's own notices are never drawn as the Operator's chat bubbles (#4306). The transcript records what Coral types into an agent as a user
// turn: (1) every notice form is read by its text, also when a long one arrives as a paste (Claude Code wraps it in pasted_content tags, which
// the old check did not look past); (2) a turn the server flags coral_notice (the automated-input gate delivered it) is a notice whatever its
// text; (3) what the operator typed stays a bubble, even when it quotes a notice. Drives the real dashboard over CDP.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const FORMS = [
    "[Task #4300 completed by QA Engineer] Outcome: success. Live smoke of v1.4.75: ALL 4 CASES PASS.\nScreenshots: coral://artifacts/7ce9 (banner on).",
    "[Task #4301 in review] Review the patch.", "[Task #4302 approved by QA Engineer] Approved.", "[Task #4303 rejected by QA Engineer] Needs a test.",
    "[Task #4304 cancelled] Not needed.", "[Task #4305 reassigned to you] Fix it — run 'coral-board task claim 4305' to start",
    "[Task #4306 resubmitted] New patch.", "@Orchestrator [Task #4307 (high)] Do the thing — assigned to you, run 'coral-board task claim 4307' to start",
    "@notify-all [New Task #4308 (medium)] Audit — run 'coral-board task claim' to pick it up",
    "[Task #4309 reminder] Still in progress. Continue it.", "You have 2 unread messages on the message board. Run 'coral-board read' to see them.",
    "[Coral health check] Team health check for coral-go-team: 1 unread message include a health-monitor summary.",
    "[Wait resolved] \"QA Engineer\" posted on 'coral-go-team': \"done\". Run 'coral-board read' to see the message.",
];
const wrap = t => `<pasted_content id="1">\n${t}\n</pasted_content id="1">`;
const OPERATOR = 'Operator: keep going';
// operator turns between the notices, so consecutive notices do not roll up into one pill
const interleave = texts => texts.flatMap(t => [{ type: 'user', content: OPERATOR }, { type: 'user', content: t }]);

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 900, height: 700, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'live_chat.js').then(m => { window.__lc = m; return true; })`);
        const fresh = () => ev(`(() => { document.getElementById('nt-box')?.remove(); const b = document.createElement('div'); b.id = 'nt-box'; b.className = 'chat-messages chat-prose';
            b.style.cssText = 'position:absolute;left:0;top:0;width:860px;height:600px;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px'; document.body.appendChild(b); return true; })()`);
        const render = msgs => ev(`window.__lc.renderTranscript(${JSON.stringify(msgs)}, document.getElementById('nt-box'), 'claude')`);
        const count = () => ev(`({ bubbles: [...document.querySelectorAll('#nt-box .chat-bubble.human .message-text')].map(e => e.textContent.trim().slice(0, 60)),
            pills: document.querySelectorAll('#nt-box .chat-system').length })`);

        // 1. every form, plain and as a paste
        for (const [label, wrapIt] of [['plain', t => t], ['pasted', wrap]]) {
            await fresh();
            await render(interleave(FORMS.map(wrapIt)));
            const c = await count();
            assert.equal(c.pills, FORMS.length, `${label}: every notice form is a Coral notice (${c.pills} of ${FORMS.length}); bubbles: ${JSON.stringify(c.bubbles)}`);
            assert.deepEqual([...new Set(c.bubbles)], [OPERATOR], `${label}: the only bubbles are the operator's own: ${JSON.stringify(c.bubbles)}`);
            assert.equal(c.bubbles.length, FORMS.length, `${label}: each operator turn is still a bubble`);
            console.log(`   ✓ ${FORMS.length} notice forms, ${label}`);
        }

        // 2. provenance: the server's flag wins over the text
        const odd = 'A notice in a form no pattern has ever heard of: the build is green.';
        await fresh();
        await render([{ type: 'user', content: OPERATOR }, { type: 'user', content: odd, coral_notice: true }, { type: 'user', content: OPERATOR }, { type: 'user', content: odd }]);
        let c = await count();
        assert.equal(c.pills, 1, 'the flagged turn is a notice');
        assert.equal(c.bubbles.length, 3, 'the same text without the flag is the operator\'s bubble: ' + JSON.stringify(c.bubbles));
        assert.ok(c.bubbles[2].startsWith('A notice in a form'), 'the unflagged copy stays a bubble');
        const pillText = await ev(`document.querySelector('#nt-box .chat-system-text').textContent`);
        assert.equal(pillText, odd, 'the notice shows its text');
        console.log('   ✓ a delivery flag wins over the text');

        // 3. the operator's own words stay a bubble, even quoting a notice
        await fresh();
        const quotes = ['please check "[Task #4300 completed by QA Engineer] Outcome: success" again', 'what does You have 2 unread messages on the message board mean?', 'Task #12 completed? tell me'];
        await render(quotes.map(q => ({ type: 'user', content: q })));
        c = await count();
        assert.equal(c.pills, 0, 'no notice from the operator\'s own words');
        assert.equal(c.bubbles.length, quotes.length);
        console.log('   ✓ the operator\'s own messages stay bubbles');

        // 4. live append: a flagged turn arriving after the page is drawn is a notice too
        await render([{ type: 'user', content: OPERATOR }, { type: 'user', content: wrap(FORMS[0]) }, { type: 'user', content: OPERATOR }, { type: 'user', content: odd, coral_notice: true }]);
        c = await count();
        assert.equal(c.pills, 2, 'a streamed pasted notice and a streamed flagged turn are both notices');
        assert.equal(c.bubbles.length, quotes.length + 2, 'only the two operator turns were added as bubbles');
        console.log('   ✓ live append');
        console.log('\nPASS Coral notices are never the operator\'s bubbles');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
