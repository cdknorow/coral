// The auto-update UI (#5275, design #5270), against a stub of the status API (Security's section 13 of #5231): behind its flag (off: nothing shown, nothing fetched), the "Relaunch to update" control in the top
// bar in its three ages (colour is never the only signal, a phone says the days too), the confirmation that lists the pty agents a relaunch restarts (safe answer focused, a long list scrolls, a stale ack shows the
// new list, a refusal says why), Settings > Updates in every state (ready, up to date, downloading, failed, Homebrew, unsupported), the two switches saved with Apply, and the failure notice (dismissed per version).
// Desktop and 390 px, both themes, reduced motion on; keyboard and the accessibility tree; 4.5:1 text contrast. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { connect } = require('./agent_view_harness');
const { setTheme, installAudit, auditRegion } = require('./helpers/contrast');
const { guardSettings } = require('./helpers/settings_guard');
const BASE = require('./test_base').BASE;

const agents = (n, extra = {}) => Array.from({ length: n }, (_, i) => ({ session_id: `sid-${i}`, name: ['api-worker', 'review-1', 'docs-2'][i] || `agent-${i}`, backend: 'pty', state: ['working', 'waiting', 'idle'][i % 3], ...(i === 0 ? { resumes: true } : i === 2 ? { resumes: false } : {}), ...extra }));
const staged = (days, restarts = agents(3), over = {}) => ({ enabled: true, installed: '1.4.125', state: 'staged', staged: { version: '1.4.126', staged_at: '2026-10-17T03:00:00Z', days_ready: days, verified: true }, relaunch: { available: true, reason: '', hub_allowed: true, restarts, tmux_agents_restart: false, ack: 'ack-1' }, last_result: null, checked_at: new Date().toISOString(), auto: true, ...over });
const idle = { enabled: true, installed: '1.4.125', state: 'none', staged: null, relaunch: { available: false, reason: 'nothing_staged', restarts: [], ack: '' }, last_result: null, checked_at: new Date().toISOString(), auto: true };
const upToDate = { ...idle, not_ready_reason: 'up_to_date' }; // what the server says when it knows (#5388); `idle` says nothing, and the pane must not claim more than that

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input, Accessibility, DOM } = client;
    const S = `(window.__coralStatic || '/static/')`;
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
    const settle = (ms, quietMs = 120) => require('./helpers/wait').settle(ev, ms, quietMs); // the page's geometry still for quietMs, or ms at most (helpers/wait.js); not a fixed sleep
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(path.join(d, name.replace(/[^a-z0-9-]+/gi, '-') + '.png'), Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    const press = async (key, code, vk) => { const text = key === 'Enter' ? '\r' : undefined; await Input.dispatchKeyEvent({ type: text ? 'keyDown' : 'rawKeyDown', key, code, windowsVirtualKeyCode: vk, text }); await Input.dispatchKeyEvent({ type: 'keyUp', key, code, windowsVirtualKeyCode: vk }); await settle(80); };
    const focused = () => ev(`(() => { const a = document.activeElement; return a ? { id: a.id || '', cls: String(a.className || '').split(' ').slice(0, 3).join('.'), text: (a.getAttribute('aria-label') || a.textContent || '').trim().slice(0, 40) } : null; })()`);

    // the stub: the status, and what each route answers; it records every request to /api/system/update
    const STUB = `(() => {
        const real = window.fetch;
        window.__upd = window.__upd || { hub: true, status: null, calls: [], relaunch: { status: 202, body: { ok: true, operation: 'op-1' } }, checkOk: true };
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            if (url.includes('/api/cloud/status')) return json({ connected: false, hub_relaunch_updates: window.__upd.hub }); // the connector's status: the saved switch is here, not in the update status
            if (url.includes('/api/cloud/hub-relaunch')) { window.__upd.calls.push({ method, url: '/hub-relaunch', body: o && o.body ? JSON.parse(o.body) : null }); return json({ ok: true }); }
            if (url.includes('/api/system/update')) {
                const u2 = window.__upd; u2.calls.push({ method, url: url.replace(/^.*\\/api\\/system\\/update/, '') || '/status', body: o && o.body ? JSON.parse(o.body) : null });
                if (url.endsWith('/api/system/update')) return u2.status ? json(u2.status) : json({ error: 'none' }, 404);
                if (url.endsWith('/relaunch-local')) { const keys = Object.keys(u2.calls[u2.calls.length - 1].body || {}); if (method !== 'POST' || keys.join() !== 'ack') return json({ error: 'bad body' }, 400); /* the server's body is exactly {ack} */ return json(u2.relaunch.body, u2.relaunch.status); }
                if (url.endsWith('/check')) return json({ ok: true }, u2.checkOk ? 200 : 500);
                if (url.endsWith('/settings')) return json({ ok: true });
            }
            return real(u, o);
        };
        return true; })()`;
    const load = async (view, { flag = true, status = null, routes = true } = {}) => {
        await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: view.phone });
        await Emulation.setTouchEmulationEnabled({ enabled: view.phone });
        await Emulation.setEmulatedMedia({ features: [{ name: 'prefers-color-scheme', value: view.theme }, { name: 'prefers-reduced-motion', value: 'reduce' }] });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired();
        await ev(`localStorage.clear(); true`);
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.then(() => true)`);
        if (flag) await ev(`import((window.__coralStatic || '/static/') + 'update_flag.js').then(m => { m.setUpdatesEnabled(true); return true; })`); // the server's `enabled` is what turns the UI on; the suite stands for a server that says so
        await installAudit(ev); await setTheme(ev, view.theme);
        await ev(STUB);
        // Check now and the auto switch exist only once the server has routes for them (QA, #5302): on here so their code stays covered; routes:false is the server as it is today
        await ev(`import(${S} + 'update_api.js').then(m => { m.serverHas.check = ${!!routes}; m.serverHas.autoPref = ${!!routes}; return true; })`); // the server has both routes now; routes:false is a server without them
        if (status) await setStatus(status);
    };
    const setStatus = async (st) => { await ev(`window.__upd.status = ${JSON.stringify(st)}; import(${S} + 'update_ui.js').then(m => m.startUpdateUi && m.startUpdateUi()).then(() => import(${S} + 'update_ui.js')).then(m => m.refreshUpdateStatus()).then(() => true)`); await settle(150); };
    const pill = () => ev(`(() => { const p = document.getElementById('update-pill'); if (!p) return null; const r = p.getBoundingClientRect(), cs = getComputedStyle(p); const vis = e => e && getComputedStyle(e).display !== 'none';
        return { hidden: p.hidden || r.width === 0, age: p.dataset.age, name: p.getAttribute('aria-label'), popup: p.getAttribute('aria-haspopup'), tag: p.tagName, h: Math.round(r.height), text: [...p.querySelectorAll('span')].filter(vis).map(e => e.textContent).join('').replace(/\\s+/g, ' ').trim(), anim: cs.animationName, trans: cs.transitionDuration }; })()`);
    const INTERACTIVE = new Set(['button', 'textbox', 'checkbox', 'link', 'combobox', 'tab', 'switch']);
    const axUnnamed = async sel => {
        await DOM.enable(); await Accessibility.enable();
        const { root } = await DOM.getDocument({ depth: 0 }); const { nodeId } = await DOM.querySelector({ nodeId: root.nodeId, selector: sel });
        if (!nodeId) return ['missing ' + sel];
        const { node } = await DOM.describeNode({ nodeId }); const { nodes } = await Accessibility.getFullAXTree();
        const byId = new Map(nodes.map(n => [n.nodeId, n])); const start = nodes.find(n => n.backendDOMNodeId === node.backendNodeId); if (!start) return ['not in the tree ' + sel];
        const out = []; const walk = n => { if (!n.ignored && INTERACTIVE.has(n.role && n.role.value) && !((n.name && n.name.value) || '').trim()) out.push(n.role.value); for (const c of n.childIds || []) { const k = byId.get(c); if (k) walk(k); } }; walk(start); return out;
    };

    const guard = await guardSettings(); // the Apply below saves Settings: they are put back
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const view of [{ name: 'desktop', w: 1280, h: 900, phone: false, theme: 'dark' }, { name: 'desktop', w: 1280, h: 900, phone: false, theme: 'light' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'dark' }, { name: '390px', w: 390, h: 844, phone: true, theme: 'light' }]) {
            const tag = `${view.name} ${view.theme}`; console.log(tag);

            // ═══ the flag is off: nothing shown, nothing fetched, no tab
            await load(view, { flag: false });
            await ev(`window.__upd.status = ${JSON.stringify(staged(3))}; true`); await settle(600);
            let p = await pill();
            check(`${tag}: flag off: the control is not shown and nothing asked /api/system/update (${JSON.stringify(await ev('window.__upd.calls'))})`, p && p.hidden && (await ev('window.__upd.calls.length')) === 0);
            await ev(`window.showSettingsModal(); true`); await waitFor(`!!document.getElementById('settings-tabs') && document.getElementById('settings-tabs').childElementCount > 0`, 'the settings tabs', 8000);
            check(`${tag}: flag off: Settings has no Updates tab`, await ev(`!document.getElementById('settings-tab-updates') && !document.getElementById('settings-pane-updates')`));
            await ev(`window.hideSettingsModal(); true`);

            // ═══ the control, in its three ages
            await load(view, { status: idle });
            p = await pill();
            check(`${tag}: nothing staged: no control`, p.hidden);
            for (const [days, tier] of [[0, 'fresh'], [3, 'warm'], [8, 'stale']]) {
                await setStatus(staged(days)); p = await pill();
                check(`${tag}: ready ${days} days: the control is a button, ${tier}, with a name that says the version and the age (${p.name})`, !p.hidden && p.tag === 'BUTTON' && p.age === tier && p.popup === 'dialog' && /1\.4\.126/.test(p.name) && (days === 0 ? /today/.test(p.name) : new RegExp(`${days} days ago`).test(p.name)));
                check(`${tag}: ${tier}: what it says (${p.text}) carries the age in words from "warm" on, on a phone too`, (days >= 2 ? /^Relaunch to update · \d+ days$/ : /^Relaunch to update$/).test(p.text));
                check(`${tag}: it never animates (${p.anim}, transitions ${p.trans})`, p.anim === 'none' && /^0s(, 0s)*$/.test(p.trans));
                if (view.phone) check(`${tag}: 44 px high (${p.h})`, p.h >= 44);
                await shot(`${tag} pill ${tier}`);
            }
            const au = await auditRegion(ev, ['#update-pill']);
            check(`${tag}: the control has 4.5:1 text contrast (${JSON.stringify((au.fails || []).slice(0, 2))})`, (au.fails || []).length === 0);

            // a phone: the control is a row under the bar, not a sixth button in it; the bar keeps its picker's name and its gear at every width, with the list and with an agent open
            if (view.phone) {
                await setStatus(staged(3));
                const where = await ev(`(() => { const p = document.getElementById('update-pill'), st = document.getElementById('update-strip'), bar = document.querySelector('.top-bar').getBoundingClientRect(), r = p.getBoundingClientRect(); return { inStrip: st.contains(p) && !st.hidden, below: r.top >= bar.bottom - 1, full: r.width >= innerWidth - 32, h: Math.round(r.height) }; })()`);
                check(`${tag}: the control is a full-width row under the top bar (${JSON.stringify(where)})`, where.inStrip && where.below && where.full && where.h >= 44);
                await ev(`window.__m.add('upd-a', 'A', 3, { sleeping: false }); true`); await settle(300);
                for (const w of [320, 360, 390]) {
                    for (const agent of [false, true]) {
                        await Emulation.setDeviceMetricsOverride({ width: w, height: view.h, deviceScaleFactor: 1, mobile: true });
                        if (agent) await ev(`window.selectLiveSession('upd-a', 'claude', 'upd-a-id', 'local'); true`); else await ev(`window.__coral && window.__coral.closeTopBarSettings && window.__coral.closeTopBarSettings(); window.showView && window.showView('sessions'); true`);
                        await settle(500);
                        const g = await ev(`(() => { const nav = document.getElementById('nav-select'), label = nav.firstElementChild, gear = [...document.querySelectorAll('.top-bar-actions .top-bar-btn')].pop().getBoundingClientRect(); return { over: document.documentElement.scrollWidth - document.documentElement.clientWidth, labelW: Math.round(label.clientWidth), cut: label.scrollWidth > label.clientWidth + 1, gearIn: gear.right <= innerWidth + 0.5 && gear.left >= 0 }; })()`);
                        await setStatus(idle); const b = await ev(`(() => { const nav = document.getElementById('nav-select'); return { labelW: Math.round(nav.firstElementChild.clientWidth) }; })()`); await setStatus(staged(3));
                        check(`${tag}: ${w}px, ${agent ? 'an agent open' : 'the list'}, update ready: no sideways scroll, the gear in view, and the picker's name as wide as with no update (${g.labelW} px, ${b.labelW} px without)`, g.over <= 0 && g.gearIn && g.labelW === b.labelW && g.labelW > 20);
                    }
                }
                await Emulation.setDeviceMetricsOverride({ width: view.w, height: view.h, deviceScaleFactor: 1, mobile: true });
            }

            // ═══ the confirmation
            await setStatus(staged(3));
            await ev(`document.getElementById('update-pill').focus(); true`); await press('Enter', 'Enter', 13);
            await waitFor(`document.getElementById('update-confirm-modal') && document.getElementById('update-confirm-modal').style.display === 'flex'`, `${tag}: the confirmation`, 5000); await settle(300);
            let d = await ev(`(() => { const m = document.getElementById('update-confirm-modal'); const go = m.querySelector('.update-go'), later = m.querySelector('.update-later'); const r = go.getBoundingClientRect();
                return { role: m.getAttribute('role'), modal: m.getAttribute('aria-modal'), title: document.getElementById('update-confirm-title').textContent, names: [...m.querySelectorAll('.update-agent-name')].map(e => e.textContent), notes: [...m.querySelectorAll('.update-agent-note')].map(e => e.textContent), go: go.textContent, goInView: r.bottom <= innerHeight && r.top >= 0, keep: m.querySelector('.update-keep').textContent, later: m.querySelector('.update-later-note').textContent }; })()`);
            check(`${tag}: it is a named alert dialog that lists the agents that restart, their resume notes, and what keeps running (${JSON.stringify(d)})`, d.role === 'alertdialog' && d.modal === 'true' && d.names.join() === 'api-worker,review-1,docs-2' && d.notes.join() === 'starts again afterwards,starts a new conversation' && d.go === 'Relaunch and restart 3 agents' && /tmux keep running/.test(d.keep) && /next time Coral starts/.test(d.later) && d.goInView);
            let f = await focused();
            check(`${tag}: with agents to restart the safe answer has focus (${JSON.stringify(f)})`, f && f.cls.includes('update-later'));
            const aud = await auditRegion(ev, ['#update-confirm-modal']);
            check(`${tag}: the dialog and its warning have 4.5:1 text contrast (${JSON.stringify((aud.fails || []).slice(0, 2))})`, (aud.fails || []).length === 0);
            const un = await axUnnamed('#update-confirm-modal');
            check(`${tag}: every control in the dialog has a name (${JSON.stringify(un)})`, un.length === 0);
            await shot(`${tag} confirm`);
            await press('Escape', 'Escape', 27); await settle(200);
            f = await focused();
            check(`${tag}: Escape closes it and focus is back on the control (${JSON.stringify(f)})`, f && f.id === 'update-pill' && await ev(`document.getElementById('update-confirm-modal').style.display === 'none'`));

            // with nothing to restart: no warning, and the action itself has focus
            await setStatus(staged(1, []));
            await ev(`document.getElementById('update-pill').click(); true`); await settle(400);
            d = await ev(`(() => { const m = document.getElementById('update-confirm-modal'); return { warn: !!m.querySelector('.update-warn'), go: m.querySelector('.update-go').textContent }; })()`); f = await focused();
            check(`${tag}: nothing to restart: no warning, "Relaunch now", focus on it (${JSON.stringify([d, f && f.text])})`, !d.warn && d.go === 'Relaunch now' && f && f.cls.includes('update-go'));
            await press('Escape', 'Escape', 27);

            // a long list scrolls; the buttons stay in view
            await setStatus(staged(2, agents(25)));
            await ev(`document.getElementById('update-pill').click(); true`); await settle(400);
            d = await ev(`(() => { const m = document.getElementById('update-confirm-modal'); const l = m.querySelector('.update-agents'), go = m.querySelector('.update-go').getBoundingClientRect(); return { n: l.children.length, scrolls: l.scrollHeight > l.clientHeight + 1, h: Math.round(l.clientHeight), vh: innerHeight, goInView: go.bottom <= innerHeight && go.top >= 0, go: m.querySelector('.update-go').textContent }; })()`);
            check(`${tag}: 25 agents: the list scrolls (${d.h} px of ${d.vh}), the primary button stays on screen and says how many (${d.go})`, d.n === 25 && d.scrolls && d.goInView && d.go === 'Relaunch and restart 25 agents');
            await shot(`${tag} confirm long`);
            await press('Escape', 'Escape', 27);

            // relaunch: the request carries the ack and nothing else; a refusal says why; a stale list is shown again
            await setStatus(staged(3));
            await ev(`window.__upd.calls.length = 0; window.__upd.relaunch = { status: 403, body: { error: 'x', code: 'owner_only' } }; document.getElementById('update-pill').click(); true`); await settle(300);
            await ev(`document.querySelector('#update-confirm-modal .update-go').click(); true`); await waitFor(`!!document.querySelector('#update-confirm-modal .update-error')`, `${tag}: the refusal`, 4000);
            check(`${tag}: a refusal says why in words (${await ev(`document.querySelector('#update-confirm-modal .update-error').textContent`)}) and the buttons work again`, /owner can relaunch/.test(await ev(`document.querySelector('#update-confirm-modal .update-error').textContent`)) && await ev(`!document.querySelector('#update-confirm-modal .update-go').disabled`));
            const fresh = staged(3, agents(4)).relaunch; fresh.ack = 'ack-2';
            await ev(`window.__upd.relaunch = { status: 409, body: { error: 'stale', code: 'stale_ack', relaunch: ${JSON.stringify({ available: true, restarts: fresh.restarts, tmux_agents_restart: false, ack: 'ack-2' })} } }; document.querySelector('#update-confirm-modal .update-go').click(); true`);
            await waitFor(`document.querySelectorAll('#update-confirm-modal .update-agent-name').length === 4`, `${tag}: the new list`, 4000); f = await focused();
            check(`${tag}: a stale ack shows the new list, says it changed, and focus is on the safe answer (${await ev(`document.querySelector('#update-confirm-modal .update-note').textContent`)})`, /changed/.test(await ev(`document.querySelector('#update-confirm-modal .update-note').textContent`)) && f && f.cls.includes('update-later'));
            await ev(`window.__upd.calls.length = 0; window.__upd.relaunch = { status: 202, body: { ok: true, operation: 'op-1' } }; document.querySelector('#update-confirm-modal .update-go').click(); true`);
            await waitFor(`/Relaunching/.test(document.querySelector('#update-confirm-modal .modal-body').textContent)`, `${tag}: relaunching`, 4000);
            const post = await ev(`window.__upd.calls.filter(c => c.method === 'POST')`);
            check(`${tag}: the request carries the new ack and nothing else (${JSON.stringify(post)})`, post.length === 1 && post[0].url === '/relaunch-local' && JSON.stringify(post[0].body) === '{"ack":"ack-2"}');
            await press('Escape', 'Escape', 27);

            // ═══ through Coral Hub: the hub's own answers (#5295)
            const dlgOpen = async (st = staged(3)) => { await setStatus(st); await ev(`(() => { const d = document.getElementById('update-confirm-modal'); if (d) d.style.display = 'none'; document.getElementById('update-pill').click(); return true; })()`); await waitFor(`document.getElementById('update-confirm-modal').style.display === 'flex'`, 'the dialog', 4000); await settle(200); };
            const answer = async (status, body) => { await ev(`window.__upd.calls.length = 0; window.__upd.relaunch = { status: ${status}, body: ${JSON.stringify(body)} }; document.querySelector('#update-confirm-modal .update-go').click(); true`); await settle(500); };
            const errText = () => ev(`(() => { const e = document.querySelector('#update-confirm-modal .update-error'); return e ? e.textContent : null; })()`);
            await dlgOpen();
            await answer(401, { reason: 'reauth_required', sign_in_url: '/signin?next=%2F' });
            let sg = await ev(`(() => { const a = document.querySelector('#update-confirm-modal .update-signin'); const go = document.querySelector('#update-confirm-modal .update-go'); return a ? { href: a.getAttribute('href'), go: go.hidden, focus: document.activeElement === a, posts: window.__upd.calls.filter(c => c.method === 'POST').length } : null; })()`);
            check(`${tag}: the hub's 401 reauth_required: "${await errText()}", a Sign in again link to the hub's own address, the relaunch button gone, focus on the link, one request and no retry (${JSON.stringify(sg)})`, (await errText()) === 'Sign in again to relaunch this Coral.' && sg && sg.href === '/signin?next=%2F' && sg.go && sg.focus && sg.posts === 1);
            await ev(`document.addEventListener('click', e => { if (e.target.closest('.update-signin')) e.preventDefault(); }, { capture: true, once: true }); document.querySelector('#update-confirm-modal .update-signin').click(); true`);
            check(`${tag}: following it leaves a note to come back (${await ev(`sessionStorage.getItem('coral-update-resume')`)}) and sends nothing`, (await ev(`sessionStorage.getItem('coral-update-resume')`)) === '1' && (await ev(`window.__upd.calls.filter(c => c.method === 'POST').length`)) === 1);
            // back from signing in: the status is read afresh (a new ack) and the dialog is shown again; the relaunch is not sent for the person
            await ev(`document.getElementById('update-confirm-modal').style.display = 'none'; window.__upd.calls.length = 0; true`);
            await setStatus({ ...staged(3), relaunch: { ...staged(3).relaunch, ack: 'ack-fresh' } });
            await ev(`import(${S} + 'update_ui.js').then(m => m.resumeAfterSignIn()).then(r => { window.__resumed = r; return true; })`); await settle(500);
            check(`${tag}: back from signing in the dialog is shown again from a fresh status and nothing was relaunched (${JSON.stringify(await ev('window.__upd.calls.map(c => c.method + c.url)'))})`, (await ev(`window.__resumed`)) === true && (await ev(`document.getElementById('update-confirm-modal').style.display`)) === 'flex' && (await ev(`window.__upd.calls.some(c => c.method === 'GET' && c.url === '/status') && !window.__upd.calls.some(c => c.method === 'POST')`)) && (await ev(`sessionStorage.getItem('coral-update-resume')`)) === null);
            await ev(`document.getElementById('update-confirm-modal').style.display = 'none'; true`);
            // an address that is not on this site is never followed
            await dlgOpen(); await answer(401, { reason: 'reauth_required', sign_in_url: 'https://evil.test/x' });
            check(`${tag}: a sign-in address on another site is not followed (${await ev(`document.querySelector('#update-confirm-modal .update-signin').getAttribute('href')`)})`, !/evil/.test(await ev(`document.querySelector('#update-confirm-modal .update-signin').getAttribute('href')`)));
            for (const [status, body, want, what] of [[403, { reason: 'mode_too_low' }, 'Needs Launch access on this machine.', 'mode_too_low'], [429, { reason: 'relaunch_fleet_busy' }, 'Try again in a few minutes.', '429 relaunch_fleet_busy'], [429, { reason: 'too_soon' }, 'Try again in a few minutes.', '429 too_soon'], [500, { reason: 'something_new' }, 'Unavailable', 'a reason this build does not know']]) {
                await dlgOpen(); await answer(status, body);
                check(`${tag}: ${what}: "${await errText()}", never the raw reason`, (await errText()) === want);
            }
            await dlgOpen(); await answer(404, { error: 'not found' });
            check(`${tag}: 404 (the hub's switch is off): the dialog closes and the control is gone`, (await ev(`document.getElementById('update-confirm-modal').style.display`)) === 'none');
            await ev(`window.__upd.status = null; true`); await ev(`import(${S} + 'update_ui.js').then(m => m.refreshUpdateStatus())`); await settle(300);
            check(`${tag}: and no control is shown`, (await pill()).hidden);
            // the status's own reasons
            for (const [reason, name, shown] of [['mode_too_low', /Needs Launch access on this machine/, true], ['fleet_busy', /Try again in a few minutes/, true], ['label_missing', /not set up for relaunching from Coral Hub/, true], ['hub_off', null, false], ['mystery', /Unavailable/, true]]) {
                const st = staged(3); st.relaunch = { ...st.relaunch, available: false, reason };
                await setStatus(st); const pl = await pill();
                check(`${tag}: status reason ${reason}: ${shown ? `the control says "${(pl.name || '').slice(-60)}" and is not pressable` : 'no control'}${reason === 'mystery' ? ' (never the raw word)' : ''}`, shown ? (!pl.hidden && name.test(pl.name) && !/mystery/.test(pl.name) && (await ev(`document.getElementById('update-pill').getAttribute('aria-disabled')`)) === 'true') : pl.hidden);
            }
            await setStatus(staged(3));

            // ═══ Settings > Updates
            const open = async () => { await ev(`window.showSettingsModal('updates'); true`); await waitFor(`!document.getElementById('settings-pane-updates')?.hidden && !!document.querySelector('#update-pane-body .update-status, #update-pane-body .update-managed, #update-pane-body .update-calm')`, `${tag}: the Updates pane`, 8000); await settle(200); };
            const pane = () => ev(`(() => { const b = document.getElementById('update-pane-body'); const t = sel => (b.querySelector(sel) || {}).textContent; return { title: t('.update-status-text b') || t('.update-managed b'), sub: t('.update-status-text p'), k: (b.querySelector('.update-status') || {}).dataset?.k, act: t('.update-act'), auto: (document.getElementById('settings-update-auto') || {}).checked, autoOff: (document.getElementById('settings-update-auto') || {}).disabled, hub: (document.getElementById('settings-update-hub') || {}).checked, cmd: t('.update-cmd code'), link: (b.querySelector('.update-link') || {}).href, bar: (b.querySelector('[role=progressbar]') || {}).getAttribute && b.querySelector('[role=progressbar]').getAttribute('aria-valuenow'), facts: t('.update-facts'), priv: t('.update-privacy') }; })()`);
            await setStatus(staged(3)); await open(); let pn = await pane();
            check(`${tag}: Updates, ready: the card, its sentence and its action (${JSON.stringify([pn.title, pn.sub, pn.act, pn.k])})`, pn.title === 'Coral 1.4.126 is ready' && /3 days ago. It installs the next time Coral starts/.test(pn.sub) && pn.act === 'Relaunch to update' && pn.k === 'warm' && pn.auto === true && pn.hub === undefined && /1\.4\.125/.test(pn.facts) && /GitHub only/.test(pn.priv));
            await shot(`${tag} settings ready`);
            const au2 = await auditRegion(ev, ['#settings-pane-updates']);
            const un2 = await axUnnamed('#settings-pane-updates');
            check(`${tag}: the pane has 4.5:1 text contrast (${JSON.stringify((au2.fails || []).slice(0, 2))}) and every control a name (${JSON.stringify(un2)})`, (au2.fails || []).length === 0 && un2.length === 0);
            // the switches are saved with Apply, not at once
            await ev(`window.__upd.calls.length = 0; document.getElementById('settings-update-auto').click(); true`); await settle(200);
            check(`${tag}: a switch is not saved until Apply (${JSON.stringify(await ev('window.__upd.calls.filter(c => c.method === "PUT")'))})`, (await ev('window.__upd.calls.filter(c => c.method === "PUT").length')) === 0);
            await ev(`window.applySettings(); true`); await waitFor(`window.__upd.calls.some(c => c.method === 'PUT')`, `${tag}: the save`, 6000);
            const put = await ev(`window.__upd.calls.filter(c => c.method === 'PUT')`);
            check(`${tag}: Apply saves the auto switch to its route (${JSON.stringify(put)})`, put.length === 1 && put[0].url === '/settings' && JSON.stringify(put[0].body) === '{"auto":false}');
            // the card's states (Apply closed the dialog: open it again, or the audits below would look at nothing)
            await setStatus(staged(3)); await open();
            for (const [what, st, expect] of [
                ['up to date', upToDate, { title: 'Coral is up to date', act: 'Check now' }],
                ['nothing known', idle, { title: 'Nothing is ready to install' }],
                ['a newer one not promoted', { ...idle, not_ready_reason: 'not_promoted', latest: '1.4.130' }, { title: 'Coral 1.4.130 is out', sub: 'Coral will download and check it once it is ready. Installing it is up to you.' }],
                ['backing off', { ...idle, not_ready_reason: 'backoff' }, { title: 'Nothing to install right now', sub: 'Coral will check again later.' }],
                ['check failed', { ...idle, not_ready_reason: 'check_failed' }, { title: 'Coral could not check for updates', k: 'warm' }],
                ['not checked yet', { ...idle, not_ready_reason: 'not_checked' }, { title: 'Coral has not checked for updates yet' }],
                ['the switch is off', { ...idle, not_ready_reason: 'switch_off' }, { title: 'Automatic update checks are off' }],
                ['downloading', { ...idle, progress: 64, staged: { version: '1.4.126', staged_at: '', days_ready: 0, verified: false } }, { title: 'Downloading Coral 1.4.126', bar: '64' }],
                ['failed', { ...idle, state: 'failed', last_result: { at: '2026-10-19T10:00:00Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } }, { title: 'Coral found an update (v1.4.126) it could not verify, so it was not installed.', sub: 'Your version is unchanged.', act: 'Try again' }],
                ['Homebrew', { ...idle, install_kind: 'homebrew', upgrade_command: 'brew upgrade --cask coral', relaunch: { ...idle.relaunch, reason: 'unsupported_install' } }, { title: 'Homebrew updates this Coral', cmd: 'brew upgrade --cask coral', autoOff: true }],
                ['Homebrew by its reason alone (no install_kind: an older server)', { ...idle, relaunch: { ...idle.relaunch, reason: 'unsupported_install' } }, { title: 'This install cannot update itself', autoOff: true }],
                ['a folder it cannot write to', { ...idle, install_kind: 'unwritable', relaunch: { ...idle.relaunch, reason: 'unsupported_install' } }, { title: 'This install cannot update itself', link: 'https://github.com/subgentic/coral/releases', autoOff: true }],
            ]) {
                await setStatus(st); await ev(`import(${S} + 'update_settings.js').then(m => m.showUpdatesPane())`); await settle(300); pn = await pane();
                check(`${tag}: Updates, ${what} (${JSON.stringify(pn).slice(0, 140)})`, Object.entries(expect).every(([k, v]) => pn[k] === v), JSON.stringify(expect));
                const auS = await auditRegion(ev, ['#settings-pane-updates']);
                check(`${tag}: Updates, ${what}: 4.5:1 text contrast (${JSON.stringify((auS.fails || []).slice(0, 2))})`, (auS.fails || []).length === 0);
                if (what === 'unsupported' && view.phone) { const lk = await ev(`(() => { const a = document.querySelector('.update-link'); const r = a.getBoundingClientRect(); const cs = getComputedStyle(a); return { h: Math.round(r.height), w: Math.round(r.width), display: cs.display, pane: !document.getElementById('settings-pane-updates').hidden, modal: getComputedStyle(document.getElementById('settings-modal')).display }; })()`); check(`${tag}: the releases link is a 44 px target on a phone (${JSON.stringify(lk)})`, lk.h >= 44); }
                await shot(`${tag} settings ${what}`);
            }
            await ev(`window.hideSettingsModal(); true`);

            // ═══ the failure notice
            await setStatus({ ...idle, state: 'failed', last_result: { at: '2026-10-19T10:00:00Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } });
            let b = await ev(`(() => { const e = document.getElementById('update-fail-banner'); return { hidden: e.hidden, role: e.getAttribute('role'), text: e.textContent.replace(/\\s+/g, ' ').trim() }; })()`);
            check(`${tag}: a refused download says plainly that nothing was changed (${b.text})`, !b.hidden && b.role === 'alert' && /Coral found an update \(v1\.4\.126\) it could not verify, so it was not installed\. Your version is unchanged\./.test(b.text));
            await shot(`${tag} failure`);
            const aub = await auditRegion(ev, ['#update-fail-banner']);
            check(`${tag}: the notice has 4.5:1 text contrast (${JSON.stringify((aub.fails || []).slice(0, 2))}) and named controls (${JSON.stringify(await axUnnamed('#update-fail-banner'))})`, (aub.fails || []).length === 0 && (await axUnnamed('#update-fail-banner')).length === 0);
            await ev(`window.__upd.calls.length = 0; document.querySelector('#update-fail-banner .update-fail-again').click(); true`); await settle(400);
            check(`${tag}: Try again asks the updater to check, then reads the status (${JSON.stringify(await ev('window.__upd.calls.map(c => c.method + c.url)'))})`, (await ev(`window.__upd.calls.map(c => c.method + c.url)`)).join() === 'POST/check,GET/status');
            await ev(`document.querySelector('#update-fail-banner .update-fail-close').click(); true`); await settle(100);
            await setStatus({ ...idle, state: 'failed', last_result: { at: '2026-10-19T10:00:00Z', from: '1.4.125', to: '1.4.126', result: 'refused', by: 'local' } });
            check(`${tag}: dismissed, it stays away for the same failure`, await ev(`document.getElementById('update-fail-banner').hidden`));
            await setStatus({ ...idle, installed: '1.4.125', last_result: { at: '2026-10-20T10:00:00Z', from: '1.4.125', to: '1.4.127', result: 'failed_rolled_back', by: 'local' } });
            b = await ev(`(() => { const e = document.getElementById('update-fail-banner'); return { hidden: e.hidden, text: e.textContent.replace(/\\s+/g, ' ').trim() }; })()`);
            check(`${tag}: a new failure shows again, and a rollback says the agents are unaffected (${b.text})`, !b.hidden && /The update to 1\.4\.127 was undone\..*went back to 1\.4\.125\. Your agents are unaffected\./.test(b.text));
            // nothing sideways on a phone
            check(`${tag}: nothing wider than the screen`, (await ev(`document.documentElement.scrollWidth - document.documentElement.clientWidth`)) <= 0);

            // ═══ the server as it is today: no route to check now or to save the auto switch (QA, #5302), so neither is drawn or sent; the hub switch has its own route
            await load(view, { routes: false });
            await setStatus(staged(3));
            await ev(`window.showSettingsModal('updates'); true`); await waitFor(`!!document.querySelector('#update-pane-body .update-status')`, `${tag}: the pane`, 6000);
            let bare = await ev(`({ auto: !!document.getElementById('settings-update-auto'), hub: !!document.getElementById('settings-update-hub'), check: !![...document.querySelectorAll('.update-act')].find(b => /Check now|Try again/.test(b.textContent)) })`);
            check(`${tag}: without server routes the auto switch is not drawn, and the hub switch is not in this pane (it is in Network and Hub) (${JSON.stringify(bare)})`, !bare.auto && !bare.hub);
            await setStatus(upToDate); await ev(`import(${S} + 'update_settings.js').then(m => m.showUpdatesPane())`); await settle(300);
            bare = await ev(`({ check: !![...document.querySelectorAll('.update-act')].find(b => /Check now|Try again/.test(b.textContent)), title: (document.querySelector('.update-status-text b') || {}).textContent })`);
            check(`${tag}: and Check now is not offered (${JSON.stringify(bare)})`, !bare.check && bare.title === 'Coral is up to date');
            // the Coral Hub relaunch switch lives in Settings > Network and Hub (#5383): there, it saves to PUT /api/cloud/hub-relaunch {allow} at once; Updates only points to it
            await ev(`window.showSettingsModal('network'); true`); await settle(300);
            await ev(`window.__upd.calls.length = 0; (() => { const c = document.getElementById('settings-cloud-hub-relaunch'); c.checked = false; c.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await waitFor(`window.__upd.calls.some(c => c.method === 'PUT')`, `${tag}: the hub save`, 6000);
            const hubPut = await ev(`window.__upd.calls.filter(c => c.method !== 'GET')`);
            check(`${tag}: the hub switch in Network and Hub saves to PUT /api/cloud/hub-relaunch {allow} at once and nothing goes to a path the server lacks (${JSON.stringify(hubPut)})`, hubPut.length === 1 && hubPut[0].url === '/hub-relaunch' && JSON.stringify(hubPut[0].body) === '{"allow":false}');
            await ev(`window.showSettingsModal('updates'); true`); await settle(300);
            const pointer = await ev(`(() => { const b = document.getElementById('update-pane-body'); const go = [...b.querySelectorAll('button')].find(x => /Network and Hub/.test(x.textContent)); return { text: b.textContent.includes('Relaunch from Coral Hub'), go: !!go }; })()`);
            check(`${tag}: Updates says where the hub switch is (${JSON.stringify(pointer)})`, pointer.text && pointer.go);
        }
        const failed = results.filter(r => !r).length;
        console.log(`${results.length - failed} of ${results.length} checks passed`);
        await guard.restore();
        await client.close();
        process.exit(failed ? 1 : 0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        await guard.restore().catch(() => {});
        await client.close().catch(() => {});
        process.exit(1);
    }
})();
