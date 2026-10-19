// A pasted image in a user turn is a chip, not text (#4940). Codex's transcript reader gives the message
// attachments [{name: '[Image #1]', file: 'shot.png'}]; the bubble shows each as a chip beside the typed text,
// with no raw '<image ...>' tag and no base64 data URL, and nothing overflows at 390px or at 200% text.
// Run against an isolated dev server; set CHIP_SHOT_DIR to save screenshots.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    const shot = async name => {
        if (!process.env.CHIP_SHOT_DIR) return;
        const { data } = await Page.captureScreenshot({ format: 'png' });
        require('node:fs').writeFileSync(`${process.env.CHIP_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
    };
    const render = msgs => ev(`import(${MOD} + 'live_chat.js').then(m => {
        document.getElementById('chip-box')?.remove();
        const box = document.createElement('div'); box.id = 'chip-box'; box.className = 'chat-messages chat-prose';
        box.style.cssText = 'position:fixed;left:0;top:0;right:0;bottom:0;overflow:auto;background:var(--bg-primary);z-index:99999;padding:12px';
        document.body.appendChild(box); m.renderTranscript(${JSON.stringify(msgs)}, box, 'codex'); return true; })`);
    const bubbles = () => ev(`(() => {
        const box = document.getElementById('chip-box');
        return [...box.querySelectorAll('.chat-bubble.human')].map(b => {
            const r = b.getBoundingClientRect();
            return { text: b.querySelector('.message-text').innerText, html: b.innerHTML, right: r.right, boxRight: box.getBoundingClientRect().right,
                chips: [...b.querySelectorAll('.chat-attachment-chip')].map(c => ({ text: c.innerText, right: c.getBoundingClientRect().right })), bubbleRight: r.right, scroll: box.scrollWidth > box.clientWidth };
        });
    })()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        const msgs = [
            { type: 'user', content: '[Image #1]', attachments: [{ name: '[Image #1]', file: 'shot1.png' }] },
            { type: 'user', content: 'why is this red', attachments: [{ name: '[Image #1]', file: 'a.png' }, { name: '[Image #2]', file: '' }] },
            { type: 'user', content: 'hi <b>x</b>', attachments: [{ name: '<i>[Image #1]</i>', file: 'a"><img src=x>.png' }] },
            { type: 'user', content: 'plain, no attachments' },
        ];
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            await open(w, h, mobile);
            await render(msgs);
            await shot(`chips-${w}`);
            const [one, two, esc, plain] = await bubbles();
            assert.deepEqual(one.chips.map(c => c.text), ['[Image #1] shot1.png'], `${w}: image-only message: one chip with the file name`);
            assert.deepEqual(two.chips.map(c => c.text), ['[Image #1] a.png', '[Image #2]'], `${w}: two chips; a missing file shows the name alone`);
            assert.equal(two.text.trim(), 'why is this red', `${w}: the typed text is kept`);
            assert.equal(esc.chips.length, 1, `${w}: attachment names are escaped`);
            assert.ok(!esc.html.includes('<img') && !esc.html.includes('<i>'), `${w}: no markup from an attachment: ${esc.html}`);
            assert.equal(plain.chips.length, 0, `${w}: a message without attachments has no chips`);
            for (const b of [one, two, esc]) {
                assert.ok(b.chips.every(c => c.right <= b.bubbleRight + 1), `${w}: chips stay inside the bubble`);
                assert.ok(!b.html.includes('base64') && !b.html.includes('&lt;image'), `${w}: no raw image text`);
            }
            assert.ok(!one.scroll, `${w}: no horizontal scroll`);
        }
        // 200% text on a phone.
        await ev(`document.documentElement.style.fontSize = '32px'`);
        await render(msgs);
        const big = await bubbles();
        assert.ok(big.every(b => !b.scroll && b.chips.every(c => c.right <= b.bubbleRight + 1)), '200% text: chips wrap inside the bubble');
        console.log('PASS pasted-image chips: names + files, escaped, inside the bubble at desktop, 390px and 200% text');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
