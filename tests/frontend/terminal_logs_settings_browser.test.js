// #4447: Settings > Storage shows how many terminal logs there are and how many belong to ended agents, and the Operator can delete those after a
// confirmation. Never automatic; the confirmation is for the count and size shown (the server refuses a request whose numbers are not the current ones).
//  - the line and the button: counts and sizes, the button names what it deletes, hidden when none belong to ended agents
//  - the confirmation says what is lost (search) and what is kept (live, sleeping, archived); focus on Cancel; Cancel deletes nothing
//  - Yes sends exactly the shown count and size; a 409 says nothing was deleted and shows the new numbers; success says what was freed
//  - an unreadable status says so and offers nothing; a phone at 200% text keeps the button inside the dialog at 44 px
// The server side (which logs, by exact name inside the folder, the state rules) is in internal/server/routes/terminal_logs_test.go; here it is stubbed.
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const MIB = 1024 ** 2;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw and a layout settle over frames; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__posts = []; window.__logs = { files: 31, bytes: ${181 * MIB}, ended: { count: 12, bytes: ${45 * MIB} } }; window.__next = null; window.__down = false;
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase(); const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                if (p === '/api/system/terminal-logs' && m === 'GET') return window.__down ? json({ error: 'x' }, 503) : json(window.__logs);
                if (p === '/api/system/terminal-logs/delete-ended' && m === 'POST') { window.__posts.push(JSON.parse(o.body)); return window.__next(); }
                return R(u, o); };
            return true; })()`);
        const openSettings = async () => {
            await waitFor(`(window.hideSettingsModal(), (!document.getElementById('settings-modal') || getComputedStyle(document.getElementById('settings-modal')).display === 'none'))`, 'Settings to close', 6000);
            await ev(`window.showSettingsModal('cleanup'); true`);
            await waitFor(`!!document.getElementById('settings-terminal-logs-status') && !/Loading/.test(document.getElementById('settings-terminal-logs-status').textContent)`, 'the terminal logs line', 8000);
        };
        const state = () => ev(`(() => { const b = document.getElementById('settings-terminal-logs-delete'); return { text: (document.getElementById('settings-terminal-logs-status')?.textContent ?? ''), role: document.getElementById('settings-terminal-logs-status').getAttribute('role'), hidden: b.hidden, button: b.textContent, label: (document.querySelector('label[for=settings-terminal-logs-delete]') || {}).textContent }; })()`);
        const showRow = () => ev(`(() => { const i = document.getElementById('settings-terminal-logs-status'); let p = i.parentElement; while (p && !(p.scrollHeight > p.clientHeight + 1 && /(auto|scroll)/.test(getComputedStyle(p).overflowY))) p = p.parentElement; if (p) p.scrollTop += i.getBoundingClientRect().top - p.getBoundingClientRect().top - 120; return true; })()`);

        console.log('1. The line and the button name the numbers');
        await openSettings();
        let st = await state();
        assert.equal(st.text, '31 logs, 0.2 GiB. 12 from ended agents (45 MiB).', JSON.stringify(st));
        assert.equal(st.role, 'status'); assert.equal(st.hidden, false);
        assert.equal(st.button, 'Delete 12 terminal logs of ended agents (45 MiB)', 'it names what it deletes');
        assert.equal(st.label, 'Terminal logs');
        await sleep(300); // sleep-ok: the dialog lays out
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        await shot('terminal-logs-desktop');

        console.log('2. The confirmation says what is lost and what is kept; Cancel deletes nothing');
        await ev(`document.getElementById('settings-terminal-logs-delete').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000);
        const ask = await ev(`({ title: document.getElementById('confirm-modal-title').textContent, message: document.getElementById('confirm-modal-message').textContent })`);
        assert.equal(ask.title, 'Delete terminal logs');
        assert.equal(ask.message, "Delete 12 terminal logs of ended agents (45 MiB)? What those agents' terminals showed can't be recovered. The logs of live, sleeping and archived agents are kept.");
        await waitFor(`document.activeElement && document.activeElement.closest('#confirm-modal') && document.activeElement.id !== 'confirm-modal-yes'`, 'focus on Cancel', 4000);
        await sleep(500); // sleep-ok: the dialog fades in
        assert.equal(await ev(`(() => { const r = document.querySelector('#confirm-modal .modal-content').getBoundingClientRect(); const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2); return !!at && !!at.closest('#confirm-modal'); })()`), true, 'the confirmation is above the Settings dialog that asked');
        await shot('terminal-logs-confirm');
        await ev(`document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)').click(); true`);
        await sleep(300); // sleep-ok: shows that Cancel deletes nothing (no condition to wait for)
        assert.equal(await ev(`window.__posts.length`), 0, 'Cancel deletes nothing');

        console.log('3. Yes sends exactly what was shown; a changed set deletes nothing and says so; success says what was freed');
        const yes = async () => { await ev(`document.getElementById('settings-terminal-logs-delete').click(); true`); await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000); await ev(`document.getElementById('confirm-modal-yes').click(); true`); };
        await ev(`window.__next = () => Promise.resolve(new Response(JSON.stringify({ error: 'changed', code: 'changed', ended: { count: 13, bytes: ${50 * MIB} } }), { status: 409, headers: { 'content-type': 'application/json' } })); window.__logs = { files: 32, bytes: ${186 * MIB}, ended: { count: 13, bytes: ${50 * MIB} } }; true`);
        await yes();
        await waitFor(`window.__posts.length === 1`, 'the request', 8000);
        assert.deepEqual(await ev(`window.__posts[0]`), { count: 12, bytes: 45 * MIB }, 'the numbers it was shown');
        await waitFor(`/changed since you looked, so nothing was deleted/.test(document.body.innerText)`, 'the changed notice', 8000);
        await waitFor(`/13 from ended agents \\(50 MiB\\)/.test((document.getElementById('settings-terminal-logs-status')?.textContent ?? ''))`, 'the new numbers', 8000);
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__next = () => Promise.resolve(new Response(JSON.stringify({ deleted: 13, bytes: ${50 * MIB}, failed: [] }), { status: 200, headers: { 'content-type': 'application/json' } })); window.__logs = { files: 19, bytes: ${136 * MIB}, ended: { count: 0, bytes: 0 } }; true`);
        await yes();
        await waitFor(`window.__posts.length === 2`, 'the second request', 8000);
        assert.deepEqual(await ev(`window.__posts[1]`), { count: 13, bytes: 50 * MIB });
        await waitFor(`/Deleted 13 terminal logs, 50 MiB freed\\./.test(document.body.innerText)`, 'the result', 8000);
        await waitFor(`document.getElementById('settings-terminal-logs-delete').hidden`, 'the button to go when none are left', 8000);
        assert.match(await ev(`(document.getElementById('settings-terminal-logs-status')?.textContent ?? '')`), /^19 logs, 0\.1 GiB$/);

        console.log('4. An unreadable status says so and offers nothing');
        await ev(`window.__down = true; true`);
        await openSettings();
        st = await state();
        assert.equal(st.text, "Couldn't read the terminal logs."); assert.equal(st.hidden, true);
        await ev(`window.__down = false; window.__logs = { files: 31, bytes: ${181 * MIB}, ended: { count: 12, bytes: ${45 * MIB} } }; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);

        console.log('5. A phone at 200% text: the button stays inside the dialog at 44 px');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`document.documentElement.style.fontSize = '32px'; true`);
        await openSettings();
        await sleep(400); // sleep-ok: the font-size layout settles
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        const fit = await ev(`(() => { const b = document.getElementById('settings-terminal-logs-delete'); const r = b.getBoundingClientRect(); const box = document.querySelector('#settings-modal .modal-content, #settings-modal .modal') || document.getElementById('settings-modal'); const br = box.getBoundingClientRect();
            const line = document.getElementById('settings-terminal-logs-status'); const lr = line.getBoundingClientRect();
            return { visible: r.width > 0, inside: r.left >= br.left - 1 && r.right <= br.right + 1, h: r.height, lineInside: lr.left >= br.left - 1 && lr.right <= br.right + 1, docOver: document.documentElement.scrollWidth - innerWidth, size: parseFloat(getComputedStyle(line).fontSize) }; })()`);
        assert.ok(fit.visible && fit.inside && fit.lineInside, 'the line and the button are on screen, inside the dialog'); assert.ok(fit.h >= 44, 'a 44 px touch target: ' + fit.h);
        assert.ok(fit.docOver <= 0, 'no sideways scroll'); assert.ok(fit.size >= 20, 'the line grows with the text size: ' + fit.size);
        await shot('terminal-logs-phone-200');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
