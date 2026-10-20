// The agent's Artifacts tab and Team Artifacts, cleaned up (#5283; operator: "detail expand can be an info icon in the actions. The refresh can be a reload icon. We are also missing the filters that we added to the
// team artifact search."): (1) a row's Details is an (i) icon button in the Actions column (labelled, aria-expanded, 44 px on a phone, by keyboard), not a "▶ Details" disclosure under the name, in both panels;
// (2) Refresh is a reload icon button (aria-label and tooltip Refresh) in both panels; (3) an agent's Artifacts tab has the Team Artifacts search, Filters, Sort and "N of M", scoped to that agent: the same
// component (team_artifact_bar.js), the same query (session_id plus q, kind, sort ...), kept per agent; an agent with no team (its list is not the board's) has none. Desktop and 390 px, both themes; names (the
// accessibility tree) and 4.5:1 contrast. The board route is stood in for by a page-side stub that applies the contract and records every request. Drives Chrome over CDP through the harness.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');

const HEX = n => n.toString(16).padStart(2, '0').repeat(32);
const TEAM = 'aa-team-' + Date.now().toString(36);
const DATA = ['alpha.png', 'bravo.pdf', 'charlie.txt', 'delta.zip', 'echo.png', 'zebra-plan.md', 'foxtrot.txt'].map((name, i) => ({ id: HEX(i + 1), name, media_type: ['image/png', 'application/pdf', 'text/plain', 'application/zip', 'image/png', 'text/markdown', 'text/plain'][i], size: (i + 1) * 300,
    task_id: 100 + i, task_title: 'Task ' + (i + 1), created_at: new Date(Date.UTC(2026, 9, 10 - i, 10)).toISOString(), uri: 'coral://artifacts/' + HEX(i + 1), available: true, inline: false, reference_count: 1, source: 'task', produced_by: 'Dev One' }));

