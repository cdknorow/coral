// #4469 (UX's #4456 pass, Settings > Storage, items 9 to 14): against stubbed store numbers.
//  - each open starts from nothing: a failed or slow load leaves the limit fields empty and off (never the last open's numbers), says so, and Save writes neither limit
//  - the artifact store's line has the received files' three states: below 80%, from 80% with what to do, at the limit that new uploads are refused (warning colour); "1 file"
//  - the Prune confirmation uses the page's words (saved diffs, unlinked artifacts, stored diff copies), singular for one; Prune and Shrink by keyboard leave the focus on their own button
//  - the age breakdown is one line per kind; Shrink and Prune sit on one row with their fields
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const GIB = 1024 ** 3;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a redraw and a layout settle over frames; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const press = async (key, code, vk) => { const b = { key, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(key === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    const storeBody = o => ({ bytes: o.bytes, objects: o.objects, max_bytes: o.max, task_diffs: { bytes: 2048, files: o.diffs ?? 1 }, session_diffs: { bytes: 0, files: 0 }, received: { bytes: 0, objects: 0, max_bytes: 2 * GIB, default_bytes: 2 * GIB, notice: false } });
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__puts = []; window.__prunes = []; window.__store = ${JSON.stringify(storeBody({ bytes: 4 * GIB, objects: 120, max: 10 * GIB }))}; window.__down = false; window.__hold = null; window.__dry = null; window.__run = null;
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const p = url.pathname; const m = (o.method || 'GET').toUpperCase(); const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
                if (p === '/api/system/artifact-store' && m === 'GET') {
                    if (url.searchParams.get('detail') === '1') return json({ ...window.__store, detail: { by_age: { objects: { '<1d': { bytes: 1000 }, '1-7d': { bytes: 2000 } }, task_diffs: { '<1d': { bytes: 10 } } }, retention: { days: 0, kinds: 'diffs' } } });
                    if (window.__down) return json({ error: 'x' }, 500); if (window.__hold) return new Promise(res => { window.__release = () => res(window.__holdFail ? new Response('{"error":"x"}', { status: 500 }) : new Response(JSON.stringify(window.__store), { status: 200, headers: { 'content-type': 'application/json' } })); });
                    return json(window.__store); }
                if (p === '/api/artifacts/prune' && m === 'POST') { const b = JSON.parse(o.body); window.__prunes.push(b); return json(b.dry_run ? window.__dry : window.__run); }
                if (p === '/api/settings' && m === 'PUT') { window.__puts.push(JSON.parse(o.body)); return json({}); }
                return R(u, o); };
            return true; })()`);
        const GONE = `(window.hideSettingsModal(), (!document.getElementById('settings-modal') || getComputedStyle(document.getElementById('settings-modal')).display === 'none'))`;
        const close = () => waitFor(GONE, 'Settings to close', 6000);
        const apply = () => ev(`import((window.__coralStatic || '/static/') + 'modals.js').then(m => m.applySettings()).then(() => true)`);
        const line = () => ev(`(document.getElementById('settings-artifact-store-size')?.textContent ?? '')`);

        console.log('1. A failed or slow load leaves the fields empty and off, and Save writes neither limit');
        await ev(`window.showSettingsModal('cleanup'); true`);
        await waitFor(`/^4\\.0 GiB of 10\\.0 GiB/.test((document.getElementById('settings-artifact-store-size')?.textContent ?? ''))`, 'the store line', 8000);
        assert.equal(await ev(`document.getElementById('settings-artifact-store-max').value`), '10', 'the limit is shown');
        await close();
        await ev(`window.__hold = true; window.showSettingsModal('cleanup'); true`);
        await waitFor(`!!window.__release`, 'the slow load', 8000);
        let f = await ev(`(() => { const i = document.getElementById('settings-artifact-store-max'), r = document.getElementById('settings-received-max'); return { line: (document.getElementById('settings-artifact-store-size')?.textContent ?? ''), rline: (document.getElementById('settings-received-size')?.textContent ?? ''), v: i.value, loaded: i.dataset.loaded || '', dis: i.disabled, rv: r.value, rloaded: r.dataset.loaded || '', rdis: r.disabled }; })()`);
        assert.equal(f.line, 'Loading…', 'the line says it is loading, not what it showed last time'); assert.equal(f.rline, 'Loading…');
        assert.equal(f.v, ''); assert.equal(f.loaded, ''); assert.equal(f.dis, true); assert.equal(f.rv, ''); assert.equal(f.rloaded, ''); assert.equal(f.rdis, true);
        await ev(`window.__puts.length = 0; document.getElementById('settings-artifact-store-max').disabled = false; document.getElementById('settings-artifact-store-max').value = '5'; true`);
        await apply();
        assert.ok(!(await ev(`window.__puts.some(b => 'artifact_store_max_bytes' in b)`)), 'a limit that was not read is never saved back, even if the field was typed in meanwhile');
        await ev(`window.__hold = null; window.__release && window.__release(); true`);
        await waitFor(`/^4\\.0 GiB/.test((document.getElementById('settings-artifact-store-size')?.textContent ?? ''))`, 'the late answer', 8000);
        await close();
        await ev(`window.__down = true; window.showSettingsModal('cleanup'); true`);
        await waitFor(`/Couldn't read the store's size, so the limit is not shown\\./.test((document.getElementById('settings-artifact-store-size')?.textContent ?? ''))`, 'the error', 8000);
        f = await ev(`(() => { const i = document.getElementById('settings-artifact-store-max'), r = document.getElementById('settings-received-max'); return { v: i.value, loaded: i.dataset.loaded || '', dis: i.disabled, rv: r.value, rdis: r.disabled, rline: (document.getElementById('settings-received-size')?.textContent ?? '') }; })()`);
        assert.equal(f.v, '', 'not the 10 of the last open'); assert.equal(f.loaded, ''); assert.equal(f.dis, true); assert.equal(f.rv, ''); assert.equal(f.rdis, true);
        assert.match(f.rline, /Couldn't read the received files' size, so their limit is not shown\./);
        await ev(`window.__puts.length = 0; true`);
        await apply();
        assert.ok(!(await ev(`window.__puts.some(b => 'artifact_store_max_bytes' in b || 'received_artifacts_max_bytes' in b)`)), 'Save writes neither limit after a failed load');
        // UX's NITs: a field that is off looks off, and a value typed into it while the load was out is cleared when the load fails.
        const look = await ev(`(() => { const c = getComputedStyle(document.getElementById('settings-artifact-store-max')); return { opacity: Number(c.opacity), cursor: c.cursor }; })()`);
        assert.ok(look.opacity < 1 && look.cursor === 'not-allowed', 'a disabled limit field looks off: ' + JSON.stringify(look));
        await close();
        await ev(`window.__down = false; window.__hold = true; window.__holdFail = true; window.__release = null; window.showSettingsModal('cleanup'); true`);
        await waitFor(`!!window.__release`, 'the slow load that will fail', 8000);
        await ev(`(() => { const i = document.getElementById('settings-artifact-store-max'); i.disabled = false; i.value = '5'; true; })()`);
        await ev(`window.__hold = null; window.__release(); true`);
        await waitFor(`/Couldn't read the store's size/.test((document.getElementById('settings-artifact-store-size')?.textContent ?? ''))`, 'the failure', 8000);
        assert.equal(await ev(`document.getElementById('settings-artifact-store-max').value`), '', 'a value typed while the load was out is not left in the field');
        await ev(`window.__holdFail = false; true`);
        await ev(`window.__down = false; true`);

        console.log('2. The artifact store line: three states, "1 file"');
        const open = async body => { await close(); await ev(`window.__store = ${JSON.stringify(body)}; window.showSettingsModal('cleanup'); true`); await waitFor(`/ GiB| B/.test((document.getElementById('settings-artifact-store-size')?.textContent ?? ''))`, 'the line', 8000); await sleep(300); // sleep-ok: the dialog finishes laying out
            return ev(`(() => { const o = document.getElementById('settings-artifact-store-size'); const span = o.querySelector('span'); return { text: o.textContent, warn: span && span.dataset.state || '', color: getComputedStyle(span).color, other: getComputedStyle(o).color, lines: o.querySelectorAll('br').length + 1 }; })()`); };
        const calm = await open(storeBody({ bytes: 3 * GIB, objects: 5, max: 10 * GIB, diffs: 1 }));
        assert.equal(calm.text.split('Diffs')[0], '3.0 GiB of 10.0 GiB'); assert.equal(calm.warn, ''); assert.match(calm.text, /Diffs: 2 KiB in 1 file$/, '"1 file", not "1 files"');
        const nearly = await open(storeBody({ bytes: 8.5 * GIB, objects: 5, max: 10 * GIB }));
        assert.match(nearly.text, /^8\.5 GiB of 10\.0 GiB \(85% full\)\. Delete artifacts in Team Artifacts to make room, or raise the limit\./); assert.equal(nearly.warn, '');
        const full = await open(storeBody({ bytes: 10.4 * GIB, objects: 1, max: 10 * GIB }));
        assert.match(full.text, /^10\.4 GiB of 10\.0 GiB: new uploads are refused\. Delete artifacts in Team Artifacts to make room, or raise the limit\./);
        assert.equal(full.warn, 'warn', 'at the limit the line is in the warning colour'); assert.notEqual(full.color, nearly.other, 'a colour that differs from the calm one');
        await ev(`document.getElementById('settings-artifact-store-size').scrollIntoView({ block: 'center' }); true`); await sleep(300); // sleep-ok: the scroll settles for the screenshot
        await shot('storage-ux-at-limit');
        const nolimit = await open(storeBody({ bytes: 3 * GIB, objects: 5, max: 0 }));
        assert.equal(nolimit.text.split('Diffs')[0], '3.0 GiB', 'no limit: just the total');
        // #4602: a limit Coral raised by itself when the 20 GiB default arrived is said, while it is still the saved limit.
        const raised = await open({ ...storeBody({ bytes: 17 * GIB, objects: 9, max: 34 * GIB }), auto_raised: 34 * GIB });
        assert.match(raised.text, /^17\.0 GiB of 34\.0 GiB\. The limit was raised to 34\.0 GiB automatically when the 20 GiB default arrived, because the store already held more than 16 GiB\. Change it here\./);
        const changed = await open({ ...storeBody({ bytes: 17 * GIB, objects: 9, max: 50 * GIB }), auto_raised: 34 * GIB });
        assert.doesNotMatch(changed.text, /raised to/, 'once the Operator saved another limit the note is gone');

        console.log('3. One line per kind in the age breakdown');
        await waitFor(`/Artifacts by age: /.test((document.getElementById('settings-storage-detail')?.textContent ?? ''))`, 'the breakdown', 8000);
        const ages = await ev(`[...document.getElementById('settings-storage-detail').childNodes].filter(n => n.nodeType === 3).map(n => n.textContent)`);
        assert.match(ages[0], /^Files: \d+ artifacts, \d+ saved diffs$/, 'the file counts moved here from the limit lines'); assert.match(ages[1], /^Artifacts by age: <1d /); assert.match(ages[2], /^Task diffs by age: <1d /);
        assert.equal(await ev(`document.querySelector('.settings-storage-breakdown').open`), false, 'the breakdown is closed until asked');

        console.log('4. Shrink and Prune sit on one row with their fields');
        await ev(`document.getElementById('settings-storage-prune').scrollIntoView({ block: 'center' }); true`); await sleep(300); // sleep-ok: the scroll settles
        const row = await ev(`(() => { const t = id => Math.round(document.getElementById(id).getBoundingClientRect().top); const h = id => Math.round(document.getElementById(id).getBoundingClientRect().height); const lab = document.querySelector('.settings-controls-row .settings-inline').getBoundingClientRect(); const inp = document.getElementById('settings-prune-days').getBoundingClientRect();
            return { shrink: t('settings-storage-shrink'), prune: t('settings-storage-prune'), kind: t('settings-prune-kind'), labelH: Math.round(lab.height), inputInLabel: inp.right <= lab.right + 1 }; })()`);
        assert.ok(Math.abs(row.shrink - row.prune) <= 2, 'Shrink and Prune side by side: ' + JSON.stringify(row)); assert.ok(row.labelH < 50, '"Older than [14] days" is one line: ' + JSON.stringify(row));
        await shot('storage-ux-controls');

        console.log('5. Prune and Shrink by keyboard: the page\'s words, and the focus comes back to the button that asked');
        const focusAsk = id => ev(`document.getElementById('${id}').focus(); true`);
        const yesWithKeys = async () => { await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000); await waitFor(`!!document.activeElement.closest('#confirm-modal')`, 'focus on the question', 4000); for (let i = 0; i < 4 && (await ev(`document.activeElement && document.activeElement.id`)) !== 'confirm-modal-yes'; i++) await press('Tab', 'Tab', 9); assert.equal(await ev(`document.activeElement.id`), 'confirm-modal-yes'); await press('Enter', 'Enter', 13); };
        await ev(`window.__dry = { kinds: { diffs: { count: 14, bytes: ${0.4 * GIB} }, objects: { count: 3, bytes: ${0.8 * GIB} }, 'diff-objects': { count: 2, bytes: 10485760 } }, freed: ${1.3 * GIB} }; window.__run = { freed: ${1.3 * GIB}, removed: 19, skipped: 1, errors: 0 }; true`);
        await focusAsk('settings-storage-prune');
        await press('Enter', 'Enter', 13);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the Prune confirmation', 8000);
        const ask = await ev(`document.getElementById('confirm-modal-message').textContent`);
        assert.match(ask, /^Prune 14 saved diffs \(0\.4 GiB\), 3 unlinked artifacts \(0\.8 GiB\), 2 stored diff copies \(10 MiB\), freeing about 1\.3 GiB\./);
        assert.doesNotMatch(ask, /(^|, |Prune )(diffs|objects|diff-objects) \(/, "never the server's kind names");
        await yesWithKeys();
        await waitFor(`/^Freed 1\\.3 GiB from 19 items; 1 left alone because it changed\\.$/.test((document.getElementById('settings-storage-result')?.textContent ?? ''))`, 'the result', 8000);
        assert.equal(await ev(`document.activeElement.id`), 'settings-storage-prune', 'the focus is back on Prune, not the page');
        await ev(`window.__dry = { kinds: { diffs: { count: 1, bytes: 1048576 } }, freed: 1048576 }; window.__run = { freed: 1048576, removed: 1, skipped: 0, errors: 0 }; true`);
        await focusAsk('settings-storage-shrink');
        await press('Enter', 'Enter', 13);
        await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the Shrink confirmation', 8000);
        assert.match(await ev(`document.getElementById('confirm-modal-message').textContent`), /^Shrink 1 saved diff \(1 MiB\), saving about 1 MiB\./, 'the singular');
        await yesWithKeys();
        await waitFor(`/^Saved 1 MiB from 1 item\\.$/.test((document.getElementById('settings-storage-result')?.textContent ?? ''))`, 'the Shrink result', 8000);
        assert.equal(await ev(`document.activeElement.id`), 'settings-storage-shrink', 'the focus is back on Shrink');
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
