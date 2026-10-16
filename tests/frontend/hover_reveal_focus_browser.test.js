// Controls that appear on hover must also appear when a keyboard user focuses them (#4026 follow-up): the file row's copy/preview/edit
// buttons, a chat bubble's Edit, a board message's delete and the agent list's kebab were opacity 0 until the mouse was over the row, so
// focusing one showed nothing. Built from the dashboard's own class structure; the rules are the real stylesheet's.
// Run against an isolated dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

(async () => {
    const { client, Page, Emulation, ev, PRELOAD, install } = await connect();
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        // [host markup class, the hidden control's selector inside it, the focusable button inside]
        const cases = [
            ['file row', '<div class="file-item"><div class="file-action-btns"><button class="file-action-btn" id="hr-btn">c</button></div></div>', '.file-action-btns'],
            ['chat bubble', '<div class="chat-bubble"><button class="edit-btn" id="hr-btn">Edit</button></div>', '.edit-btn'],
            ['board message', '<div class="mb-message"><button class="mb-delete-msg-btn" id="hr-btn">x</button></div>', '.mb-delete-msg-btn'],
            ['agent list row', '<ul class="session-list"><li><button class="sidebar-kebab-btn" id="hr-btn">...</button></li></ul>', '.sidebar-kebab-btn'],
        ];
        for (const [name, html, hidden] of cases) {
            const r = await ev(`(async () => {
                const host = document.createElement('div'); host.id = 'hr-host'; host.style.cssText = 'position:fixed;left:0;top:0;z-index:9999';
                host.innerHTML = ${JSON.stringify(html)}; document.body.appendChild(host);
                const style = document.createElement('style'); style.textContent = '#hr-host * { transition: none !important; }'; document.head.appendChild(style);
                const el = host.querySelector(${JSON.stringify(hidden)});
                const before = getComputedStyle(el).opacity;
                document.getElementById('hr-btn').focus();
                const focused = getComputedStyle(el).opacity;
                host.remove(); style.remove();
                return { before, focused };
            })()`);
            assert.equal(r.before, '0', `${name}: hidden until used`);
            assert.equal(r.focused, '1', `${name}: shown while a keyboard user has focus inside it`);
            console.log(`   ✓ ${name}: hidden at rest, visible on keyboard focus`);
        }
        console.log('\nPASS hover-only controls show on focus');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
