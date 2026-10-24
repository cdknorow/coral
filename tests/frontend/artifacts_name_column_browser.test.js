// The artifacts list keeps its Name readable (#6751, the operator's report): in the expanded list Name was squeezed to three letters ("ima...") with the file icon stacked above it, while Type and Created kept their
// fixed widths. Name gets the free width: the icon sits beside the name on one line, the name is one line with an ellipsis only when it is truly long (the whole name is its title), Type and Created step aside
// before Name drops under a readable width, and Created is short (today 9:46, yesterday, Oct 21). Measured at several panel widths on a desktop and on a 390 px phone, against a stubbed list.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');

const HEX = c => c.repeat(64);

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const sleep = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the panel lays out after a width change; there is no completion event
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const team = 'name-team-' + Date.now();
    const now = new Date(), yest = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1, 12), old = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 60, 12); // noon of the calendar day, so 'yesterday' is yesterday at any hour (now - 1.2 days was two days back before 04:48)
    const NAMES = ['image-2026-10-21.png', 'orchestrator-notes.md', 'a-very-long-report-name-that-keeps-going-and-going-for-a-whole-quarter-of-the-year-final-v3.txt'];
    const ITEMS = [now, yest, old].map((d, i) => ({ id: HEX('abc'[i]), name: NAMES[i], media_type: ['image/png', 'text/markdown', 'text/plain'][i], size: 1000 + i, task_id: 20 + i, task_title: 'T' + i, created_at: d.toISOString(), uri: 'coral://artifacts/' + HEX('abc'[i]), produced_by: 'orc' }));
    const measure = () => ev(`(() => [...document.querySelectorAll('tr.team-artifact-row')].map(row => { const b = row.querySelector('.team-artifact-name'), ic = b.querySelector('svg, .file-icon, .material-icons, img') || b.firstElementChild, tx = b.querySelector('.team-artifact-name-text'); const br = row.querySelector('td.team-artifact-col-name').getBoundingClientRect(), ir = ic && ic.getBoundingClientRect(), tr = tx && tx.getBoundingClientRect(); const cr = row.querySelector('td.team-artifact-col-created'); return { nameW: Math.round(br.width), iconTop: ir && Math.round(ir.top), textTop: tr && Math.round(tr.top), textH: tr && Math.round(tr.height), lh: tx && parseFloat(getComputedStyle(tx).lineHeight), title: b.title, createdShown: !!cr && getComputedStyle(cr).display !== 'none', created: cr ? cr.textContent.trim() : null, typeShown: getComputedStyle(row.querySelector('td.team-artifact-col-type')).display !== 'none', cut: tx ? tx.scrollWidth > tx.clientWidth : null, ...(() => { const mid = e => { if (!e) return null; const r = document.createRange(); r.selectNodeContents(e); const b = r.getBoundingClientRect(); return b.width ? (b.top + b.bottom) / 2 : null; }; const act = row.querySelector('td.team-artifact-col-actions button, td.team-artifact-col-actions a'); const ar = act && act.getBoundingClientRect(); return { midName: mid(tx), midCreated: cr && getComputedStyle(cr).display !== 'none' ? mid(cr) : null, midAction: ar ? (ar.top + ar.bottom) / 2 : null }; })() }; }))()`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const R = window.fetch; window.__items = ${JSON.stringify(ITEMS)};
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            window.fetch = (u, o = {}) => { const url = new URL(String(u), location.origin); const p = url.pathname;
                if (p.startsWith('/api/board/') && p.endsWith('/artifacts')) return json({ project: decodeURIComponent(p.split('/')[3]), artifacts: window.__items, has_more: false, truncated: false });
                if (p === '/api/system/artifact-store/kept') return json({ kept: [] });
                if (p.startsWith('/api/')) return json({}); return R(u, o); };
            return true; })()`);
        await ev(`(async () => { const {state} = await import((window.__coralStatic||'/static/')+'state.js'); const files = await import((window.__coralStatic||'/static/')+'changed_files.js');
            state.currentSession = { type: 'live', name: 'nm-ui', session_id: 'nm-a', board_project: ${JSON.stringify(team)} };
            const {showView} = await import((window.__coralStatic||'/static/')+'utils.js'); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); document.getElementById('agentic-tab-team-artifacts').click(); true`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3`, 'the three rows', 8000);

        for (const w of [1000, 800, 680, 560, 480]) {
            await ev(`document.getElementById('agentic-state').style.width = '${w}px'; true`); await sleep(500); // sleep-ok: the container query re-lays the table out; nothing to wait on
            const rows = await measure(); const at = `panel ${w}px: `;
            for (const [i, r] of rows.entries()) {
                assert.ok(r.nameW >= 200, at + `the Name column keeps a readable width (>= 200 px), row ${i}: ${JSON.stringify(r)}`);
                assert.ok(r.iconTop !== null && Math.abs(r.iconTop - r.textTop) <= 8, at + `the icon sits beside the name's first line, not above it: icon top ${r.iconTop}, text top ${r.textTop}`);
                assert.ok(r.textH <= r.lh * 1.5, at + `the name is one line: ${r.textH} px high`);
                assert.equal(r.title, NAMES[i], at + 'the whole name is its title');
                for (const k of ['midCreated', 'midAction']) if (r[k] !== null) assert.ok(Math.abs(r[k] - r.midName) <= 3, at + `the name and the ${k === 'midAction' ? 'action icons' : 'Created time'} sit on one line: name centre ${r.midName}, ${k} ${r[k]}`);
                if (r.createdShown) assert.match(r.created, i === 0 ? /^today \d{1,2}:\d{2}$/ : i === 1 ? /^yesterday$/ : /^[A-Z][a-z]{2} \d{1,2}(, \d{4})?$/, at + 'Created is short: ' + r.created);
            }
            assert.equal(rows[0].cut, false, at + 'a short name is not cut');
            if (w === 680) await shot('artifacts-name-680');
            if (w === 480) await shot('artifacts-name-480');
        }
        // the whole time is still in the row's details
        assert.ok(await ev(`[...document.querySelectorAll('.team-artifact-details')].some(d => /Created/.test(d.textContent) && /\\d{4}|\\d{1,2}:\\d{2}/.test(d.textContent))`), 'Details keep the full Created');

        // a phone
        await Emulation.setDeviceMetricsOverride({ width: 390, height: 844, deviceScaleFactor: 2, mobile: true }); await Emulation.setTouchEmulationEnabled({ enabled: true }); await sleep(700); // sleep-ok: the viewport change reaches the page's layout
        await ev(`(() => { const side = document.getElementById('agentic-state'); side.style.width = ''; if (!side.checkVisibility()) { const t = document.getElementById('panel-toggle-btn') || document.getElementById('popout-panel-toggle-btn'); t && t.click(); } document.getElementById('agentic-tab-team-artifacts').click(); return true; })()`);
        await waitFor(`document.querySelectorAll('tr.team-artifact-row').length === 3 && document.querySelector('tr.team-artifact-row').getBoundingClientRect().width > 0`, 'the rows on a phone', 8000);
        const phone = await measure();
        for (const [i, r] of phone.entries()) {
            assert.ok(r.nameW >= 150, `phone: Name keeps a readable width: ${JSON.stringify(r)}`);
            assert.ok(r.iconTop !== null && Math.abs(r.iconTop - r.textTop) <= 8, `phone: the icon sits beside the name: ${r.iconTop} / ${r.textTop}`);
            assert.ok(r.textH <= r.lh * 1.5, 'phone: the name is one line');
        }
        await shot('artifacts-name-phone');
        console.log('PASS artifacts list: Name keeps its width');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
