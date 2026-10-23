// The dashboard's own Inbox is tabbed like Coral Hub's (#5126): a header, then the tabs "Needs you" | "Tasks for you" | "Saved for later", one panel on screen at a time.
//   - roles: a tablist named Inbox, two tabs with the hub's names, each controlling a tabpanel it labels; Needs you is the default;
//   - only the chosen section shows (the other is out of the layout, not just scrolled away), and the Saved scope heading stays inside the Saved tab;
//   - arrow keys, Home and End move between the tabs (roving tabindex, the focus follows); a click does the same;
//   - the chosen tab is remembered across a reload (this browser, localStorage);
//   - the live announcement (#4926) still speaks while the Saved tab is the one shown;
//   - 44 px tall tabs on a phone, readable text in both themes, no sideways scroll.
// Real isolated server, two mock agents (one waiting), one saved message. Drives Chrome over CDP through agent_view_harness.js.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const api = (method, path, body) => ev(`fetch(${JSON.stringify(path)}, { method: ${JSON.stringify(method)}, headers: { 'Content-Type': 'application/json' }, body: ${body ? JSON.stringify(JSON.stringify(body)) : 'undefined'} }).then(async r => ({ status: r.status, body: await r.json().catch(() => ({})) }))`);
    const key = async (k, code) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code: k, windowsVirtualKeyCode: code }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code: k, windowsVirtualKeyCode: code }); };
    const shown = id => ev(`(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`);
    const look = () => ev(`(() => { const t = n => document.getElementById('inbox-tab-' + n), p = n => document.getElementById('inbox-panel-' + n);
        return { sel: ['needs', 'tasks', 'saved'].filter(n => t(n).getAttribute('aria-selected') === 'true'), tabindex: ['needs', 'tasks', 'saved'].map(n => t(n).tabIndex), focus: document.activeElement && document.activeElement.id,
            panels: ['needs', 'tasks', 'saved'].map(n => p(n).getClientRects().length > 0), list: document.getElementById('inbox-needs-list').getClientRects().length > 0, savedView: document.getElementById('saved-view').getClientRects().length > 0,
            stored: localStorage.getItem('coral.inbox.tab'), over: document.documentElement.scrollWidth - document.documentElement.clientWidth }; })()`);
    const theme = name => ev(`(async () => { const m = await import((window.__coralStatic || '/static/') + 'settings_core.js'); m.clearCustomThemeVars && m.clearCustomThemeVars(); m.applyTheme(${JSON.stringify(name)}); return true; })()`);

    const open = async (w, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width: w, height: 900, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`(() => { const entry = window.__m.entry.bind(window.__m); window.__m.entry = a => ({ ...entry(a), waiting_for_input: a.session_id === 'tb-w', waiting_kind: a.session_id === 'tb-w' ? 'permission' : null });
            window.__m.add('Tab Waiting', 'A', 4, { sleeping: false, session_id: 'tb-w' }); window.__m.add('Tab Idle', 'B', 4, { sleeping: false, session_id: 'tb-i' }); return true; })()`);
        await waitFor(`(window.__qaState && window.__qaState.liveSessions || []).length === 2`, 'two agents', 8000);
        await ev(`window.__qaState.liveSessions = window.__qaState.liveSessions.map(s => ({ ...s, ...window.__m.entry(window.__m.agents[s.session_id]) })); import((window.__coralStatic || '/static/') + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
        await ev(`document.getElementById('nav-tab-hub-inbox') && (document.getElementById('nav-tab-hub-inbox').click(), true)`);
        await waitFor(`!!document.getElementById('inbox-local') && !document.getElementById('inbox-local').hidden`, 'the Inbox', 8000);
    };
    const pick = async n => { await ev(`document.getElementById('inbox-tab-${n}').click(); true`); };

    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        for (const it of (await api('GET', '/api/saved')).body.items) await api('DELETE', `/api/saved/${it.id}`);
        const r = await api('POST', '/api/saved', { kind: 'agent', ref: 'tb-i', key: '0'.repeat(19) + '1', title: 'Tab Idle', snippet: 'a message kept for later', agent_type: 'claude', said_at: '2026-10-17T08:00:00Z' });
        assert.equal(r.status, 200, JSON.stringify(r));
        await ev(`localStorage.removeItem('coral.inbox.tab'); true`);

        for (const [dev, w, mobile] of [['desktop', 1280, false], ['phone', 390, true]]) {
            console.log(dev);
            await ev(`localStorage.removeItem('coral.inbox.tab'); true`); // a fresh browser: the Inbox opens on Needs you
            await open(w, mobile);
            await waitFor(`document.getElementById('inbox-needs-list').getClientRects().length > 0`, 'Needs you', 8000);
            // roles and names
            const roles = await ev(`(() => { const l = document.getElementById('inbox-tablist'); const tabs = [...l.querySelectorAll('[role=tab]')]; const panel = id => document.getElementById(id);
                return { list: l.getAttribute('role'), name: l.getAttribute('aria-label'), tabs: tabs.map(t => t.textContent.trim()), panels: tabs.map(t => { const p = panel(t.getAttribute('aria-controls')); return !!p && p.getAttribute('role') === 'tabpanel' && p.getAttribute('aria-labelledby') === t.id; }),
                    title: document.getElementById('inbox-title').textContent.trim(), sub: !!document.querySelector('.inbox-sub') }; })()`);
            check(`${dev}: a tablist named Inbox with the hub's tab names (and Tasks for you, #5754), each controlling the tabpanel it labels`, roles.list === 'tablist' && roles.name === 'Inbox' && roles.tabs.join('|') === 'Needs you|Tasks for you|Saved for later' && roles.panels.every(Boolean), JSON.stringify(roles));
            check(`${dev}: the page has one Inbox heading without a repeated overview`, roles.title === 'Inbox' && !roles.sub, JSON.stringify([roles.title, roles.sub]));
            let s = await look();
            check(`${dev}: it opens on Needs you, and only that section is on screen`, s.sel.join() === 'needs' && s.panels.join() === 'true,false,false' && s.list && !s.savedView && !(await shown('saved-scope')), JSON.stringify(s));
            check(`${dev}: Needs you lists the waiting agent`, (await ev(`document.querySelectorAll('#inbox-needs-list .inbox-needs-item').length`)) === 1);

            // keyboard
            await ev(`document.getElementById('inbox-tab-needs').focus(); true`);
            await key('ArrowRight', 39);
            await waitFor(`document.getElementById('inbox-panel-tasks').getClientRects().length > 0`, 'Tasks for you', 8000);
            s = await look();
            check(`${dev}: ArrowRight moves to Tasks for you (focus follows, roving tabindex), and only that section is on screen`, s.sel.join() === 'tasks' && s.focus === 'inbox-tab-tasks' && s.tabindex.join() === '-1,0,-1' && s.panels.join() === 'false,true,false' && !s.list, JSON.stringify(s));
            await key('ArrowRight', 39);
            await waitFor(`document.getElementById('inbox-panel-saved').getClientRects().length > 0 && document.querySelectorAll('#saved-list .saved-item').length === 1`, 'Saved for later', 8000);
            s = await look();
            check(`${dev}: ArrowRight moves on to Saved for later, and only that section is on screen`, s.sel.join() === 'saved' && s.focus === 'inbox-tab-saved' && s.tabindex.join() === '-1,-1,0' && s.panels.join() === 'false,false,true' && !s.list && s.savedView, JSON.stringify(s));
            check(`${dev}: the Saved scope heading is inside the Saved tab`, (await ev(`document.getElementById('inbox-panel-saved').contains(document.getElementById('saved-scope')) && document.getElementById('saved-scope').textContent`)) === 'All saved');
            // every panel has been opened by now (the Tasks panel's section is only mounted on its first visit)
            const dup = await ev(`['needs', 'tasks', 'saved'].flatMap(n => { const panel = document.getElementById('inbox-panel-' + n), name = (document.getElementById(panel.getAttribute('aria-labelledby')) || {}).textContent.trim().toLowerCase();
                return [...panel.querySelectorAll('[role="region"], section[aria-label], section[aria-labelledby], form[aria-label]')].filter(r => (r.getAttribute('aria-label') || '').trim().toLowerCase() === name).map(r => n + ': ' + r.id); })`);
            check(`${dev}: a tab panel holds no region named like the panel (a screen reader lists "Needs you" twice, the panel and the section inside it, #6570)`, dup.length === 0, JSON.stringify(dup));
            await key('ArrowRight', 39); s = await look();
            check(`${dev}: ArrowRight wraps to Needs you`, s.sel.join() === 'needs' && s.focus === 'inbox-tab-needs' && s.panels.join() === 'true,false,false', JSON.stringify(s));
            await key('End', 35); s = await look();
            check(`${dev}: End goes to the last tab`, s.sel.join() === 'saved' && s.focus === 'inbox-tab-saved', JSON.stringify(s));
            await key('Home', 36); s = await look();
            check(`${dev}: Home goes to the first`, s.sel.join() === 'needs' && s.focus === 'inbox-tab-needs', JSON.stringify(s));
            await key('ArrowLeft', 37); s = await look();
            check(`${dev}: ArrowLeft goes back to Saved for later`, s.sel.join() === 'saved', JSON.stringify(s));

            // persistence across a reload
            await pick('saved'); s = await look();
            check(`${dev}: the choice is kept (Saved for later)`, s.stored === 'saved', JSON.stringify(s));
            await open(w, mobile);
            await waitFor(`document.getElementById('inbox-panel-saved').getClientRects().length > 0 && document.querySelectorAll('#saved-list .saved-item').length === 1`, 'Saved after a reload', 8000);
            s = await look();
            check(`${dev}: after a reload the Inbox opens on Saved for later, its list loaded, Needs you not on screen`, s.sel.join() === 'saved' && s.savedView && !s.list && s.tabindex.join() === '-1,-1,0', JSON.stringify(s));
            await pick('needs');
            await open(w, mobile);
            await waitFor(`document.getElementById('inbox-needs-list').getClientRects().length > 0`, 'Needs after a reload', 8000);
            s = await look();
            check(`${dev}: and back on Needs you after a reload when that was the last choice`, s.sel.join() === 'needs' && s.list && !s.savedView && s.over <= 0, JSON.stringify(s));

            // the live announcement while the Saved tab is shown
            await pick('saved');
            await ev(`(() => { const m = window.__m; m.add('Tab Late', 'C', 4, { sleeping: false, session_id: 'tb-l' }); return true; })()`);
            await waitFor(`(window.__qaState.liveSessions || []).some(x => x.session_id === 'tb-l')`, 'the late agent', 8000);
            await ev(`window.__qaState.liveSessions = window.__qaState.liveSessions.map(x => x.session_id === 'tb-l' ? { ...x, waiting_for_input: true, waiting_kind: 'permission' } : x); import((window.__coralStatic || '/static/') + 'inbox_tab.js').then(m => m.updateInboxTab()); true`);
            check(`${dev}: a newly waiting agent is announced in the live region while the Saved tab is shown`, await waitFor(`/needs you/i.test(document.getElementById('inbox-live').textContent)`, 'the announcement', 4000).then(() => true, () => false), await ev(`document.getElementById('inbox-live').textContent`));
            check(`${dev}: the live region is outside both panels`, await ev(`!document.getElementById('inbox-panel-needs').contains(document.getElementById('inbox-live')) && !document.getElementById('inbox-panel-tasks').contains(document.getElementById('inbox-live')) && !document.getElementById('inbox-panel-saved').contains(document.getElementById('inbox-live'))`));

            // size and legibility
            const box = await ev(`['needs', 'tasks', 'saved'].map(n => { const r = document.getElementById('inbox-tab-' + n).getBoundingClientRect(); return [Math.round(r.width), Math.round(r.height)]; })`);
            check(`${dev}: each tab is at least 44 px tall`, box.every(b => b[1] >= 44), JSON.stringify(box));
            for (const th of ['dark', 'light']) {
                await theme(th); await new Promise(r => setTimeout(r, 350)); // sleep-ok: the theme's colour transitions
                const c = await ev(`(() => { const px = c => { const sr = /color\\(srgb ([^)]+)\\)/.exec(c); const m = sr || /rgba?\\(([^)]+)\\)/.exec(c); const p = m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number); const k = sr ? 255 : 1; return { r: p[0] * k, g: p[1] * k, b: p[2] * k, a: p.length > 3 ? p[3] : 1 }; };
                    const bgOf = el => { for (let e = el; e; e = e.parentElement) { const c = px(getComputedStyle(e).backgroundColor); if (c.a > 0.99) return c; } return { r: 255, g: 255, b: 255, a: 1 }; };
                    const f = v => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; const lum = c => 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b);
                    const ratio = (a, b) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05); };
                    return ['needs', 'tasks', 'saved'].map(n => { const t = document.getElementById('inbox-tab-' + n); return { n, sel: t.getAttribute('aria-selected'), ratio: ratio(px(getComputedStyle(t).color), bgOf(t)) }; }); })()`);
                check(`${dev}, ${th}: all three tab labels read at 4.5:1 (the chosen one and the other)`, c.every(x => x.ratio >= 4.5), JSON.stringify(c));
                await shot(`inbox-tabs-${dev}-${th}`);
            }
            await theme('dark');
        }
    } catch (e) {
        console.error(e && e.stack || e);
        results.push(false);
    } finally {
        await require('./helpers/saved_cleanup').clearSaved(api).catch(() => {});
        try { await client.close(); } catch (_) {}
    }
    const failed = results.filter(r => !r).length;
    console.log(failed ? `${failed} FAILED` : `PASS inbox tabs: ${results.length} checks`);
    process.exitCode = failed ? 1 : 0;
})();
