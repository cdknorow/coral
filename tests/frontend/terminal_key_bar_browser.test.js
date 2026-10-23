// The phone's terminal key bar (#6676, the phone terminal audit #6669 finding 1): a phone's keyboard has no Esc, Tab, Ctrl or arrows, so the Terminal view gets a row of keys under the terminal: Esc, Tab, Ctrl-C, the four arrows
// and | ~ / -, each a 44 px target with a name, two rows with every key on the screen, phone width and the Terminal view only. A tap sends the key to the agent (the keys route, the names the server takes) and leaves focus
// where it is. The message box says it is a message and not the terminal; the terminal's own input says what it is. 390 and 360 px, 1280 for "not on a desktop"; both themes for contrast. The agent is the harness's mock.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const KEYS = [['Esc', 'Escape', 'Escape'], ['Tab', 'Tab', 'Tab'], ['Ctrl-C', 'C-c', 'Ctrl-C, interrupt'], ['←', 'Left', 'Left arrow'], ['↑', 'Up', 'Up arrow'], ['↓', 'Down', 'Down arrow'], ['→', 'Right', 'Right arrow'], ['|', '|', 'Pipe'], ['~', '~', 'Tilde'], ['/', '/', 'Slash'], ['-', '-', 'Dash']];
const STUB = `(() => { const real = window.fetch; window.__keys = [];
    window.fetch = (u, o = {}) => { if (/\\/api\\/sessions\\/live\\/[^/]+\\/keys$/.test(String(u)) && (o.method || 'GET').toUpperCase() === 'POST') { window.__keys.push(JSON.parse(o.body).keys); return Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } })); } return real(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true], [360, 800, true], [844, 390, true]]) {
            const at = `at ${w}px: `;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`); await ev(STUB);
            await ev(`localStorage.setItem('coral-live-view-mode', 'chat'); window.__m.add('tk-a', 'A', 4, { sleeping: false }); true`);
            await select('tk-a');
            await ev(`window.setLiveViewMode('chat'); true`);
            await waitFor(`!window.__vis('#terminal-statusbar')`, 'the Chat view', 8000);
            await installAudit(ev);

            // Chat view: no key bar, and the message box keeps its label
            assert.equal(await ev(`window.__vis('#terminal-key-bar')`), false, at + 'no key bar in the Chat view');
            assert.equal(await ev(`document.getElementById('command-input').getAttribute('aria-label')`), 'Enter command or paste image');
            await ev(`window.setLiveViewMode('terminal'); true`);
            await waitFor(`window.__vis('#terminal-statusbar')`, 'the terminal bar', 8000);
            if (!mobile) { assert.equal(await ev(`window.__vis('#terminal-key-bar')`), false, at + 'a desktop has a keyboard: no key bar'); continue; }

            // Terminal view on a phone: the bar, its keys, their names and sizes
            await waitFor(`window.__vis('#terminal-key-bar')`, 'the key bar', 8000);
            assert.equal(await ev(`document.getElementById('terminal-key-bar').getAttribute('role') + '|' + document.getElementById('terminal-key-bar').getAttribute('aria-label')`), 'group|Terminal keys');
            const keys = await ev(`[...document.querySelectorAll('#terminal-key-bar .tkb-key')].map(b => { const r = b.getBoundingClientRect(); return { text: b.textContent.trim(), keys: b.dataset.keys, name: b.getAttribute('aria-label'), w: Math.round(r.width), h: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right) }; })`);
            assert.deepEqual(keys.map(k => [k.text, k.keys, k.name]), KEYS, at + 'the keys, in order, each with its name');
            assert.ok(keys.every(k => k.w >= 44 && k.h >= 44), at + 'every key is a 44 px target: ' + JSON.stringify(keys.filter(k => k.w < 44 || k.h < 44)));
            assert.ok(keys.every(k => k.right <= w && k.left >= 0 && k.w >= 44), at + 'every key is inside the screen, no scrolling to find one: ' + JSON.stringify(keys.map(k => [k.left, k.right])));
            assert.equal(await ev(`document.documentElement.scrollWidth <= innerWidth`), true, at + 'the page does not scroll sideways (the bar does)');
            assert.equal(await ev(`(() => { const b = document.getElementById('terminal-key-bar'); return b.scrollWidth <= b.clientWidth; })()`), true, at + 'the bar does not scroll: nothing is hidden');

            // the two inputs say what they are
            assert.equal(await ev(`document.getElementById('command-input').getAttribute('aria-label')`), 'Message to the agent, not typed into the terminal', at + 'the message box is a message');
            const tl = await ev(`(() => { const t = document.querySelector('#xterm-container textarea'); return t ? t.getAttribute('aria-label') : null; })()`);
            if (tl !== null) assert.match(tl, /^Terminal: what you type goes straight to the agent's shell$/, at + 'the terminal input says what it is');

            // a tap sends the key (the names the server takes) and leaves focus alone
            assert.equal(await ev(`(() => { const e = new PointerEvent('pointerdown', { bubbles: true, cancelable: true }); document.querySelector('#terminal-key-bar .tkb-key').dispatchEvent(e); return e.defaultPrevented; })()`), true, at + 'pointerdown does not take focus (the keyboard stays open)');
            for (const [, key] of KEYS) await ev(`[...document.querySelectorAll('#terminal-key-bar .tkb-key')].find(b => b.dataset.keys === ${JSON.stringify(key)}).click(); true`);
            await waitFor(`window.__keys.length === ${KEYS.length}`, 'every key sent', 8000);
            assert.deepEqual(await ev(`window.__keys`), KEYS.map(k => [k[1]]), at + 'each tap sends its key by name');
            await shot(`terminal-key-bar-${w}`);

            // contrast, both themes
            for (const theme of ['dark', 'light']) { await setTheme(ev, theme); const c = await auditRegion(ev, ['#terminal-key-bar']); assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails)); }
            await setTheme(ev, 'dark');

            // back in the Chat view the bar goes
            await ev(`window.setLiveViewMode('chat'); true`);
            await waitFor(`!window.__vis('#terminal-key-bar')`, 'the key bar to go with the terminal', 8000);
            assert.equal(await ev(`document.getElementById('command-input').getAttribute('aria-label')`), 'Enter command or paste image', at + 'the message box is itself again');
        }
        console.log('PASS terminal key bar: phone width and the Terminal view only, 44 px, named, sends its keys');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
