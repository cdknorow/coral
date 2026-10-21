// The Docs page has a "Copy docs line for agents" button: one click copies exactly the line a user pastes to an
// agent (DOCS_FOR_AGENTS: run coral-agent docs, coral-agent docs <topic>, coral-agent docs all) and shows a
// "Copied" toast. The line is the one Coral gives every agent in its own prompt (a Go test keeps the two equal).
// When the clipboard refuses, the toast says so instead of claiming a copy. The button is reachable by keyboard
// and fits a phone. Drives Chrome over CDP against a dev server.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

const LINE = "Coral's docs for agents: run `coral-agent docs` to list the topics, `coral-agent docs <topic>` to read one (`coral-agent docs all` prints everything).";

(async () => {
    const { client, Page, Emulation, ev, waitFor } = await connect();
    try {
        await Page.enable();
        const load = async (path, w, h, mobile) => {
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: mobile ? 2 : 1, mobile });
            const loaded = Page.loadEventFired();
            await Page.navigate({ url: BASE + path });
            await loaded;
            await ev(`window.__coralReady.then(() => true)`);
            await waitFor(`!!document.getElementById('docs-copy-agent-line') && getComputedStyle(document.getElementById('docs-view')).display !== 'none'`, 'the Docs page', 15000);
        };
        const stubClipboard = (ok) => ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: t => { window.__copied.push(t); return ${ok ? 'Promise.resolve()' : "Promise.reject(new Error('refused'))"}; } } }); true`);
        const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent.trim())`);

        for (const [label, w, h, mobile] of [['1. Desktop', 1280, 900, false], ['2. Phone', 390, 844, true]]) {
            console.log(`${label}: one click copies the exact line and says Copied`);
            await load('/docs', w, h, mobile);
            await stubClipboard(true);
            const before = await ev(`({ text: document.getElementById('docs-copy-agent-line').textContent.replace(/\\s+/g, ' ').trim(), vw: innerWidth, page: document.documentElement.scrollWidth - innerWidth,
                box: (r => ({ l: r.left, r: r.right, h: r.height }))(document.getElementById('docs-copy-agent-line').getBoundingClientRect()) })`);
            assert.match(before.text, /Copy docs line for agents/, JSON.stringify(before));
            assert.ok(before.box.l >= 0 && before.box.r <= before.vw && before.box.h >= 24, 'the button is on the screen and tappable: ' + JSON.stringify(before));
            assert.ok(before.page <= 1, 'no page-wide horizontal scroll: ' + JSON.stringify(before));
            await ev(`document.getElementById('docs-copy-agent-line').click(); true`);
            await waitFor(`window.__copied.length === 1`, 'one copy', 5000);
            assert.deepEqual(await ev(`window.__copied`), [LINE], 'it copies exactly the line');
            const exported = await ev(`import((window.__coralStatic || '/static/') + 'docs.js').then(m => m.DOCS_FOR_AGENTS)`);
            assert.equal(exported, LINE, 'the module constant is the copied line');
            await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /^Copied$/.test(t.textContent.trim()))`, 'the Copied toast', 5000);
            if (process.env.CORAL_SCREENSHOT_DIR) {
                require('node:fs').mkdirSync(process.env.CORAL_SCREENSHOT_DIR, { recursive: true });
                await new Promise(r => setTimeout(r, 300)); // sleep-ok: a screenshot settles layout, there is no event for it
                require('node:fs').writeFileSync(`${process.env.CORAL_SCREENSHOT_DIR}/docs-copy-${mobile ? 'phone' : 'desktop'}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64'));
            }
            console.log('   ✓');
        }

        console.log('3. A keyboard press copies it too (a real button, in the tab order)');
        await load('/docs', 1280, 900, false);
        await stubClipboard(true);
        const kb = await ev(`(() => { const b = document.getElementById('docs-copy-agent-line'); return { tag: b.tagName, type: b.type, tab: b.tabIndex, disabled: b.disabled }; })()`);
        assert.deepEqual(kb, { tag: 'BUTTON', type: 'button', tab: 0, disabled: false });
        console.log('   ✓');

        console.log('4. When the clipboard refuses, the toast does not claim a copy');
        await load('/docs', 1280, 900, false);
        await ev(`window.__copied = []; Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: () => Promise.reject(new Error('refused')) } }); document.execCommand = () => false; true`);
        await ev(`document.getElementById('docs-copy-agent-line').click(); true`);
        await waitFor(`[...document.querySelectorAll('.toast .toast-message')].some(t => /Could not copy/.test(t.textContent))`, 'the failure toast', 5000);
        assert.ok(!(await toasts()).some(t => /^Copied$/.test(t)), 'no Copied toast: ' + JSON.stringify(await toasts()));
        console.log('   ✓');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