const STUB = `(() => {
    const R = window.fetch; window.__req = []; window.__data = ${JSON.stringify(DATA)};
    const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
    window.fetch = (u, o = {}) => {
        const url = new URL(String(u), location.origin); const q = url.searchParams;
        const m = url.pathname.match(/^\\/api\\/board\\/([^/]+)\\/artifacts$/);
        if (m && (o.method || 'GET').toUpperCase() === 'GET') {
            window.__req.push({ kind: 'board', project: decodeURIComponent(m[1]), query: Object.fromEntries(q.entries()) });
            let items = window.__data.slice();
            if (q.get('q')) { const t = q.get('q').toLowerCase(); items = items.filter(i => [i.name, i.task_title, i.produced_by, i.media_type].some(x => (x || '').toLowerCase().includes(t))); }
            if (q.get('kind')) { const k = q.get('kind'); items = items.filter(i => (i.media_type.startsWith('image/') ? 'image' : i.media_type === 'application/pdf' ? 'pdf' : i.media_type.startsWith('text/') ? 'text' : 'other') === k); }
            const sort = q.get('sort') || 'newest';
            items.sort((a, b) => sort === 'name' ? a.name.localeCompare(b.name) : sort === 'oldest' ? a.created_at.localeCompare(b.created_at) : b.created_at.localeCompare(a.created_at));
            const body = { project: decodeURIComponent(m[1]), artifacts: items, has_more: false, truncated: false };
            if (q.get('session_id')) body.session_id = q.get('session_id');
            if (q.has('sort')) { body.total = items.length; body.all = window.__data.length; }
            return json(body);
        }
        if (url.pathname === '/api/session-artifacts') { window.__req.push({ kind: 'standalone', query: Object.fromEntries(q.entries()) }); return json({ session_id: q.get('session_id'), artifacts: window.__data.slice(0, 3), has_more: false, truncated: false }); }
        if (url.pathname === '/api/system/artifact-store/kept') return json({ kept: [] });
        return R(u, o);
    };
    return true;
})()`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Accessibility, DOM } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = ms => new Promise(r => setTimeout(r, ms)); // sleep-ok: the search is debounced and a panel redraws over frames; there is no event for "nothing was sent"
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await settle(350); fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); }; // sleep-ok: tab fades
    const press = async (key, code, vk) => { const t = key === 'Enter' ? '\r' : undefined; await Input.dispatchKeyEvent({ type: t ? 'keyDown' : 'rawKeyDown', key, code, windowsVirtualKeyCode: vk, text: t }); await Input.dispatchKeyEvent({ type: 'keyUp', key, code, windowsVirtualKeyCode: vk }); await settle(60); };
    const type = async text => { for (const ch of text) { await Input.dispatchKeyEvent({ type: 'keyDown', key: ch, text: ch }); await Input.dispatchKeyEvent({ type: 'keyUp', key: ch }); } };
    const INTERACTIVE = new Set(['button', 'textbox', 'checkbox', 'link', 'combobox', 'searchbox']);
    const axUnnamed = async sel => {
        await DOM.enable(); await Accessibility.enable();
        const { root } = await DOM.getDocument({ depth: 0 }); const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: sel }); if (!nodeId) return ['missing'];
        const { node } = await DOM.describeNode({ nodeId }); const { nodes } = await Accessibility.getFullAXTree();
        const byId = new Map(nodes.map(n => [n.nodeId, n])); const start = nodes.find(n => n.backendDOMNodeId === node.backendNodeId); if (!start) return ['not in the tree'];
        const out = []; const walk = n => { if (!n.ignored && INTERACTIVE.has(n.role && n.role.value) && !((n.name && n.name.value) || '').trim()) out.push(n.role.value); for (const c of n.childIds || []) { const k = byId.get(c); if (k) walk(k); } }; walk(start); return out;
    };
    const open = async (view) => {
        await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
        await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }] });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`); await ev(`localStorage.clear(); true`);
        await installAudit(ev); await setTheme(ev, view.theme);
        await ev(STUB);
    };
    const showAgent = async (session, tab) => {
        await ev(`(async () => { const S = (window.__coralStatic || '/static/'); const { state } = await import(S + 'state.js'); const files = await import(S + 'changed_files.js'); const { showView } = await import(S + 'utils.js');
            state.currentSession = Object.assign({ type: 'live', name: 'aa-agent', session_id: 'aa-1' }, ${JSON.stringify(session)}); showView('live-session-view');
            const side = document.getElementById('agentic-state'); side.classList.remove('collapsed'); side.style.width = window.innerWidth <= 767 ? '' : '680px'; files.initFileSearch(); files.syncFilesViewerSession(); return true; })()`);
        // a phone shows the side panel as a drawer the top bar's button opens (once: the button toggles it)
        await ev(`(() => { const side = document.getElementById('agentic-state'); if (window.innerWidth <= 767 && side.getBoundingClientRect().width === 0) (document.getElementById('popout-panel-toggle-btn') || document.getElementById('panel-toggle-btn')).click(); return true; })()`); await settle(500);
        await ev(`localStorage.removeItem('coral-agentic-tab-top'); window.switchAgenticTab('${tab}', 'top'); true`);
    };
    const panel = tab => `#agentic-panel-${tab}`;
    const settled = (tab) => waitFor(`document.getElementById('agentic-panel-${tab}-view').getAttribute('aria-busy') === 'false' && document.querySelectorAll('${panel(tab)} tr.team-artifact-row').length > 0`, `${tab}: the list`, 8000);
    const last = () => ev(`window.__req[window.__req.length - 1]`);

    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of [{ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' }]) {
            const tag = `${view.name} ${view.theme}`; console.log(tag);
            await open(view);
            // ═══ an agent on a team: the search bar is its own, scoped to the agent
            await showAgent({ board_project: TEAM }, 'artifacts'); await settled('artifacts');
            let r = await last();
            check(`${tag}: the agent's list is asked for with its session, the newest first, and the board's route (${JSON.stringify(r.query)})`, r.kind === 'board' && r.query.session_id === 'aa-1' && r.query.sort === 'newest' && r.query.offset === '0');
            const bar = await ev(`(() => { const p = document.querySelector('${panel('artifacts')}'); const q = s => p.querySelector(s); const vis = e => !!e && e.getClientRects().length > 0; return { search: vis(q('.list-bar-search')), filters: vis(q('.list-bar-toggle')), sort: !!q('select[aria-label="Sort artifacts"]'), count: (q('.list-bar-count') || {}).textContent, label: (q('.list-bar-search') || {}).getAttribute && q('.list-bar-search').getAttribute('aria-label') }; })()`);
            check(`${tag}: the agent's Artifacts tab has the search, the filter icon (Filters and Sort in its panel) and the count (${JSON.stringify(bar)})`, bar.search && bar.filters && bar.sort && /^7 artifacts?$/.test(bar.count || '') && /this agent/i.test(bar.label || ''));
            await ev(`document.querySelector('${panel('artifacts')} .list-bar-search').focus(); true`); await type('zebra'); await settle(900);
            r = await last();
            check(`${tag}: a search is the server's, scoped to the agent (${JSON.stringify(r.query)}), and the count says N of M`, r.query.q === 'zebra' && r.query.session_id === 'aa-1' && (await ev(`document.querySelector('${panel('artifacts')} .list-bar-count').textContent`)) === '1 of 7');
            await ev(`document.querySelector('${panel('artifacts')} select[aria-label="Sort artifacts"]').value = 'name'; document.querySelector('${panel('artifacts')} select[aria-label="Sort artifacts"]').dispatchEvent(new Event('change', { bubbles: true })); true`); await settle(500);
            check(`${tag}: Sort is sent (${JSON.stringify((await last()).query)})`, (await last()).query.sort === 'name' && (await last()).query.q === 'zebra');
            // the filters are kept per agent: another agent starts clean, the first one finds its search again
            await showAgent({ board_project: TEAM, session_id: 'aa-2', name: 'aa-other' }, 'artifacts'); await settle(900);
            check(`${tag}: another agent's tab starts with no search (${(await last()).query.q})`, !(await last()).query.q && (await last()).query.session_id === 'aa-2');
            await showAgent({ board_project: TEAM, session_id: 'aa-1' }, 'artifacts'); await settle(900);
            check(`${tag}: and the first agent's search is remembered (${(await last()).query.q})`, (await last()).query.q === 'zebra');
            await ev(`document.querySelector('${panel('artifacts')} .list-bar-search').value = ''; document.querySelector('${panel('artifacts')} .list-bar-search').dispatchEvent(new Event('input', { bubbles: true })); true`); await settle(900);
            await settled('artifacts');

            // ═══ Refresh is a reload icon, in both panels; Details is an (i) in the Actions column
            for (const tab of ['artifacts', 'team-artifacts']) {
                if (tab === 'team-artifacts') { await ev(`window.switchAgenticTab('team-artifacts', 'top'); true`); await settled('team-artifacts'); }
                const sec = `${tab}: `;
                const ref = await ev(`(() => { const b = document.querySelector('${panel(tab)} .team-artifacts-refresh'); if (!b) return null; const r = b.getBoundingClientRect(); return { label: b.getAttribute('aria-label'), title: b.title, text: b.textContent.trim(), svg: !!b.querySelector('svg'), w: Math.round(r.width), h: Math.round(r.height), tag: b.tagName, anyText: [...document.querySelectorAll('${panel(tab)}-view .team-artifacts-action')].some(x => x.textContent.trim() === 'Refresh') }; })()`);
                check(`${tag}: ${sec}Refresh is a reload icon button, named, with a tooltip, no text (${JSON.stringify(ref)})`, ref && ref.tag === 'BUTTON' && ref.label === 'Refresh' && ref.title === 'Refresh' && ref.text === '' && ref.svg && !ref.anyText && (!view.phone || (ref.w >= 44 && ref.h >= 44)));
                const before = await ev('window.__req.length');
                await ev(`document.querySelector('${panel(tab)} .team-artifacts-refresh').click(); true`); await settle(600);
                check(`${tag}: ${sec}it refreshes (${before} -> ${await ev('window.__req.length')} requests)`, (await ev('window.__req.length')) > before);
                const rows = await ev(`(() => { const p = document.querySelector('${panel(tab)}'); const row = p.querySelector('tr.team-artifact-row'); const b = row.querySelector('.team-artifact-info-btn'); const cell = row.querySelector('td.team-artifact-col-actions'); const r = b.getBoundingClientRect();
                    return { n: p.querySelectorAll('tr.team-artifact-row').length, infos: p.querySelectorAll('.team-artifact-info-btn').length, label: b.getAttribute('aria-label'), expanded: b.getAttribute('aria-expanded'), inActions: cell.contains(b), hiddenDetails: row.querySelector('.team-artifact-details').hidden, summaries: p.querySelectorAll('summary, details').length, disclosure: /▶|Details$/.test(row.querySelector('td.team-artifact-col-name, td.team-artifact-info').textContent.replace(/\\s+/g, ' ')), w: Math.round(r.width), h: Math.round(r.height) }; })()`);
                check(`${tag}: ${sec}every row has an (i) button in the Actions column, closed, and no disclosure under the name (${JSON.stringify(rows)})`, rows.infos === rows.n && /^Details for /.test(rows.label) && rows.expanded === 'false' && rows.inActions && rows.hiddenDetails && rows.summaries === 0 && !rows.disclosure && (!view.phone || (rows.w >= 44 && rows.h >= 44)));
                await ev(`document.querySelector('${panel(tab)} .team-artifact-info-btn').focus(); true`); await press('Enter', 'Enter', 13);
                let d = await ev(`(() => { const row = document.querySelector('${panel(tab)} tr.team-artifact-row'); const b = row.querySelector('.team-artifact-info-btn'); const det = row.querySelector('.team-artifact-details'); return { open: !det.hidden, expanded: b.getAttribute('aria-expanded'), controls: b.getAttribute('aria-controls') === det.id, mime: /MIME/.test(det.textContent), focus: document.activeElement === b }; })()`);
                check(`${tag}: ${sec}Enter on it opens the details (MIME, size, task ...), says it is expanded, and keeps focus (${JSON.stringify(d)})`, d.open && d.expanded === 'true' && d.controls && d.mime && d.focus);
                await ev(`document.querySelector('${panel(tab)} .team-artifacts-refresh').click(); true`); await settle(700);
                check(`${tag}: ${sec}a refresh keeps the row's details open`, await ev(`!document.querySelector('${panel(tab)} tr.team-artifact-row .team-artifact-details').hidden`));
                await ev(`document.querySelector('${panel(tab)} .team-artifact-info-btn').click(); true`);
                check(`${tag}: ${sec}pressed again it closes`, await ev(`document.querySelector('${panel(tab)} tr.team-artifact-row .team-artifact-details').hidden`));
                await ev(`document.querySelector('${panel(tab)} .team-artifact-info-btn').click(); true`); await settle(200);
                await shot(`${tag} ${tab} details open`);
                await ev(`document.querySelector('${panel(tab)} .team-artifact-info-btn').click(); true`);
                const un = await axUnnamed(`#agentic-panel-${tab}-view`);
                const au = await auditRegion(ev, [`#agentic-panel-${tab}-view`]);
                check(`${tag}: ${sec}every control has a name (${JSON.stringify(un)}) and the text has 4.5:1 contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, un.length === 0 && (au.fails || []).length === 0);
                await shot(`${tag} ${tab}`);
            }
            const dup = await ev(`(() => { const ids = [...document.querySelectorAll('[id]')].map(e => e.id); return ids.filter((id, i) => ids.indexOf(id) !== i); })()`);
            check(`${tag}: the two panels' bars share no id (${JSON.stringify(dup)})`, dup.length === 0);

            // ═══ an agent with no team: its list is the files it uploaded, no search (the standalone route has no query)
            await showAgent({ board_project: null, session_id: 'solo-1', name: 'solo' }, 'artifacts'); await settle(900);
            r = await last();
            const solo = await ev(`(() => { const p = document.querySelector('${panel('artifacts')}'); return { bar: !!p.querySelector('.list-bar-search') && p.querySelector('.list-bar-search').getClientRects().length > 0, rows: p.querySelectorAll('tr.team-artifact-row').length, info: p.querySelectorAll('.team-artifact-info-btn').length, refresh: !!p.querySelector('.team-artifacts-refresh') }; })()`);
            check(`${tag}: an agent with no team: the standalone route, rows with the (i) and the reload icon, no search bar (${JSON.stringify(solo)})`, r.kind === 'standalone' && !solo.bar && solo.rows === 3 && solo.info === 3 && solo.refresh);
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
