// #4441 (Security's #4438 SHOULD 2): Settings > Storage shows what other teams and Corals have sent this computer against the limit on it, the limit can be
// raised or cleared, and the Operator is told once at 80% of it.
//  - the row: the total, the limit, a labelled number field described by its line, the default shown as 2 when nothing is saved
//  - Save sends the limit only when it was changed (an untouched field never turns the default into a saved value), in whole bytes; 0 is no limit
//  - the notice: shown once (the server remembers it by saving the cap it was shown for), not again on the next check, and again only for a different cap
//  - received files no board lists (their message never came): counted in the limit, listed with their size, and deletable by the Operator after a confirmation, by the exact digests the server named; Cancel deletes nothing
//  - a phone at 200% text: the label, the field and its line stay inside the dialog
// The server side (the cap, store_full, the status block, the notice) is in internal/server/routes/received_cap_test.go; here the status is stubbed.
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const GIB = 1024 ** 3;

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
        await ev(`(() => { const R = window.fetch; window.__puts = []; window.__deletes = []; window.__status = { bytes: ${Math.round(1.7 * GIB)}, objects: 3, max_bytes: ${2 * GIB}, default_bytes: ${2 * GIB}, notice: false };
            window.fetch = (u, o = {}) => { const p = new URL(String(u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
                if (p === '/api/system/artifact-store' && m === 'GET') return Promise.resolve(new Response(JSON.stringify({ bytes: 1000, objects: 1, max_bytes: 0, task_diffs: { bytes: 0, files: 0 }, session_diffs: { bytes: 0, files: 0 }, received: window.__status }), { headers: { 'content-type': 'application/json' } }));
                if (p === '/api/artifacts/received/unlisted/delete' && m === 'POST') { const b = JSON.parse(o.body); window.__deletes.push(b); window.__status = { ...window.__status, unlisted: { count: 0, bytes: 0, items: [] } }; return Promise.resolve(new Response(JSON.stringify({ deleted: b.ids.length, bytes: 3000000, skipped: [] }), { headers: { 'content-type': 'application/json' } })); }
                if (p === '/api/settings' && m === 'PUT') { window.__puts.push(JSON.parse(o.body)); if ('received_cap_notice_ack' in JSON.parse(o.body)) window.__status = { ...window.__status, notice: false }; return Promise.resolve(new Response('{}', { headers: { 'content-type': 'application/json' } })); }
                return R(u, o); };
            return true; })()`);
        const MOD = `(window.__coralStatic || '/static/')`;
        const save = () => ev(`import(${MOD} + 'modals.js').then(m => m.applySettings()).then(() => true)`);
        // Scroll the dialog's own scroller (the page itself does not scroll) so the row is in view, a little below the top.
        const showRow = () => ev(`(() => { const i = document.getElementById('settings-received-max'); let p = i.parentElement; while (p && !(p.scrollHeight > p.clientHeight + 1 && /(auto|scroll)/.test(getComputedStyle(p).overflowY))) p = p.parentElement; if (p) p.scrollTop += i.getBoundingClientRect().top - p.getBoundingClientRect().top - 80; return true; })()`);
        const lastPut = () => ev(`window.__puts[window.__puts.length - 1] || null`);

        console.log('1. The row: the total, the limit, a labelled field');
        await ev(`window.showSettingsModal('cleanup'); true`);
        await waitFor(`/ GiB| B/.test((document.getElementById('settings-received-size') || {}).textContent || '')`, 'the received line', 8000);
        const row = await ev(`(() => { const i = document.getElementById('settings-received-max'); const d = document.getElementById(i.getAttribute('aria-describedby').split(' ')[0]); const l = document.querySelector('label[for=settings-received-max]');
            return { value: i.value, loaded: i.dataset.loaded, text: d.textContent, label: l && l.textContent.trim(), type: i.type, min: i.min }; })()`);
        assert.equal(row.value, '2'); assert.equal(row.loaded, '1'); assert.equal(row.label, 'Received files limit'); assert.equal(row.type, 'number'); assert.equal(row.min, '0');
        assert.match(row.text, /^1\.7 GiB of 2\.0 GiB \(85% full\)\. Delete received files in Team Artifacts to make room, or raise the limit\.$/, 'from 80% on: how full, and what to do');
        assert.equal(await ev(`document.getElementById('settings-received-size').dataset.state || ''`), '', 'the warning colour is for the limit itself');
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        await shot('received-cap-desktop');

        console.log('1b. The line says how full it is: nothing special below 80%, a warning at the limit that new files are refused');
        const reopenWith = async status => {
            await waitFor(`(window.hideSettingsModal(), getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings to close', 6000);
            await ev(`window.__status = ${JSON.stringify(status)}; window.showSettingsModal('cleanup'); true`);
            await waitFor(`/ GiB| B/.test(document.getElementById('settings-received-size').textContent) && document.getElementById('settings-received-max').dataset.loaded === '1'`, 'the line', 8000);
            await sleep(300); // sleep-ok: the rest of the dialog finishes laying out
            return ev(`({ text: document.getElementById('settings-received-size').textContent, state: document.getElementById('settings-received-size').dataset.state || '', color: getComputedStyle(document.getElementById('settings-received-size')).color })`);
        };
        const calm = await reopenWith({ bytes: GIB / 2, objects: 3, max_bytes: 2 * GIB, default_bytes: 2 * GIB, notice: false });
        assert.equal(calm.text, '0.5 GiB of 2.0 GiB'); assert.equal(calm.state, '');
        const full = await reopenWith({ bytes: 2 * GIB, objects: 3, max_bytes: 2 * GIB, default_bytes: 2 * GIB, notice: false });
        assert.equal(full.text, '2.0 GiB of 2.0 GiB: new received files are refused. Delete received files in Team Artifacts to make room, or raise the limit.');
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        await shot('received-cap-at-limit');
        assert.equal(full.state, 'warn', 'at the limit the line is in the warning colour'); assert.notEqual(full.color, calm.color, 'and the colour differs from the calm one');
        await reopenWith({ bytes: Math.round(1.7 * GIB), objects: 3, max_bytes: 2 * GIB, default_bytes: 2 * GIB, notice: false });

        console.log('2. Save sends it only when changed, in whole bytes; 0 is no limit; an emptied field is unchanged');
        await ev(`window.__puts.length = 0; true`);
        await save();
        assert.ok(!(await ev(`window.__puts.some(b => 'received_artifacts_max_bytes' in b)`)), 'an untouched field never turns the default into a saved value');
        await ev(`document.getElementById('settings-received-max').value = '5'; window.__puts.length = 0; true`);
        await save();
        assert.equal((await lastPut()).received_artifacts_max_bytes, String(5 * GIB));
        await ev(`document.getElementById('settings-received-max').value = '0'; window.__puts.length = 0; true`);
        await save();
        assert.equal((await lastPut()).received_artifacts_max_bytes, '0', '0 clears the limit');
        // An emptied field is unchanged: it must never save 0 (no limit) by accident, for this limit or the artifact store's.
        await ev(`document.getElementById('settings-received-max').value = ''; document.getElementById('settings-artifact-store-max').value = ''; window.__puts.length = 0; true`);
        await save();
        assert.ok(!(await ev(`window.__puts.some(b => 'received_artifacts_max_bytes' in b || 'artifact_store_max_bytes' in b)`)), 'an emptied GiB field saves nothing');
        await ev(`window.hideSettingsModal(); window.__status = { ...window.__status, max_bytes: 0 }; window.showSettingsModal('cleanup'); true`);
        await waitFor(`document.getElementById('settings-received-max').dataset.loaded === '1' && !/ of /.test((document.getElementById('settings-received-size') || {}).textContent || ' of ')`, 'the no-limit line (just the total, no "of")', 8000);
        assert.equal(await ev(`document.getElementById('settings-received-max').value`), '0');
        await ev(`window.hideSettingsModal(); window.__status = { bytes: ${Math.round(1.7 * GIB)}, objects: 3, max_bytes: ${2 * GIB}, default_bytes: ${2 * GIB}, notice: false }; true`);
        // showSettingsModal finishes opening after its requests answer, which can land after a hide: hide again on every poll until it stays closed.
        await waitFor(`(window.hideSettingsModal(), getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings to close', 6000);

        console.log('3. The notice at 80%: a toast when Settings is closed, none on top of the dialog, shown once');
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`); // the saves above left their own
        const TOAST = /Other teams’ files: 1\.7 GiB of 2\.0 GiB\. New files are refused above this limit\. Raise it in Settings > Storage\./;
        // With Settings open the line says it: remembered, no toast.
        await ev(`window.__puts.length = 0; window.__status = { ...window.__status, bytes: ${Math.round(1.7 * GIB)}, notice: true }; window.showSettingsModal('cleanup'); true`);
        await waitFor(`/ GiB| B/.test(document.getElementById('settings-received-size').textContent)`, 'Settings open', 8000);
        await waitFor(`window.__puts.some(b => 'received_cap_notice_ack' in b)`, 'the notice remembered while Settings is open', 8000);
        await sleep(400); // sleep-ok: shows that no toast follows (no condition to wait for)
        assert.ok(!TOAST.test(await ev(`document.body.innerText`)), 'no toast over the dialog it points to');
        // With Settings closed it is a toast, once.
        await waitFor(`(window.hideSettingsModal(), getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings to close', 6000);
        await ev(`window.__puts.length = 0; window.__status = { ...window.__status, notice: true }; true`);
        await ev(`import(${MOD} + 'received_cap_notice.js').then(m => m.checkReceivedCap()).then(() => true)`);
        await waitFor(`/New files are refused above this limit/.test(document.body.innerText)`, 'the notice', 8000);
        assert.deepEqual(await ev(`window.__puts`), [{ received_cap_notice_ack: String(2 * GIB) }], 'it is remembered by the cap it was shown for');
        assert.match(await ev(`document.body.innerText`), TOAST);
        await sleep(700); // sleep-ok: the toast fades in
        await shot('received-cap-notice');
        await ev(`import(${MOD} + 'received_cap_notice.js').then(m => m.checkReceivedCap()).then(() => true)`); await sleep(300); // sleep-ok: shows that nothing more happens (no condition to wait for)
        assert.equal(await ev(`window.__puts.length`), 1, 'the next check, with the server no longer saying it is due, says and saves nothing');
        assert.equal(await ev(`(document.body.innerText.match(/New files are refused above this limit/g) || []).length`), 1);

        console.log('4. Received files no board lists: listed, and deleted only after a confirmation, by the exact digests');
        // More than the 50 the server names: the line counts all of them, says that the button takes the oldest 50, and the button names 50.
        const many = Array.from({ length: 50 }, (_, i) => ({ id: i.toString(16).padStart(64, '0'), size: 1000000, at: '2026-10-17T09:00:00Z', source: 'src' }));
        await ev(`window.__status = { bytes: ${Math.round(1.7 * GIB)}, objects: 120, max_bytes: ${2 * GIB}, default_bytes: ${2 * GIB}, notice: false, unlisted: { count: 120, bytes: 120000000, items: ${JSON.stringify(many)} } }; document.querySelectorAll('.toast').forEach(t => t.remove()); window.showSettingsModal('cleanup'); true`);
        await waitFor(`!document.getElementById('settings-received-unlisted').hidden`, 'the unlisted button with more than 50', 8000);
        const over = await ev(`({ text: document.getElementById('settings-received-size').textContent, button: document.getElementById('settings-received-unlisted-delete').textContent })`);
        assert.match(over.text, /Not listed on any board: 120 files, 0\.1 GiB\./, 'the line counts all of them');
        assert.match(over.text, / Showing the oldest 50; delete them, then the rest\.$/, 'and says the button takes the oldest 50');
        assert.equal(over.button, 'Delete 50 unlisted files (48 MiB)', 'the button names the 50 it deletes');
        await waitFor(`(window.hideSettingsModal(), getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings to close', 6000);
        const ids = ['a', 'b', 'c'].map(c => c.repeat(64));
        await ev(`window.__status = { bytes: ${Math.round(1.7 * GIB)}, objects: 3, max_bytes: ${2 * GIB}, default_bytes: ${2 * GIB}, notice: false, unlisted: { count: 3, bytes: 3000000, items: ${JSON.stringify(ids.map(id => ({ id, size: 1000000, at: '2026-10-17T09:00:00Z', source: 'src' })))} } }; window.__deletes.length = 0; document.querySelectorAll('.toast').forEach(t => t.remove()); window.showSettingsModal('cleanup'); true`);
        await waitFor(`!document.getElementById('settings-received-unlisted').hidden`, 'the unlisted line', 8000);
        const un = await ev(`(() => { const b = document.getElementById('settings-received-unlisted-delete'); const region = document.getElementById('settings-received-size');
            return { text: region.textContent, role: region.getAttribute('role'), described: document.getElementById('settings-received-max').getAttribute('aria-describedby'), button: b.textContent, disabled: b.disabled, buttonInRegion: region.contains(b) }; })()`);
        assert.match(un.text, /Not listed on any board: 3 files, 3 MiB\. Another Coral sent them but the message that names them never came\. They count toward the limit\.$/);
        assert.equal(un.role, 'status'); assert.equal(un.described, 'settings-received-size settings-received-max-unit', 'one status region, described by the field (then its unit, GiB)'); assert.equal(un.buttonInRegion, false, 'the button is outside the announced text');
        assert.equal(un.button, 'Delete 3 unlisted files (3 MiB)', 'it names what it deletes'); assert.equal(un.disabled, false);
        await waitFor(`/ GiB| B/.test(document.getElementById('settings-received-size').textContent) && document.getElementById('settings-received-max').dataset.loaded === '1'`, 'the dialog to finish loading', 8000);
        await sleep(400); // sleep-ok: the rest of the dialog finishes laying out
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        await shot('received-cap-unlisted');
        await ev(`document.getElementById('settings-received-unlisted-delete').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000);
        const ask = await ev(`({ title: document.getElementById('confirm-modal-title').textContent, message: document.getElementById('confirm-modal-message').textContent })`);
        assert.equal(ask.title, 'Delete received files');
        assert.match(ask.message, /^Delete 3 unlisted files \(3 MiB\)\? Only these files are removed from Coral and can't be recovered; nothing else is touched, and a file something now lists or links is left alone\.$/);
        await waitFor(`document.activeElement && document.activeElement.closest('#confirm-modal') && document.activeElement.id !== 'confirm-modal-yes'`, 'focus on Cancel', 4000);
        await ev(`document.querySelector('#confirm-modal .modal-actions .btn:not(#confirm-modal-yes)').click(); true`);
        await sleep(300); // sleep-ok: shows that Cancel deletes nothing (no condition to wait for)
        assert.equal(await ev(`window.__deletes.length`), 0, 'Cancel deletes nothing');
        await ev(`document.getElementById('settings-received-unlisted-delete').click(); true`);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation again', 8000);
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await waitFor(`window.__deletes.length === 1`, 'the delete', 8000);
        assert.deepEqual(await ev(`window.__deletes[0]`), { ids }, 'the exact digests the server named, nothing else');
        await waitFor(`/Deleted 3 received files, 3 MiB freed\./.test(document.body.innerText)`, 'the result', 8000);
        await waitFor(`document.getElementById('settings-received-unlisted').hidden`, 'the line to go once none are left', 8000);
        await ev(`window.hideSettingsModal(); true`);
        await waitFor(`(window.hideSettingsModal(), getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings to close', 6000);

        console.log('5. A phone at 200% text: the label, the field and its line stay inside the dialog');
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); document.documentElement.style.fontSize = '32px'; window.__status = { ...window.__status, bytes: ${Math.round(1.7 * GIB)}, notice: false, unlisted: { count: 3, bytes: 3000000, items: ${JSON.stringify(ids.map(id => ({ id, size: 1000000, at: '2026-10-17T09:00:00Z', source: 'src' })))} } }; window.showSettingsModal('cleanup'); true`);
        await waitFor(`/ GiB| B/.test((document.getElementById('settings-received-size') || {}).textContent || '')`, 'the received line on a phone', 8000);
        await sleep(400); // sleep-ok: the font-size layout settles
        await showRow(); await sleep(300); // sleep-ok: the scroll settles
        const fit = await ev(`(() => { const box = document.querySelector('#settings-modal .modal-content, #settings-modal .modal') || document.getElementById('settings-modal'); const br = box.getBoundingClientRect();
            const inside = e => { const r = e.getBoundingClientRect(); return r.width > 0 && r.left >= br.left - 1 && r.right <= br.right + 1; };
            const i = document.getElementById('settings-received-max'); const l = document.querySelector('label[for=settings-received-max]'); const d = document.getElementById('settings-received-size');
            return { input: inside(i), label: inside(l), line: inside(d), docOver: document.documentElement.scrollWidth - innerWidth, h: i.getBoundingClientRect().height, size: parseFloat(getComputedStyle(d).fontSize) }; })()`);
        assert.equal(fit.input, true); assert.equal(fit.label, true); assert.equal(fit.line, true);
        assert.ok(fit.docOver <= 0, 'no sideways scroll'); assert.ok(fit.h >= 44, 'the field is a 44 px touch target: ' + fit.h); assert.ok(fit.size >= 20, 'its line grows with the text size: ' + fit.size);
        const del = await ev(`(() => { const b = document.getElementById('settings-received-unlisted-delete'); const r = b.getBoundingClientRect(); const box = document.querySelector('#settings-modal .modal-content, #settings-modal .modal') || document.getElementById('settings-modal'); const br = box.getBoundingClientRect(); return { h: r.height, w: r.width, inside: r.left >= br.left - 1 && r.right <= br.right + 1, visible: r.width > 0 }; })()`);
        assert.ok(del.visible && del.inside, 'the delete button is on screen, inside the dialog'); assert.ok(del.h >= 44, 'and a 44 px touch target: ' + del.h);
        await shot('received-cap-phone-200');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
