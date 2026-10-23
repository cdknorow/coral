// The terminal with the phone's on-screen keyboard open (#6694, the phone terminal audit #6669 finding 5): at 390 x 464 (the keyboard takes 55% of an 844 px screen) the terminal had 130 px, 10 rows, under the
// sign-in banner, the Model and Thinking chips, the status bar and a three-line message box. While the keyboard is open on a phone (a coarse pointer, phone width, the viewport under 75% of its height) the banner, the chips and the
// status bar step aside and the message box is one line; the key bar stays; they all come back when the keyboard closes. A desktop window, and a narrow window with a mouse, are not a phone with a keyboard: nothing changes there.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const size = (w, h, mobile) => Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile });
    const boxes = () => ev(`(() => { const g = s => { const e = document.querySelector(s); return !e || !e.checkVisibility() ? null : Math.round(e.getBoundingClientRect().height); }; return { cls: document.documentElement.classList.contains('kb-open'), banner: g('#local-auth-banner'), chips: g('#model-chip-bar'), status: g('#terminal-statusbar'), xterm: g('#xterm-container'), keys: g('#terminal-key-bar'), input: g('#command-input') }; })()`);
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [label, touch, w, h, kb] of [['a phone', true, 390, 844, 464], ['a small phone', true, 360, 800, 440], ['a desktop window', false, 1280, 800, 440], ['a narrow window with a mouse', false, 390, 844, 464]]) {
            const at = `${label}: `;
            await size(w, h, touch); await Emulation.setTouchEmulationEnabled({ enabled: touch });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`window.__m.add('kb-a', 'A', 4, { sleeping: false }); true`); await select('kb-a');
            await ev(`window.setLiveViewMode('terminal'); true`);
            await waitFor(`window.__vis('#terminal-statusbar')`, 'the terminal view', 8000);
            await ev(`document.getElementById('model-chip-bar').hidden = false; true`); // the chips show once the agent's model is known: here they are made to show
            const closed = await boxes();
            assert.equal(closed.cls, false, at + 'no keyboard: nothing compact');
            assert.ok(closed.banner && closed.status && (w > 767 || closed.input >= 60), at + 'the banner, the status bar and the message box are as they were: ' + JSON.stringify(closed));
            await size(w, kb, touch);
            await new Promise(r => setTimeout(r, 700)); // sleep-ok: the viewport change reaches the page's resize listener
            const open = await boxes();
            if (!(touch && w <= 767)) { assert.equal(open.cls, false, at + 'not a phone with a keyboard: nothing changes: ' + JSON.stringify(open)); continue; }
            assert.equal(open.cls, true, at + 'the keyboard is open: the layout is compact: ' + JSON.stringify(open));
            assert.equal(open.banner, null, at + 'the sign-in banner steps aside');
            assert.equal(open.chips, null, at + 'the model chips step aside');
            assert.equal(open.status, null, at + 'the status bar steps aside');
            assert.ok(open.input && open.input <= 48, at + 'the message box is one line: ' + open.input);
            assert.ok(open.keys >= 44, at + 'the key bar stays: ' + JSON.stringify(open));
            const rows = await ev(`(async () => (await import((window.__coralStatic||'/static/')+'xterm_renderer.js')).getTerminal().rows)()`);
            assert.ok(open.xterm >= 170 && rows >= 13, at + `the terminal keeps its rows (it had 10 rows in 130 px before): ${rows} rows in ${open.xterm} px`);
            await shot(`terminal-keyboard-${w}`);
            await size(w, h, touch);
            await new Promise(r => setTimeout(r, 700)); // sleep-ok: the viewport change reaches the page's resize listener
            const back = await boxes();
            assert.equal(back.cls, false, at + 'the keyboard closed: the layout is back');
            assert.ok(back.banner && back.status && back.chips && back.input >= 60, at + 'everything is back: ' + JSON.stringify(back));
        }
        console.log('PASS terminal keyboard: the terminal keeps room while the phone keyboard is open');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
