// #4469 (UX's #4456 pass, items 1 to 8 and #4460's NIT): the Team Artifacts table, driven by keys and measured, against stubbed lists.
//  - the Actions column holds its five icons and no icon sits over the Created time or the file name; on a touch phone every control is a 44 px target and the icons wrap
//  - the origin chip is one line with an ellipsis and the whole text in its title
//  - an error is red and an empty list shows its line without a table; "1 artifact"; later senders of one file are a line each with a gap
//  - Delete by keyboard: the confirmation, then Yes, and the focus lands on the next row's first control (the previous row's when the last one went, Refresh when none is left), never the page
//  - the outcome of a delete is in a live region that is always in the page; Retry leaves the focus on the panel, not the page; Refresh keeps the focus while it loads
// Run against an isolated dev server. CORAL_SCREENSHOT_DIR=<dir> saves screenshots.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const HEX = c => c.repeat(64);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the panel rebuilds after a stubbed answer, a layout settles; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const press = async (key, code, vk) => { const b = { key, code, windowsVirtualKeyCode: vk }; await Input.dispatchKeyEvent({ type: 'keyDown', ...b, ...(key === 'Enter' ? { text: '\r' } : {}) }); await Input.dispatchKeyEvent({ type: 'keyUp', ...b }); };
    const active = () => ev(`(() => { const a = document.activeElement; if (!a || a === document.body) return { body: true }; const row = a.closest('tr.team-artifact-row'); return { cls: String(a.className), label: (a.getAttribute('aria-label') || a.textContent || '').trim().slice(0, 40), row: row ? [...row.parentElement.children].indexOf(row) : -1, inModal: !!a.closest('#confirm-modal'), id: a.id }; })()`);
    const team = 'ux-team-' + Date.now();
    const ITEMS = [0, 1, 2].map(i => ({ id: HEX('abc'[i]), name: `report-${i}.txt`, media_type: 'text/plain', size: 1000 + i, task_id: 20 + i, task_title: 'T' + i, created_at: `2026-10-0${i + 1}T10:00:00Z`, uri: 'coral://artifacts/' + HEX('abc'[i]), available: true, source: 'completion', provenance: 'uploaded', produced_by: 'Worker' }));
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__mode = 'normal'; window.__items = ${JSON.stringify(ITEMS)}; window.__lists = 0; window.__deletes = []; window.__release = null;
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const m = (o.method || 'GET').toUpperCase(); const p = url.pathname;
                if (p.startsWith('/api/board/') && p.endsWith('/artifacts')) { window.__lists++; const body = () => json({ project: decodeURIComponent(p.split('/')[3]), artifacts: window.__mode === 'empty' ? [] : window.__items, has_more: false, truncated: false });
                    if (window.__mode === 'error') return json({}, 500); if (window.__mode === 'deferred') return new Promise(res => { window.__release = () => res(body()); }); return body(); }
                if (p === '/api/system/artifact-store/kept') return json({ kept: [] });
                { const d = /^\\/api\\/artifacts\\/([a-f0-9]{64})$/.exec(p); if (d && m === 'DELETE') { window.__deletes.push(d[1]); window.__items = window.__items.filter(i => !i.uri.endsWith(d[1])); return json({ deleted: true, bytes: 1 }); } }
                if (p.startsWith('/api/')) return json({}); return R(u, o); };
            return true; })()`);
        await ev(`(async () => { const {state} = await import((window.__coralStatic||'/static/')+'state.js'); const files = await import((window.__coralStatic||'/static/')+'changed_files.js');
            state.currentSession = { type: 'live', name: 'ux-ui', session_id: 'ux-a', board_project: ${JSON.stringify(team)} };
            const {showView} = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); document.getElementById('agentic-tab-team-artifacts').click(); true`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'the three rows', 8000);

        console.log('1. Desktop: five icons fit their column and sit over nothing');
        const box = sel => `(() => { const r = document.querySelector(${JSON.stringify(sel)}).getBoundingClientRect(); return { l: r.left, r: r.right, t: r.top, b: r.bottom, w: r.width, h: r.height }; })()`;
        const overlap = await ev(`(() => { const hit = (a, b) => a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top; const bad = [];
            for (const row of document.querySelectorAll('tr.team-artifact-row')) { const btns = [...row.querySelectorAll('td.team-artifact-col-actions a, td.team-artifact-col-actions button')].map(b => b.getBoundingClientRect());
                const created = document.createRange(); const cc = row.querySelector('td.team-artifact-col-created'); created.selectNodeContents(cc); const nameBtn = row.querySelector('.team-artifact-name').getBoundingClientRect(); const cr = created.getBoundingClientRect();
                const act = row.querySelector('td.team-artifact-col-actions').getBoundingClientRect();
                for (const b of btns) { if (hit(b, cr)) bad.push('over Created'); if (hit(b, nameBtn)) bad.push('over the name'); if (b.left < act.left - 1 || b.right > act.right + 1) bad.push('outside its column'); }
                if (new Set(btns.map(b => Math.round(b.top))).size !== 1) bad.push('not one row'); }
            return { bad, buttons: document.querySelectorAll('tr.team-artifact-row')[0].querySelectorAll('td.team-artifact-col-actions a, td.team-artifact-col-actions button').length }; })()`);
        assert.deepEqual(overlap.bad, [], 'the icons overprint nothing and stay in their column: ' + overlap.bad.join(', '));
        assert.equal(overlap.buttons, 5, 'Preview, Download, Copy link, Keep, Delete');
        await shot('team-artifacts-ux-desktop');

        console.log('2. A singular count, the live region and a dd gap');
        await ev(`window.__items = [window.__items[0]]; window.switchAgenticTab && 0; true`);
        await ev(`document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').click(); true`);
        await waitFor(`/^1 artifact$/.test((document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-status') || {}).textContent || '')`, '"1 artifact"', 8000);
        await ev(`window.__items = ${JSON.stringify(ITEMS)}; document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').click(); true`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'three rows again', 8000);
        assert.equal(await ev(`(() => { const dl = document.createElement('dl'); const w = document.createElement('div'); w.className = 'team-artifact-details'; w.append(dl); for (const t of ['Also received from', 'a', 'b']) { const e = document.createElement(t === 'Also received from' ? 'dt' : 'dd'); e.textContent = t; dl.append(e); } document.body.append(w);
            const gap = parseFloat(getComputedStyle(dl.querySelectorAll('dd')[1]).marginTop); w.remove(); return gap > 0; })()`), true, 'the second sender line has a gap above it');

        console.log('3. Delete by keyboard: the focus goes to the next row, the previous one, then Refresh');
        const focusDelete = async i => ev(`document.querySelectorAll('tr.team-artifact-row')[${i}].querySelector('.team-artifact-delete').focus(); true`);
        const deleteWithKeys = async () => { await press('Enter', 'Enter', 13); await waitFor(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirmation', 8000); for (let i = 0; i < 4 && (await ev(`document.activeElement && document.activeElement.id`)) !== 'confirm-modal-yes'; i++) await press('Tab', 'Tab', 9); assert.equal(await ev(`document.activeElement && document.activeElement.id`), 'confirm-modal-yes', 'Tab reaches Yes'); await press('Enter', 'Enter', 13); };
        await focusDelete(1);
        await deleteWithKeys();
        await waitFor(`window.__deletes.length === 1 && document.querySelectorAll('tr.team-artifact-row').length === 2`, 'the middle row to go', 8000);
        let a = await active();
        assert.equal(a.body, undefined, 'the focus is not on the page: ' + JSON.stringify(a)); assert.equal(a.row, 1, 'it is on the row that took the deleted one\'s place'); assert.equal(a.inModal, false);
        assert.match(await ev(`document.querySelector('#agentic-panel-team-artifacts-live').textContent`), /^Deleted report-1\.txt\.$/, 'the live region says it');
        assert.equal(await ev(`document.querySelector('.team-artifacts-notice') && document.querySelector('.team-artifacts-notice').getAttribute('aria-hidden')`), 'true', 'the visible line is not read twice');
        await focusDelete(1); // the last row
        await deleteWithKeys();
        await waitFor(`window.__deletes.length === 2 && document.querySelectorAll('tr.team-artifact-row').length === 1`, 'the last row to go', 8000);
        a = await active();
        assert.equal(a.body, undefined, JSON.stringify(a)); assert.equal(a.row, 0, 'the row before it when the last one went');
        await focusDelete(0);
        await deleteWithKeys();
        await waitFor(`window.__deletes.length === 3 && document.querySelectorAll('tr.team-artifact-row').length === 0`, 'the only row to go', 8000);
        a = await active();
        assert.equal(a.body, undefined, JSON.stringify(a)); assert.equal(a.label, 'Refresh', 'Refresh when nothing is left');
        assert.equal(await ev(`!!document.querySelector('.team-artifacts-list')`), false, 'an empty list is its line, with no table header over nothing');

        console.log('4. An error is red, has no table, and Retry keeps the focus on the panel; Refresh keeps it while loading');
        await ev(`window.__mode = 'error'; document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').click(); true`);
        await waitFor(`!!document.querySelector('.team-artifacts-retry')`, 'the error with Retry', 8000);
        const err = await ev(`(() => { const s = document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-status'); const calm = document.createElement('div'); calm.className = 'team-artifacts-status'; document.querySelector('#agentic-panel-team-artifacts-view').append(calm); const c = getComputedStyle(calm).color; calm.remove();
            return { state: s.dataset.state, color: getComputedStyle(s).color, calm: c, role: s.getAttribute('role'), table: !!document.querySelector('.team-artifacts-list') }; })()`);
        assert.equal(err.state, 'error'); assert.notEqual(err.color, err.calm, 'the error is not the grey of the other lines'); assert.equal(err.role, 'alert'); assert.equal(err.table, false);
        await shot('team-artifacts-ux-error');
        await ev(`window.__mode = 'normal'; window.__items = ${JSON.stringify(ITEMS)}; document.querySelector('.team-artifacts-retry').focus(); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'the list after Retry', 8000);
        a = await active();
        assert.equal(a.body, undefined, 'after Retry the focus is on the panel: ' + JSON.stringify(a)); assert.equal(a.row, 0, 'on the first row\'s first control');
        // Refresh while the list loads: the focus stays on it, and a second press does not start a second load.
        await ev(`window.__mode = 'deferred'; window.__lists = 0; document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').focus(); true`);
        await press('Enter', 'Enter', 13);
        await waitFor(`window.__lists === 1 && !!window.__release`, 'the load to start', 8000);
        a = await active();
        assert.equal(a.label, 'Refresh', 'the focus stays on Refresh while it loads: ' + JSON.stringify(a));
        assert.equal(await ev(`document.activeElement.getAttribute('aria-disabled')`), 'true', 'it says it is busy');
        await press('Enter', 'Enter', 13); await sleep(200); // sleep-ok: shows that a second press does nothing
        assert.equal(await ev(`window.__lists`), 1, 'a second press starts no second load');
        await ev(`window.__release(); true`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3 && !document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').hasAttribute('aria-disabled')`, 'the load to end', 8000);
        await ev(`window.__mode = 'normal'; true`);

        console.log('5. A touch phone at 100% and 200% text: 44 px targets, icons wrapped, nothing over the name, one-line chip');
        await ev(`window.__items = [Object.assign({}, window.__items[0], { id: 'shared:x', source: 'shared', shared_to: 'a-very-long-server-name-that-goes-on-and-on/the-board-with-a-long-name' })].concat(window.__items.slice(1)); true`);
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
        await Emulation.setTouchEmulationEnabled({ enabled: true, maxTouchPoints: 5 });
        await ev(`(document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); true`); await sleep(500); // sleep-ok: the drawer opens over frames
        await ev(`window.switchAgenticTab('team-artifacts', 'top'); true`); await sleep(300); // sleep-ok: the tab shows over frames
        for (const size of ['', '32px']) {
            await ev(`document.documentElement.style.fontSize = '${size}'; document.getElementById('agentic-state').style.width = ''; true`);
            await ev(`document.querySelector('#agentic-panel-team-artifacts-view .team-artifacts-heading .team-artifacts-action').click(); true`);
            await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'the rows on the phone', 8000);
            await sleep(400); // sleep-ok: layout settles
            const m = await ev(`(() => { const hit = (a, b) => a.left < b.right - 1 && a.right > b.left + 1 && a.top < b.bottom - 1 && a.bottom > b.top + 1; const small = [], over = [];
                for (const row of document.querySelectorAll('tr.team-artifact-row')) { const nameR = row.querySelector('.team-artifact-name').getBoundingClientRect();
                    for (const b of row.querySelectorAll('td.team-artifact-col-actions a, td.team-artifact-col-actions button')) { const r = b.getBoundingClientRect(); if (r.width < 43.5 || r.height < 43.5) small.push((b.getAttribute('aria-label') || '').slice(0, 20) + ' ' + Math.round(r.width) + 'x' + Math.round(r.height)); if (hit(r, nameR)) over.push((b.getAttribute('aria-label') || '').slice(0, 20)); }
                    const sm = row.querySelector('.team-artifact-details summary').getBoundingClientRect(); if (sm.height < 43.5) small.push('Details ' + Math.round(sm.height));
                    const nb = row.querySelector('button.team-artifact-name'); if (nb && nb.getBoundingClientRect().height < 43.5) small.push('name ' + Math.round(nb.getBoundingClientRect().height)); }
                const help = document.querySelector('.team-artifacts-help').getBoundingClientRect(); if (help.width < 43.5) small.push('help ' + Math.round(help.width));
                const chip = document.querySelector('.team-artifact-badge-shared'); const cs = getComputedStyle(chip); const lh = parseFloat(cs.lineHeight);
                const rows = [...document.querySelectorAll('tr.team-artifact-row')[0].querySelectorAll('td.team-artifact-col-actions a, td.team-artifact-col-actions button')].map(b => Math.round(b.getBoundingClientRect().top));
                return { small, over, chipLines: Math.round(chip.getBoundingClientRect().height / lh), chipNowrap: cs.whiteSpace, chipTitle: chip.title, chipEllipsis: cs.textOverflow, iconRows: new Set(rows).size, docOver: document.documentElement.scrollWidth - innerWidth }; })()`);
            assert.deepEqual(m.small, [], `${size || '100%'}: every control is a 44 px target`);
            assert.deepEqual(m.over, [], `${size || '100%'}: no icon over the file name`);
            assert.ok(m.chipLines <= 2, `${size || '100%'}: the chip is one line (${m.chipLines})`); assert.equal(m.chipNowrap, 'nowrap'); assert.equal(m.chipEllipsis, 'ellipsis'); assert.match(m.chipTitle, /^Sent to a-very-long-server-name/, 'the whole text is its title');
            assert.ok(m.iconRows >= 2, `${size || '100%'}: the five icons wrap into more than one row on a phone (${m.iconRows})`);
            assert.ok(m.docOver <= 0, `${size || '100%'}: no sideways scroll`);
            await shot(`team-artifacts-ux-phone${size ? '-200' : ''}`);
        }
        console.log('PASS');
    } catch (e) {
        console.error(e && e.stack || e);
        process.exitCode = 1;
    } finally {
        try { await client.close(); } catch (_) {}
    }
})();
