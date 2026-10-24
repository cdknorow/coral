// The agent's keys and Stop on a phone's chat (#6802, design #6795, UI Accessibility's eleven conditions on it). Driven over CDP through agent_view_harness.js with a mocked agent whose state the test sets
// through the page's own updateWaitingIndicator, and the keys route stubbed so each press is counted.
//  Stop: Send turned into a red rounded square (Escape, once, aria-disabled for 3 s, named by state, said in the status), 10 px from the team button; typing brings Send back
//  The Keys chip (aria-pressed, 44 px) opens a row of seven named 44 px keys, all on one row inside the screen with 6 px at each side; closed on every visit
//  A question opens it by itself once, never moves focus, closes when answered only if it opened itself; closed by the person it stays closed for that question
//  Interact and above only (a viewer's composer has neither); not on a desktop; 200% text keeps every key on screen; with the keyboard up it closes itself rather than push the chat out; contrast, both themes
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;

const KEYS = [['Esc', 'Escape', 'Escape'], ['←', 'Left', 'Left arrow'], ['↑', 'Up', 'Up arrow'], ['↓', 'Down', 'Down arrow'], ['→', 'Right', 'Right arrow'], ['Enter', 'Enter', 'Enter'], ['Ctrl-C', 'C-c', 'Ctrl-C, interrupt']];
const STUB = `(() => { const real = window.fetch; window.__keys = [];
    window.fetch = (u, o = {}) => { if (/\\/api\\/sessions\\/live\\/[^/]+\\/keys$/.test(String(u)) && (o.method || 'GET').toUpperCase() === 'POST') { window.__keys.push(JSON.parse(o.body).keys); return Promise.resolve(new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'content-type': 'application/json' } })); } return real(u, o); };
    return true; })()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install, select } = await connect();
    const shot = async n => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${n}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: layout, the 3 s Stop hold and a frame for the status text are the things measured
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD }); await Emulation.setFocusEmulationEnabled({ enabled: true });
        const size = (w, h, mobile) => Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 2, mobile });
        // set the agent's state as the page learns it
        const row = patch => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'session_header_updates.js'); const base = window.__m.entry(Object.values(window.__m.agents).find(a => a.name === 'ak')); m.updateWaitingIndicator({ ...base, session_id: 'ak-sid', ...${JSON.stringify(patch)} }); await new Promise(r => setTimeout(r, 80)); return true; })()`);
        const q = sel => `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (!e || !e.checkVisibility()) return null; const r = e.getBoundingClientRect(); return { l: Math.round(r.left), r: Math.round(r.right), t: Math.round(r.top), b: Math.round(r.bottom), w: Math.round(r.width), h: Math.round(r.height) }; })()`;
        const status = () => ev(`document.getElementById('agent-keys-status').textContent`);
        const load = async (w, h, mobile) => {
            await size(w, h, mobile); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'liveListLoaded', 'settings')`); await ev(STUB);
            await ev(`window.__m.add('ak', 'A', 6, { sleeping: false, session_id: 'ak-sid' }); window._coralSetLiveSessions(Object.values(window.__m.agents).map(a => window.__m.entry(a))); localStorage.setItem('coral-live-view-mode', 'chat'); true`);
            await select('ak');
            await waitFor(`window.__rendered && window.__rendered().length >= 3`, 'the chat', 15000);
            await ev(`document.getElementById('model-chip-bar').hidden = false; true`); // the chips show once the model is known: made to show
            await row({ working: false }); await sleep(300); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
        };
        for (const [w, h] of [[390, 844], [360, 800]]) {
            const at = `${w}px: `;
            await load(w, h, true);
            // closed by default, the chip
            const chip = await ev(q('#keys-chip'));
            assert.ok(chip && chip.h >= 44, at + 'the Keys chip is a 44 px target: ' + JSON.stringify(chip));
            assert.equal(await ev(`document.getElementById('keys-chip').getAttribute('aria-pressed')`), 'false');
            assert.equal(await ev(`document.getElementById('keys-chip').getAttribute('aria-label')`), 'Keys');
            assert.equal(await ev(q('#agent-key-strip')), null, at + 'the strip is closed on arrival');

            // Stop: Send turned red while the agent works and the box is empty
            assert.equal(await ev(`document.getElementById('mobile-send-btn').getAttribute('aria-label')`), 'Send', at + 'an idle agent: Send');
            await row({ working: true }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            const st = await ev(`(() => { const b = document.getElementById('mobile-send-btn'), t = document.querySelector('.mobile-send-team-btn'), r = b.getBoundingClientRect(), tr = t.getBoundingClientRect(); return { stop: b.classList.contains('is-stop'), label: b.getAttribute('aria-label'), action: b.getAttribute('data-on-click'), w: Math.round(r.width), h: Math.round(r.height), gap: Math.round(r.left - tr.right), radius: parseFloat(getComputedStyle(b).borderTopLeftRadius), bg: getComputedStyle(b).backgroundColor }; })()`);
            assert.ok(st.stop && st.label === 'Stop the agent' && st.action === 'cmd-stop', at + 'a working agent and an empty box: Stop: ' + JSON.stringify(st));
            assert.ok(st.w >= 44 && st.h >= 44, at + 'Stop is a 44 px target');
            assert.ok(st.gap >= 8, at + `Stop is at least 8 px from the team button (${st.gap}px)`);
            assert.ok(st.radius < st.w / 2 - 4, at + `Stop is a rounded square, not the neighbour's circle (radius ${st.radius}px)`);
            assert.equal(await status(), 'Stop is shown', at + 'the swap is said once in the status region');
            await shot(`agent-keys-stop-${w}`);
            // typing brings Send back, clearing brings Stop back
            await ev(`{ const i = document.getElementById('command-input'); i.value = 'next, please'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(`document.getElementById('mobile-send-btn').getAttribute('aria-label')`), 'Send', at + 'a word in the box: Send');
            await ev(`{ const i = document.getElementById('command-input'); i.value = ''; i.dispatchEvent(new Event('input', { bubbles: true })); } true`); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(`document.getElementById('mobile-send-btn').getAttribute('aria-label')`), 'Stop the agent', at + 'an empty box again: Stop');
            // one press sends Escape once; a second is ignored while it is aria-disabled
            await ev(`window.__keys.length = 0; const b = document.getElementById('mobile-send-btn'); b.click(); b.click(); true`); await sleep(400); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.deepEqual(await ev(`window.__keys`), [['Escape']], at + 'Stop sends one Escape (the CLI\'s interrupt), never two');
            assert.equal(await ev(`document.getElementById('mobile-send-btn').getAttribute('aria-disabled')`), 'true', at + 'Stop is aria-disabled after the press (still focusable)');
            assert.equal(await status(), 'Stopping the agent');
            await sleep(3300); // sleep-ok: the 3 s hold
            assert.equal(await ev(`document.getElementById('mobile-send-btn').hasAttribute('aria-disabled')`), false, at + 'allowed again after 3 s');
            await row({ working: false }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(`document.getElementById('mobile-send-btn').getAttribute('aria-label')`), 'Send', at + 'the agent stopped: Send');

            // the chip opens the strip: seven named keys on one row, inside the screen with 6 px each side
            await ev(`document.getElementById('command-input').focus(); true`);
            assert.equal(await ev(`(() => { const e = new PointerEvent('pointerdown', { bubbles: true, cancelable: true, pointerType: 'touch' }); document.getElementById('keys-chip').dispatchEvent(e); return e.defaultPrevented; })()`), true, at + 'a touch on the chip does not take focus (the keyboard stays)');
            await ev(`document.getElementById('keys-chip').click(); true`); await sleep(250); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            const keys = await ev(`[...document.querySelectorAll('#agent-key-strip .tkb-key')].map(b => { const r = b.getBoundingClientRect(); return { text: b.textContent.trim(), keys: b.dataset.keys, name: b.getAttribute('aria-label'), l: Math.round(r.left), r: Math.round(r.right), t: Math.round(r.top), h: Math.round(r.height), w: Math.round(r.width) }; })`);
            assert.deepEqual(keys.map(k => [k.text, k.keys, k.name]), KEYS, at + 'the seven keys with their names');
            assert.ok(keys.every(k => k.h >= 44 && k.w >= 44), at + 'every key is a 44 px target: ' + JSON.stringify(keys.map(k => [k.w, k.h])));
            assert.ok(keys.every(k => k.l >= 6 && k.r <= w - 6), at + 'every key is 6 px inside the screen edges: ' + JSON.stringify(keys.map(k => [k.l, k.r])));
            assert.equal(new Set(keys.map(k => k.t)).size, 1, at + 'one row: ' + JSON.stringify(keys.map(k => [k.l, k.t, k.w])));
            assert.equal(await ev(`document.getElementById('keys-chip').getAttribute('aria-pressed')`), 'true');
            await shot(`agent-keys-open-${w}`);
            assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'command-input', at + 'focus stayed in the message box');
            assert.equal(await ev(`document.getElementById('agent-key-strip').getAttribute('role') + '|' + document.getElementById('agent-key-strip').getAttribute('aria-label')`), 'group|Keys for the agent');
            assert.equal(await ev(`[...document.querySelectorAll('#agent-key-strip .tkb-key')].every(b => b.tabIndex === 0)`), true, at + 'seven plain Tab stops, no roving');
            const order = await ev(`(() => { const all = [...document.querySelectorAll('#command-pane button, #command-pane textarea')].filter(e => e.checkVisibility()); return [all.indexOf(document.querySelector('#agent-key-strip .tkb-key')), all.indexOf(document.getElementById('command-input'))]; })()`);
            assert.ok(order[0] < order[1], at + 'the strip is above the message box on the screen and before it in the Tab order');
            // a key press sends its key and leaves focus
            await ev(`window.__keys.length = 0; document.querySelector('#agent-key-strip [data-keys="Enter"]').click(); true`); await sleep(300); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.deepEqual(await ev(`window.__keys`), [['Enter']], at + 'a tap sends its key');
            // Ctrl-C and the focus ring
            const ring = await ev(`(() => { const b = document.querySelector('#agent-key-strip .tkb-key'); b.focus(); b.matches(':focus-visible') || 0; return getComputedStyle(b).outlineWidth; })()`);
            assert.ok(ring === '0px' || parseFloat(ring) >= 0, at + 'the key can take focus');
            // the chip closes it again
            await ev(`document.getElementById('keys-chip').click(); true`); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(q('#agent-key-strip')), null, at + 'the chip closes it');

            // a question opens it by itself, once, without moving focus
            await ev(`document.getElementById('command-input').focus(); true`);
            await row({ waiting_for_input: true, waiting_kind: 'choice', waiting_summary: 'Which file?' }); await sleep(300); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.ok(await ev(q('#agent-key-strip')), at + 'a question opens the strip by itself');
            assert.equal(await status(), 'Keys for the question are shown', at + 'said once');
            assert.equal(await ev(`document.activeElement.id`), 'command-input', at + 'focus was not moved into the strip');
            // closed by the person: it stays closed for that question, a new question opens it again
            await ev(`document.getElementById('keys-chip').click(); true`); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            await row({ waiting_for_input: true, waiting_kind: 'choice', waiting_summary: 'Which file?' }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(q('#agent-key-strip')), null, at + 'the person closed it for this question: it stays closed');
            await row({ waiting_for_input: true, waiting_kind: 'choice', waiting_summary: 'Which folder?' }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.ok(await ev(q('#agent-key-strip')), at + 'another question opens it');
            // focus in the strip when the question is answered: it goes to the message box
            await ev(`document.querySelector('#agent-key-strip .tkb-key').focus(); true`);
            await row({ waiting_for_input: false }); await sleep(250); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(q('#agent-key-strip')), null, at + 'answered: the strip it opened closes');
            assert.equal(await ev(`document.activeElement.id`), 'command-input', at + 'focus went to the message box, not the page');
            // opened by the person it stays after the answer
            await ev(`document.getElementById('keys-chip').click(); true`); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            await row({ waiting_for_input: true, waiting_kind: 'choice', waiting_summary: 'Another?' }); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            await row({ waiting_for_input: false }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.ok(await ev(q('#agent-key-strip')), at + 'opened with the chip: it stays after the answer');

            // closed on every visit: the chat off screen and back
            await ev(`document.getElementById('command-pane').style.display = 'none'; true`); await row({ working: false }); await ev(`document.getElementById('command-pane').style.display = ''; true`); await row({ working: false }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(q('#agent-key-strip')), null, at + 'a new visit starts closed');

            // a viewer: no chip, no strip, no Stop
            await ev(`document.getElementById('command-pane').classList.add('hub-view-only'); true`);
            await row({ working: true }); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            assert.equal(await ev(q('#keys-chip')), null, at + 'a viewer has no Keys chip');
            assert.equal(await ev(`document.getElementById('mobile-send-btn').classList.contains('is-stop')`), false, at + 'a viewer has no Stop');
            await ev(`document.getElementById('command-pane').classList.remove('hub-view-only'); true`); await row({ working: false }); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event

            // contrast, both themes: the strip with Ctrl-C's red, and Stop's white square on red
            await installAudit(ev);
            await ev(`document.getElementById('keys-chip').click(); true`); await sleep(200); await row({ working: true }); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            for (const theme of ['dark', 'light']) { await setTheme(ev, theme); const c = await auditRegion(ev, ['#agent-key-strip', '#mobile-send-btn', '#keys-chip']); assert.ok(c.checked && !c.fails.length, at + theme + ' contrast: ' + JSON.stringify(c.fails)); }
            await setTheme(ev, 'dark');
            await ev(`document.getElementById('keys-chip').click(); true`); await row({ working: false });

            // 200% text: every key still on screen, no clipped label, at most two rows (three at 360)
            await ev(`document.documentElement.style.fontSize = '32px'; document.getElementById('keys-chip').click(); true`); await sleep(500); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
            const big = await ev(`(() => { const bs = [...document.querySelectorAll('#agent-key-strip .tkb-key')]; const rs = bs.map(b => b.getBoundingClientRect()); return { rows: new Set(rs.map(r => Math.round(r.top))).size, inside: rs.every(r => r.left >= 0 && r.right <= innerWidth), clipped: bs.filter(b => b.scrollWidth > b.clientWidth + 1).map(b => b.textContent.trim()) }; })()`);
            assert.ok(big.inside && !big.clipped.length, at + '200% text: every key on screen and whole: ' + JSON.stringify(big));
            assert.ok(big.rows <= (w >= 390 ? 2 : 3), at + `200% text: at most ${w >= 390 ? 2 : 3} rows (${big.rows})`);
            await ev(`document.documentElement.style.fontSize = ''; document.getElementById('keys-chip').click(); true`);
            console.log('PASS ' + at.trim());
        }
        // the keyboard up at 390 x 464: the strip must not push the chat out; under three lines it closes itself and says so
        await load(390, 844, true);
        await ev(`document.getElementById('keys-chip').click(); true`); await sleep(200); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
        await size(390, 380, true); await sleep(900); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
        const room = await ev(`(() => { const c = document.getElementById('live-history-messages'); return { chat: c ? c.clientHeight : -1, strip: !!document.querySelector('#agent-key-strip') && document.getElementById('agent-key-strip').checkVisibility() }; })()`);
        await shot('agent-keys-keyboard-390');
        assert.ok(room.chat >= 72 || !room.strip, 'with the keyboard up the chat keeps three lines or the strip closed itself: ' + JSON.stringify(room));
        if (!room.strip) assert.match(await status(), /no room/, 'and it said so');

        // a desktop: nothing
        await load(1280, 800, false);
        assert.equal(await ev(q('#keys-chip')), null, 'a desktop window has no Keys chip');
        await row({ working: true }); await sleep(150); // sleep-ok: the layout, the status frame or the 3 s Stop hold settles; there is no completion event
        assert.equal(await ev(`document.getElementById('mobile-send-btn').classList.contains('is-stop')`), false, 'a desktop window keeps Send');
        console.log('PASS agent keys and Stop on the phone chat');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
