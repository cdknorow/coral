// The user's chat bubble (#4208). (1) It is wider: at most 85% of the 780px reading column on a
// desktop, 85% of the screen on a phone, still right-aligned with the column and with the same
// padding and radius. (2) Claude Code wraps pasted text in <pasted_content id="N"> ...
// </pasted_content id="N">; the bubble showed the closing tag as text (the sanitizer dropped the
// opening one). A matching pair is removed and the pasted text shown; anything else stays.
// Run against an isolated dev server; set BUBBLE_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const LONG = 'lets also let my chat messages have a larger width, because a long request wraps into a narrow column '
    + 'of short lines that is harder to read than the agent replies around it, which use the whole column.';
// The operator's message from the screenshot, as Claude Code wrote it to the transcript.
const PASTED = '<pasted_content id="3068">\nlets do this.\n\nEvery change -> one other reviewer and related tests run once\n\n'
    + 'Batches of changes get queued up for Devops and they run the test and release in batches\n</pasted_content id="3068">';

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    const shot = async name => {
        if (!process.env.BUBBLE_SHOT_DIR) return;
        const { data } = await Page.captureScreenshot({ format: 'png' });
        require('node:fs').writeFileSync(`${process.env.BUBBLE_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
    };
    const render = msgs => ev(`import(${MOD} + 'live_chat.js').then(m => {
        document.getElementById('bubble-box')?.remove();
        const box = document.createElement('div'); box.id = 'bubble-box'; box.className = 'chat-messages chat-prose';
        box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
        document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'claude'); return true; })`);
    // Each user bubble's box, and the column it sits in: the content box, at most 780px, centred.
    const bubbles = () => ev(`(() => {
        const box = document.getElementById('bubble-box');
        const cs = getComputedStyle(box);
        const inner = box.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
        const col = Math.min(inner, 780);
        const colRight = box.getBoundingClientRect().left + parseFloat(cs.paddingLeft) + (inner + col) / 2;
        return [...box.querySelectorAll('.chat-bubble.human')].map(b => {
            const r = b.getBoundingClientRect(), s = getComputedStyle(b);
            return { width: r.width, right: r.right, col, colRight, padding: s.padding, radius: s.borderTopLeftRadius,
                text: b.querySelector('.message-text').innerText, html: b.querySelector('.message-text').innerHTML };
        });
    })()`);
    const reply = { type: 'assistant', text: 'Sure.' };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });

        // Desktop: a long request fills 85% of the reading column, right-aligned with it.
        await open(1280, 800, false);
        await render([{ type: 'user', content: LONG }, reply, { type: 'user', content: PASTED }, reply, { type: 'user', content: 'short one' }]);
        await shot('desktop');
        let [long, pasted, short] = await bubbles();
        assert.ok(Math.abs(long.width - 0.85 * long.col) <= 1, `desktop: the long bubble is 85% of the ${long.col}px column: ${long.width}`);
        assert.ok(Math.abs(long.right - long.colRight) <= 1, `desktop: right-aligned with the column: ${long.right} vs ${long.colRight}`);
        assert.ok(short.width < 200 && Math.abs(short.right - short.colRight) <= 1, `desktop: a short message stays a small bubble on the right: ${short.width}`);
        assert.equal(long.padding, '10px 16px', 'the same padding');
        assert.equal(long.radius, '18px', 'the same radius');

        // The pasted text is shown, without its wrapper tags (the screenshot's case: only the
        // closing tag was visible, because the sanitizer dropped the opening one).
        assert.ok(!pasted.text.includes('pasted_content') && !pasted.html.includes('pasted_content'), `no wrapper tag: ${pasted.text}`);
        assert.ok(pasted.text.startsWith('lets do this.') && pasted.text.trim().endsWith('release in batches'), `the pasted text itself: ${pasted.text}`);

        // Phone: 85% of the screen's column, still on the right.
        await open(390, 844, true);
        await render([{ type: 'user', content: LONG }, reply]);
        await shot('phone');
        [long] = await bubbles();
        assert.ok(Math.abs(long.width - 0.85 * long.col) <= 1, `phone: 85% of the ${long.col}px column: ${long.width}`);
        assert.ok(Math.abs(long.right - long.colRight) <= 1, `phone: right-aligned: ${long.right} vs ${long.colRight}`);

        // Only a well-formed matching pair is removed.
        const unwrap = cases => ev(`import(${MOD} + 'pasted_content.js').then(m => ${JSON.stringify(cases)}.map(m.unwrapPastedContent))`);
        assert.deepEqual(await unwrap([
            PASTED,
            'see <pasted_content id="1">\nA\n</pasted_content id="1"> then',
            '<pasted_content>no id</pasted_content>',
            'tail of a paste </pasted_content id="3068">',
            '<pasted_content id="1">ids differ</pasted_content id="2">',
            '<pasted_content id="1">never closed',
            'x < y and <b>bold</b> and 2 > 1',
        ]), [
            'lets do this.\n\nEvery change -> one other reviewer and related tests run once\n\nBatches of changes get queued up for Devops and they run the test and release in batches',
            'see \n\nA\n\n then',
            'no id',
            'tail of a paste </pasted_content id="3068">',
            '<pasted_content id="1">ids differ</pasted_content id="2">',
            '<pasted_content id="1">never closed',
            'x < y and <b>bold</b> and 2 > 1',
        ]);
        // A lone closing tag is not a pair: the bubble keeps it as text.
        await render([{ type: 'user', content: 'tail of a paste </pasted_content id="3068">' }]);
        assert.ok((await bubbles())[0].text.includes('</pasted_content id="3068">'), 'a lone closing tag is kept');
        console.log('PASS user chat bubble: 85% of the column on desktop and phone, pasted_content pairs unwrapped, nothing else');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
