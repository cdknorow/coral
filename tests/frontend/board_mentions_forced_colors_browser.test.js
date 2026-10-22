// @mentions of a board member in forced colors (Windows High Contrast, #5964 review): the browser drops the tint and the underline bar, so a mention is a bordered word and the viewer's own name is also
// underlined: still told apart from the text around it without colour. The text is rendered by the board's own renderer; nothing else about it is checked here (board_mentions_browser does).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 800, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`import((window.__coralStatic || '/static/') + 'utils.js').then(u => { u.setBoardMentionNames(['Orchestrator', 'QA Engineer']); const d = document.createElement('div'); d.id = 'mt'; d.innerHTML = u.renderBoardMessageBody({ id: 1, content: 'hello @QA Engineer and @Operator, plain words' }); document.body.prepend(d); return true; })`);
        const read = () => ev(`(() => { const q = c => { const e = document.querySelector('#mt .' + c), cs = getComputedStyle(e); return { bw: cs.borderTopWidth, bs: cs.borderTopStyle, ul: cs.textDecorationLine, fw: cs.fontWeight }; }; return { m: q('mb-mention:not(.mb-mention-self)'), self: q('mb-mention-self') }; })()`);
        let r = await read();
        assert.equal(r.m.bw, '0px', 'outside forced colors a mention has no border (the tint carries it)');
        await Emulation.setEmulatedMedia({ features: [{ name: 'forced-colors', value: 'active' }] });
        r = await read();
        assert.ok(r.m.bs === 'solid' && parseFloat(r.m.bw) >= 1, 'a mention is bordered in forced colors: ' + JSON.stringify(r.m));
        assert.equal(r.self.ul, 'underline', 'the viewer\'s own name is underlined too: ' + JSON.stringify(r.self));
        assert.ok(Number(r.m.fw) >= 600, 'and bold');
        console.log('PASS mentions are told apart in forced colors');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
