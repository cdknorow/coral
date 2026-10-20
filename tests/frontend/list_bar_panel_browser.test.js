// The Tasks panel and the Team Artifacts list use the agents tab's pattern for filters (#5293; operator: "Let's follow the same pattern for filters that we have in our agents tab with the expandable icon that
// shows it."): one row with the search, the count and an icon; the icon opens a panel under the row with the fields, the Sort and Clear filters; no Filters button, no Sort row. A dot and the name ("Filters and sort,
// 2 on") say that something is on while the panel is shut, and the chips of what is on stay under the row; the panel's state is kept per board like the filters; the artifact list has the second icon (open or
// close every row's details). Desktop and a 390 px phone (44 px targets), keyboard (Enter opens and moves into the panel, Escape shuts and returns), names. Uses the shared panel fixtures (helpers/list_bars_panels.js).
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit } = require('./helpers/contrast');
const BASE = require('./test_base').BASE;
const PANELS = require('./helpers/list_bars_panels');
const { removeBoards } = require('./helpers/board_cleanup');

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: a debounce and a redraw over frames; there is no event for "nothing happened"
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const press = async (key, code, vk) => { const t = key === 'Enter' ? '\r' : undefined; await Input.dispatchKeyEvent({ type: t ? 'keyDown' : 'rawKeyDown', key, code, windowsVirtualKeyCode: vk, text: t }); await Input.dispatchKeyEvent({ type: 'keyUp', key, code, windowsVirtualKeyCode: vk }); await settle(80); };
    const ctx = { ev, waitFor, Page, Emulation, client, install, BASE, assert, settle: (c, l) => waitFor(c, l, 10000) };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const key of ['tasks', 'artifacts']) {
            const panel = PANELS.find(p => p.key === key);
            await panel.prepare?.(ctx);
            const root = key === 'tasks' ? '#board-task-bar' : '#agentic-panel-team-artifacts';
            const label = key === 'tasks' ? 'Tasks' : 'Team Artifacts';
            for (const view of [{ name: 'desktop', w: 1440, phone: false, theme: 'dark' }, { name: '390px', w: 390, phone: true, theme: 'light' }]) {
                const tag = `${label}, ${view.name} ${view.theme}`; console.log(tag);
                ctx.__phone = view.phone;
                await Emulation.setDeviceMetricsOverride({ width: view.w, height: 900, deviceScaleFactor: 1, mobile: view.phone }); await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
                await panel.show(ctx, { w: view.w, theme: view.theme, phone: view.phone });
                await setTheme(ev, view.theme);
                // the fixture leaves the panel open with a filter on: shut it and clear what is on, to start from the default
                await ev(`(() => { const b = document.querySelector('${root} .list-bar-bar'); const t = b.querySelector('.list-bar-toggle'); if (t.getAttribute('aria-expanded') === 'true') t.click(); const c = b.querySelector('.list-bar-clear-all'); c.hidden || c.click(); return true; })()`); await settle(900);
                const q = (sel) => ev(`(() => { const b = document.querySelector('${root} .list-bar-bar'); const e = b.querySelector(${JSON.stringify(sel)}); if (!e) return null; const r = e.getBoundingClientRect(); return { shown: e.getClientRects().length > 0, w: Math.round(r.width), h: Math.round(r.height), top: Math.round(r.top), text: (e.textContent || '').trim(), label: e.getAttribute('aria-label'), expanded: e.getAttribute('aria-expanded'), controls: e.getAttribute('aria-controls'), cls: e.className }; })()`);
                const tog = await q('.list-bar-toggle'), search = await q('.list-bar-search'), count = await q('.list-bar-count');
                check(`${tag}: one row: the search, the count and the icon (${JSON.stringify([search && search.top, count && count.top, tog && tog.top])}), no Filters button, no Sort row`, tog && tog.shown && !(await q('.list-bar-filter-btn')) && Math.abs(tog.top - search.top) < 20 && (count.shown ? /\d/.test(count.text) : key === 'artifacts' && view.phone) && !(await ev(`!!([...document.querySelectorAll('${root} .list-bar-sort')].find(e => e.getClientRects().length))`)));
                check(`${tag}: the icon is named, says it is shut, and controls the panel (${JSON.stringify([tog.label, tog.expanded, tog.controls])})`, tog.label === 'Filters and sort' && tog.expanded === 'false' && !!tog.controls && !(await q('.list-bar-menu')).shown);
                if (view.phone) check(`${tag}: a 44 px target (${tog.w}x${tog.h})`, tog.w >= 44 && tog.h >= 44);
                // open by keyboard: focus goes into the panel, which holds the fields, the Sort and Clear filters
                await ev(`document.querySelector('${root} .list-bar-toggle').focus(); true`); await press('Enter', 'Enter', 13);
                const op = await ev(`(() => { const b = document.querySelector('${root} .list-bar-bar'); const m = b.querySelector('.list-bar-menu'); return { open: !m.hidden, expanded: b.querySelector('.list-bar-toggle').getAttribute('aria-expanded'), focusIn: m.contains(document.activeElement), sort: !!m.querySelector('select[aria-label^="Sort"]'), clear: !!m.querySelector('.list-bar-clear-all'), fields: m.querySelectorAll('select, input').length }; })()`);
                check(`${tag}: Enter opens the panel and moves focus into it; it holds the fields, the Sort and Clear filters (${JSON.stringify(op)})`, op.open && op.expanded === 'true' && op.focusIn && op.sort && op.clear && op.fields >= 3);
                // every field spans the panel: the text field (Agent, Uploaded by) is as wide as the selects, not a narrow box pushed aside
                const wd = await ev(`(() => { const m = document.querySelector('${root} .list-bar-menu'); const r = e => e.getBoundingClientRect(); const sel = m.querySelector('select'); const txt = m.querySelector('input[type=text]'); return txt ? { sel: Math.round(r(sel).right), txt: Math.round(r(txt).right), selW: Math.round(r(sel).width), txtW: Math.round(r(txt).width) } : null; })()`);
                check(`${tag}: the text field is as wide as the selects (${JSON.stringify(wd)})`, wd && Math.abs(wd.sel - wd.txt) <= 4 && wd.txtW >= wd.selW - 8);
                await shot(`list-bar-panel-${key}-${view.name}-open`);
                // a filter: the dot, the name and the chip while the panel is shut
                await ev(`(() => { const b = document.querySelector('${root} .list-bar-bar'); const s = [...b.querySelectorAll('.list-bar-menu select')].find(x => !/^Sort/.test(x.getAttribute('aria-label') || '')); s.value = s.options[1].value; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`); await settle(900);
                await press('Escape', 'Escape', 27);
                const on = await ev(`(() => { const b = document.querySelector('${root} .list-bar-bar'); const t = b.querySelector('.list-bar-toggle'); const badge = t.querySelector('.display-badge'); const chips = b.querySelector('.list-bar-chips'); return { shut: b.querySelector('.list-bar-menu').hidden, back: document.activeElement === t, dot: !badge.hidden, active: t.classList.contains('active'), name: t.getAttribute('aria-label'), chips: !chips.hidden && chips.children.length, count: b.querySelector('.list-bar-count').textContent }; })()`);
                check(`${tag}: a filter on, the panel shut by Escape (focus back on the icon): a dot, the name "Filters and sort, 1 on", and the chip stays visible (${JSON.stringify(on)})`, on.shut && on.back && on.dot && on.active && on.name === 'Filters and sort, 1 on' && on.chips >= 1 && /\d/.test(on.count));
                await shot(`list-bar-panel-${key}-${view.name}-filtered`);
                // the state is kept per board: open writes it, shut removes it
                const kept = async () => ev(`Object.keys(localStorage).filter(k => k.startsWith('coral-list-bar-open:${key === 'tasks' ? 'tasks' : 'team-artifacts'}:'))`);
                await ev(`document.querySelector('${root} .list-bar-toggle').click(); true`);
                const k1 = await kept();
                await ev(`document.querySelector('${root} .list-bar-toggle').click(); true`);
                const k2 = await kept();
                check(`${tag}: open or shut is kept for this board (${JSON.stringify(k1)} then ${JSON.stringify(k2)})`, k1.length === 1 && k2.length === 0);
                // the sort alone counts, and Clear filters takes the filters away
                await ev(`document.querySelector('${root} .list-bar-toggle').click(); (() => { const s = document.querySelector('${root} .list-bar-menu select[aria-label^="Sort"]'); s.value = s.options[s.options.length - 1].value; s.dispatchEvent(new Event('change', { bubbles: true })); })(); true`); await settle(900);
                check(`${tag}: a changed sort counts too ("${await ev(`document.querySelector('${root} .list-bar-toggle').getAttribute('aria-label')`)}")`, (await ev(`document.querySelector('${root} .list-bar-toggle').getAttribute('aria-label')`)) === 'Filters and sort, 2 on');
                await ev(`document.querySelector('${root} .list-bar-clear-all').click(); true`); await settle(900);
                const cl = await ev(`(() => { const t = document.querySelector('${root} .list-bar-toggle'); return { dot: !t.querySelector('.display-badge').hidden, name: t.getAttribute('aria-label') }; })()`);
                check(`${tag}: Clear filters: no dot, plain name (${JSON.stringify(cl)}); the sort goes back with them? (kept: it is a choice, not a filter)`, !cl.dot === (cl.name === 'Filters and sort') || cl.name === 'Filters and sort, 1 on');
                await ev(`document.querySelector('${root} .list-bar-toggle').click(); true`);
                // the artifact list's second icon: open or close every row's details
                if (key === 'artifacts') {
                    const ex = await ev(`(() => { const c = document.querySelector('${root} .list-bar-expand-all'); const rows = [...document.querySelectorAll('${root} tr.team-artifact-row')]; const before = rows.filter(r => !r.querySelector('.team-artifact-details').hidden).length; const label = c.getAttribute('aria-label'); c.click(); const mid = [...document.querySelectorAll('${root} tr.team-artifact-row')].filter(r => !r.querySelector('.team-artifact-details').hidden).length; return { label, n: rows.length, before, mid, after: document.querySelector('${root} .list-bar-expand-all').getAttribute('aria-label') }; })()`);
                    check(`${tag}: the second icon opens every row's details and then offers to close them (${JSON.stringify(ex)})`, ex.label === 'Expand all details' && ex.before === 0 && ex.mid === ex.n && ex.n > 0 && ex.after === 'Collapse all details');
                    await ev(`document.querySelector('${root} .list-bar-expand-all').click(); true`);
                    check(`${tag}: and closes them again`, (await ev(`[...document.querySelectorAll('${root} tr.team-artifact-row')].filter(r => !r.querySelector('.team-artifact-details').hidden).length`)) === 0);
                } else check(`${tag}: no second icon (task rows have nothing to expand)`, !(await q('.list-bar-expand-all')));
            }
            if (key === 'tasks') await removeBoards(ev, [ctx.TBOARD]);
        }
        const failed = results.filter(x => !x).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await client.close();
        process.exit(failed ? 1 : 0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
