// QA (#5510): the allowlist editor with hostile and extreme data from the server: labels, ids and log fields that carry markup are text and never run; a list at the cap cannot be added to; a
// list is operable by keyboard alone; Save sends exactly the list shown. The routes are stubbed on top of the real page code (the author's suite covers the real server end to end).
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;
const XID = n => 'x-' + n.repeat(16);
const HOSTILE = '<img src=x onerror="window.__pwn=1">';
(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const S = `(window.__coralStatic || '/static/')`;
    const TEAM = 'qa-ct-' + Date.now().toString(36);
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' \u2014 ' + detail : ''}`); };
    const P = '#team-settings-messaging';
    const call = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))} }).then(async r => ({ status: r.status, body: await r.json().catch(() => null) }))`);
    const board = (name, path = '', method = 'GET', body) => call(method, `/api/board/${name}${path}`, body);
    const layer = () => ev(`(() => { if (!window.__realFetch) window.__realFetch = window.fetch; const R = window.__realFetch; window.__ct = window.__ct || { state: null, cand: null, log: null, puts: [] };
        const json = o => Promise.resolve(new Response(JSON.stringify(o), { status: 200, headers: { 'content-type': 'application/json' } }));
        window.fetch = (u, o = {}) => { const p = new URL(String(u && u.url || u), location.origin).pathname; const m = (o.method || 'GET').toUpperCase();
            if (p === '/api/board/${TEAM}/cross-team-state' && m === 'GET' && window.__ct.stateStatus) return Promise.resolve(new Response(JSON.stringify(window.__ct.stateBody || {}), { status: window.__ct.stateStatus, headers: { 'content-type': 'application/json' } }));
            if (p === '/api/board/${TEAM}/cross-team-state' && m === 'GET' && window.__ct.state) return json(window.__ct.state);
            if (p === '/api/board/${TEAM}/cross-team-candidates' && m === 'GET' && window.__ct.cand) return json(window.__ct.cand);
            if (p === '/api/board/${TEAM}/settings/log' && m === 'GET' && window.__ct.log) return json(window.__ct.log);
            if (p === '/api/board/${TEAM}/settings' && m === 'PUT' && JSON.parse(o.body).cross_team_allowed) { window.__ct.puts.push(JSON.parse(o.body)); return json({}); }
            return R(u, o); }; return true; })()`);
    const open = async () => {
        await ev(`document.getElementById('team-settings-workspace')?.remove(); document.getElementById('team-working-mode-dialog')?.remove(); import(${S} + 'team_working_mode.js').then(m => m.showTeamWorkingMode(${JSON.stringify(TEAM)}, { workspace: false })); true`);
        await waitFor(`!!document.querySelector('[data-settings-tab=health]')`, 'the settings dialog', 15000);
    };
    const tabClick = () => waitFor(`(() => { const t = document.querySelector('[data-settings-tab=messaging]'); if (!t) return false; t.click(); const p = document.querySelector('${P}'); return !!p && !p.hidden && !!p.querySelector('form .ct-section'); })()`, 'the Messaging tab', 10000);
    const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
    const names = () => ev(`[...document.querySelectorAll('${P} .ct-row')].map(r => r.querySelector('.ct-name').textContent)`);
    const status = () => ev(`document.querySelector('${P} .ct-state').textContent`);
    const active = () => ev(`(() => { const a = document.activeElement; return a ? (a.getAttribute('aria-label') || a.dataset.act || a.name || a.tagName) : ''; })()`);
    const setVal = (name, v) => ev(`(() => { const e = document.querySelector('${P} [name=${name}]'); e.value = ${JSON.stringify(v)}; e.dispatchEvent(new Event('input', { bubbles: true })); return true; })()`);
    const pickFor = label => ev(`(() => { const o = [...document.querySelectorAll('${P} [name=pick] option')].find(x => x.textContent === ${JSON.stringify(label)}); if (!o) return false; document.querySelector('${P} [name=pick]').value = o.value; return true; })()`);
    const guard = await guardSettings();
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await board(TEAM, '/subscribe', 'POST', { subscriber_id: 'Boss', job_title: 'Orchestrator', session_name: TEAM + '-session' });
        await board(TEAM, '/settings', 'PUT', { subscriber_id: 'Operator', cross_team_messaging: true });
        await call('PUT', '/api/settings', { cross_team_allowlist: 'true' });
        await layer();

        console.log('1. hostile data is text');
        await ev(`window.__ct.state = { enforced: true, max_entries: 200, self_cross_team_id: '${XID('s')}', was_open: true,
            allowed: [{ server: 'local', cross_team_id: '${XID('a')}', label: ${JSON.stringify(HOSTILE + ' listed')}, known_here: false }, { server: 'hub-1', cross_team_id: '${XID('b')}', label: '', known_here: false }],
            suggestions: [{ server: 'hub-2', cross_team_id: '${XID('c')}', label: ${JSON.stringify('<b>bold</b>' + HOSTILE)}, label_is_a_claim: true, last_seen: '2026-10-20T10:00:00Z', count: 3, listable: true },
                          { server: 'local', cross_team_id: '', label: ${JSON.stringify(HOSTILE)}, label_is_a_claim: false, last_seen: 'not a date', count: 'many', listable: false }] };
            window.__ct.cand = { candidates: [{ cross_team_id: '${XID('d')}', label: ${JSON.stringify(HOSTILE + ' board')}, created_at: '2026-10-20T10:00:00Z' }] };
            window.__ct.log = { entries: [{ id: 1, at: '2026-10-20T10:00:00Z', kind: 'edit', actor: ${JSON.stringify(HOSTILE)}, added: [{ server: ${JSON.stringify('x"><img src=x onerror=window.__pwn=1>')}, cross_team_id: ${JSON.stringify(HOSTILE)}, label: ${JSON.stringify(HOSTILE)} }], removed: [] },
                { id: 2, at: 'bad', kind: 'refused', count: 'NaN', senders: [{ server: ${JSON.stringify(HOSTILE)}, cross_team_id: ${JSON.stringify(HOSTILE)} }], others: 7 }, { id: 3, at: '2026-10-20T10:00:00Z', kind: ${JSON.stringify(HOSTILE)} }] }; true`);
        await open(); await tabClick();
        const r = await ev(`(() => ({ pwn: !!window.__pwn, imgs: document.querySelectorAll('${P} img').length, onerr: document.querySelectorAll('${P} [onerror]').length, bold: document.querySelectorAll('${P} b').length, text: document.querySelector('${P}').textContent }))()`);
        check('no markup from a label, id, actor or kind ever becomes an element or runs', !r.pwn && r.imgs === 0 && r.onerr === 0, JSON.stringify({ pwn: r.pwn, imgs: r.imgs, onerr: r.onerr }));
        check('the hostile text is shown as text', r.text.includes(HOSTILE) , '');
        check('a suggestion with no id says it cannot be added yet and is disabled', await ev(`(() => { const boxes = [...document.querySelectorAll('${P} input[name=suggest]')]; return boxes.length === 2 && boxes.some(b => b.disabled) && /cannot be added yet/.test(document.querySelector('${P} .ct-suggest').textContent); })()`));
        check('a bad date or count does not break the page (the log still draws)', await ev(`document.querySelectorAll('${P} .ct-log li').length === 3`));

        console.log('2. the list at the cap');
        await ev(`(() => { const all = []; for (let i = 0; i < 200; i++) { all.push({ server: 'hub-1', cross_team_id: 'x-' + 'abcdefghijklmnopqrstuvwxyz234567'[Math.floor(i / 32)] + 'abcdefghijklmnopqrstuvwxyz234567'[i % 32] + 'aaaaaaaaaaaaaa', label: 'team ' + i, known_here: false }); }
            const seen = new Set(); window.__ct.state = { enforced: true, max_entries: 200, self_cross_team_id: '${XID('s')}', was_open: false, allowed: all.filter(e => !seen.has(e.cross_team_id) && seen.add(e.cross_team_id)), suggestions: [] };
            window.__ct.cand = { candidates: [{ cross_team_id: '${XID('d')}', label: 'one more', created_at: '2026-10-20T10:00:00Z' }] }; window.__ct.log = { entries: [] }; return seen.size; })()`);
        await open(); await tabClick();
        const cap = await ev(`(() => ({ rows: document.querySelectorAll('${P} .ct-row').length, count: document.querySelector('${P} .ct-count').textContent, pickDisabled: document.querySelector('${P} [name=pick]').disabled, addDisabled: document.querySelector('${P} [data-act=add-pick]').disabled, otherDisabled: document.querySelector('${P} [data-act=add-other]').disabled }))()`);
        check(`at the cap the picker and both Add buttons are disabled and the count says so (${JSON.stringify(cap)})`, cap.pickDisabled && cap.addDisabled && cap.otherDisabled && /list is full/.test(cap.count));

        console.log('3. keyboard only');
        await ev(`window.__ct.state = { enforced: true, max_entries: 200, self_cross_team_id: '${XID('s')}', was_open: false, allowed: [{ server: 'local', cross_team_id: '${XID('a')}', label: 'Alpha', known_here: true }], suggestions: [] };
            window.__ct.cand = { candidates: [{ cross_team_id: '${XID('d')}', label: 'Delta', created_at: '2026-10-20T10:00:00Z' }] }; window.__ct.log = { entries: [] }; window.__ct.puts = []; true`);
        await open(); await tabClick();
        const flow = await ev(`(async () => {
            const q = s => document.querySelector('${P} ' + s);
            const sel = q('[name=pick]'); sel.focus(); sel.value = sel.options[1].value; sel.dispatchEvent(new Event('change', { bubbles: true }));
            const add = q('[data-act=add-pick]'); add.focus(); const ok1 = document.activeElement === add; add.click();
            await new Promise(r => setTimeout(r, 50));
            const rows = [...document.querySelectorAll('${P} .ct-row')].map(r => r.querySelector('.ct-name').textContent);
            const rm = q('.ct-remove'); const names = [...document.querySelectorAll('${P} .ct-remove')].map(b => b.getAttribute('aria-label'));
            const form = q('form'); form.requestSubmit();
            for (let i = 0; i < 100 && !window.__ct.puts.length; i++) await new Promise(r => setTimeout(r, 20));
            return { ok1, rows, names, puts: window.__ct.puts };
        })()`);
        check(`pick then Add by keyboard adds the team, every Remove has its own name, and Save sends exactly the list shown (${JSON.stringify(flow.rows)})`, flow.ok1 && JSON.stringify(flow.rows) === JSON.stringify(['Alpha', 'Delta']) && flow.names.every(n => /^Remove .+ from the list$/.test(n)) && flow.puts.length === 1 && flow.puts[0].cross_team_allowed.length === 2 && flow.puts[0].cross_team_allowed.every(e => e.server === 'local' && /^x-[a-z2-7]{16}$/.test(e.cross_team_id)));


        console.log('5. the tab while the list is not in force (#5516)');
        const base = `{ max_entries: 200, self_cross_team_id: '${XID('s')}', suggestions: [{ server: 'local', cross_team_id: '${XID('c')}', label: 'Gamma', label_is_a_claim: false, last_seen: '2026-10-20T10:00:00Z', count: 2, listable: true }], allowed: [] }`;
        for (const [what, patch, expectTab] of [['the state route is local-only (403, as through Coral Hub)', `window.__ct.stateStatus = 403; window.__ct.stateBody = { code: 'local_only' }; window.__ct.state = null;`, false],
            ['an older server has no such route (404)', `window.__ct.stateStatus = 404; window.__ct.stateBody = { error: 'not found' }; window.__ct.state = null;`, false],
            ['an answer without a list is no state', `window.__ct.stateStatus = 0; window.__ct.state = { enforced: false };`, false]]) {
            await ev(`${patch} true`);
            await open();
            await waitFor(`!!document.querySelector('[data-settings-tab=messaging]') && !!document.querySelector('${P} .ct-unavailable')`, `the Messaging panel says it is not available when ${what}`, 8000);
            check(`when ${what}, the tab is still drawn (with the strip, #5518) and its panel says it is not available, with no editor`, await ev(`!!document.querySelector('[data-settings-tab=messaging]') && /not available here/.test(document.querySelector('${P} .ct-unavailable').textContent) && !document.querySelector('${P} .ct-form')`));
        }
        await ev(`window.__ct.stateStatus = 0; window.__ct.state = Object.assign({ enforced: false, was_open: true }, ${base}); window.__ct.cand = { candidates: [] }; window.__ct.log = { entries: [] }; true`);
        await open(); await tabClick();
        const off = await ev(`(() => ({ notice: !!document.querySelector('${P} .ct-notice'), text: (document.querySelector('${P} .ct-notice') || {}).textContent || '', prompt: !!document.querySelector('${P} .ct-prompt'), tabs: [...document.querySelectorAll('[data-settings-tab]')].map(t => t.dataset.settingsTab), role: (document.querySelector('${P} .ct-notice') || document.body).getAttribute('role') }))()`);
        check(`not enforced: the line says it is not in force, there is no one-time prompt even for a board that was open, and the tab is last (${JSON.stringify(off.tabs)})`, off.notice && /not in force yet/.test(off.text) && !off.prompt && off.tabs[off.tabs.length - 1] === 'messaging' && off.role === 'status');
        await ev(`window.__ct.state = Object.assign({ enforced: true, was_open: true }, ${base}); true`);
        await open(); await tabClick();
        const on = await ev(`(() => ({ notice: !!document.querySelector('${P} .ct-notice'), prompt: !!document.querySelector('${P} .ct-prompt') }))()`);
        check('enforced: no "not in force" line, and the prompt is back for a board that was open', !on.notice && on.prompt);
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed}/${results.length} checks passed`);
        process.exitCode = failed ? 1 : 0;
    } finally { try { await call('PUT', '/api/settings', { cross_team_allowlist: 'false' }); } catch (_) {} try { await guard.restore(); } catch (_) {} try { await removeBoards(ev, [TEAM]); } catch (_) {} await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
