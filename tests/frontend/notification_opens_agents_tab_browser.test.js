// Opening an agent from a notification puts the Agents tab up first (#4953). From the Inbox (the operator's report), Chats, Servers,
// Workflows or Jobs, a needs-input toast's link and a desktop notification's click select the agent, and then: the Agents tab is
// the one highlighted, the agent list is drawn with that agent selected (desktop), and the agent's chat is shown (phone too).
// Run against an isolated dev server; set NOTIF_SHOT_DIR to save a screenshot after the Inbox toast.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');

const TABS = ['hub-inbox', 'history', 'servers', 'workflows', 'jobs'];

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const MOD = `(window.__coralStatic || '/static/')`;
    const open = async (width, height, mobile) => {
        await Emulation.setDeviceMetricsOverride({ width, height, deviceScaleFactor: 1, mobile });
        await Page.navigate({ url: process.env.CORAL_TEST_URL + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        await ev(`window.__m.add('alpha', 'A', 6, { sleeping: false }); window.__m.add('beta', 'B', 6, { sleeping: false }); window.__m.add('gamma', 'G', 6, { sleeping: false }); true`);
    };
    // The page's own feed message: the first one only records who is waiting; a later one raises the toast.
    const feed = (waiting) => ev(`(async () => {
        const ws = await import(${MOD} + 'websocket.js'); const { state } = await import(${MOD} + 'state.js');
        const rows = Object.values(window.__m.agents).map(a => ({ ...window.__m.entry(a), waiting_for_input: ${JSON.stringify(waiting)}.includes(a.name) }));
        state.settings = state.settings || {}; ws.handleCoralMessage({ type: 'coral_update', sessions: rows, servers: [] }); return true; })()`);
    const shot = async name => {
        if (!process.env.NOTIF_SHOT_DIR) return;
        await new Promise(r => setTimeout(r, 400)); // sleep-ok: let the list paint before a screenshot, only with NOTIF_SHOT_DIR set
        const { data } = await Page.captureScreenshot({ format: 'png' });
        require('node:fs').writeFileSync(`${process.env.NOTIF_SHOT_DIR}/${name}.png`, Buffer.from(data, 'base64'));
    };
    const arrived = async (name, what) => {
        await waitFor(`window.__qaState.currentSession && window.__qaState.currentSession.name === ${JSON.stringify(name)}`, `${what}: the agent to be selected`);
        const r = await ev(`(() => {
            const active = [...document.querySelectorAll('.top-nav-tab.active')].map(b => b.id);
            const live = document.querySelector('[data-section="live-sessions"]');
            const view = document.getElementById('live-session-view');
            const row = document.querySelector('#live-sessions-list .session-group-item.active');
            return { active, listShown: !!live && getComputedStyle(live).display !== 'none' && live.offsetParent !== null, viewShown: !!view && view.offsetParent !== null,
                selectedRow: row ? row.dataset.sessionId : null, inboxShown: !!document.getElementById('inbox-view')?.offsetParent };
        })()`);
        if (what.endsWith('from hub-inbox (toast)')) await shot(what.split(',')[0]);
        assert.deepEqual(r.active, ['nav-tab-agents'], `${what}: only the Agents tab is highlighted ${JSON.stringify(r)}`);
        assert.ok(r.viewShown, `${what}: the agent's view is shown`);
        assert.equal(r.inboxShown, false, `${what}: the Inbox is not showing under it`);
        return r;
    };
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile, label] of [[1280, 800, false, 'desktop'], [390, 844, true, 'phone']]) {
            await open(w, h, mobile);
            // A desktop alert is raised only for a hidden page that opted in.
            await ev(`(() => { window.__notes = []; function N(t, o) { window.__notes.push(this); this.close = () => {}; } N.permission = 'granted'; window.Notification = N;
                localStorage.setItem('coral-desktop-notify', '1'); Object.defineProperty(document, 'hidden', { configurable: true, get: () => !!window.__hidden }); return true; })()`);
            await ev(`import(${MOD} + 'websocket.js').then(m => { m._resetWaitingSeedForTests(); return true; })`);
            await feed([]);
            let n = 0;
            for (const tab of TABS) {
                const what = `${label}, from ${tab}`;
                // 1. The toast's "Switch to session" link, for beta.
                await ev(`document.getElementById('nav-tab-${tab}') ? (window.switchNavTab('${tab}'), true) : false`);
                assert.ok(await ev(`document.getElementById('nav-tab-${tab}').classList.contains('active')`), `${what}: that tab is the active one`);
                await ev(`window.__hidden = false; document.querySelectorAll('.notification-toast').forEach(t => t.remove()); true`);
                await feed(['beta']);
                await waitFor(`!!document.querySelector('.notification-toast-link')`, `${what}: the needs-input toast`);
                await ev(`document.querySelector('.notification-toast-link').click(); true`);
                const viaToast = await arrived('beta', `${what} (toast)`);
                if (!mobile) assert.ok(viaToast.listShown && viaToast.selectedRow === 'beta-id', `${what} (toast): the agent list is drawn with beta selected ${JSON.stringify(viaToast)}`);
                await feed([]);

                // 2. A desktop notification's click, for gamma, while the page is hidden.
                await ev(`window.switchNavTab('${tab}'); window.__hidden = true; window.__notes.length = 0; true`);
                await feed(['gamma']);
                await waitFor(`window.__notes.length === 1`, `${what}: the desktop alert`);
                await ev(`window.__hidden = false; window.__notes[0].onclick(); true`);
                const viaAlert = await arrived('gamma', `${what} (desktop alert)`);
                if (!mobile) assert.ok(viaAlert.listShown && viaAlert.selectedRow === 'gamma-id', `${what} (alert): the agent list is drawn with gamma selected ${JSON.stringify(viaAlert)}`);
                await feed([]);
                // Each pass sees the agent as newly waiting; the per-agent alert gap is the page's own, so reset it.
                await ev(`import(${MOD} + 'desktop_notify.js').then(m => { m._resetRateForTests(); return true; })`);
                n++;
            }
            assert.equal(n, TABS.length, `${label}: every starting tab was tried`);
        }
        console.log('PASS notifications open the Agents tab: toast and desktop alert, from Inbox, Chats, Servers, Workflows and Jobs, desktop and phone');
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
