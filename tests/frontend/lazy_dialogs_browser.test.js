// #4500: the Settings, Launch, Job, Create task, Info, Default prompts and Custom view dialogs and the top-bar settings menu
// are in the page as templates and built on first open. At load none of them is in the document (their templates hold some
// 800 elements); each opens as before at desktop width and on a 390 px touch phone: focus moves in, Escape closes it, focus
// goes back to what opened it, and on the phone every control in it is at least 44x44. Settings saves (Apply sends PUT
// /api/settings and closes it); the menu opens with its items (Servers and Stop all auto-accept hidden here) and closes on an
// outside click. The parts' own saves are covered by their suites (scheduler_stale_save, default_prompts_stale_save,
// actions_tasks, custom_views_*).
'use strict';

const assert = require('node:assert/strict');
const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;
const { TOUCH_AFTER } = require('./helpers/phone_scan');

const LAZY = ['settings-modal', 'launch-modal', 'job-modal', 'create-task-modal', 'info-modal', 'default-prompts-modal', 'custom-view-modal', 'top-bar-settings-menu'];

// Controls in `root` that are shown and smaller than 44px either way (a checkbox or radio counts through its label), as
// touch_targets_browser measures the page.
const undersized = sel => `(() => {
    const root = document.querySelector(${JSON.stringify(sel)}); const out = [];
    for (const el of root.querySelectorAll('button, a[href], [role=button], input:not([type=hidden]), select, summary, [data-on-click]')) {
        const hit = el.matches('input[type=checkbox], input[type=radio]') && el.closest('label') ? el.closest('label') : el;
        const r = hit.getBoundingClientRect(); const cs = getComputedStyle(el);
        if (!r.width || !r.height || cs.visibility === 'hidden' || cs.display === 'none') continue;
        if (r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
        if (r.width >= 44 && r.height >= 44) continue;
        if (${TOUCH_AFTER}(el)) continue; // the (i) is 24 px with a 44 px ::after (#6750)
        out.push(Math.round(r.width) + 'x' + Math.round(r.height) + ' ' + (el.id ? '#' + el.id : el.className) + ' [' + (el.getAttribute('aria-label') || el.title || el.textContent.trim()).slice(0, 30) + ']');
    }
    return out;
})()`;

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation, Input } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 600));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const S = `(window.__coralStatic || '/static/')`;
    const shown = id => `(() => { const e = document.getElementById(${JSON.stringify(id)}); return !!e && getComputedStyle(e).display !== 'none' && e.getClientRects().length > 0; })()`;
    const escape = async () => {
        await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
        await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    };
    let viewId = null;

    const views = [
        { name: 'desktop', width: 1400, height: 900, mobile: false, coarse: false },
        { name: 'phone 390', width: 390, height: 844, mobile: true, coarse: true },
    ];
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back before it ends (#4849)
    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        for (const v of views) {
            await Emulation.setDeviceMetricsOverride({ width: v.width, height: v.height, deviceScaleFactor: 1, mobile: v.mobile });
            await Emulation.setEmulatedMedia({ features: v.coarse ? [{ name: 'pointer', value: 'coarse' }, { name: 'hover', value: 'none' }] : [{ name: 'pointer', value: 'fine' }, { name: 'hover', value: 'hover' }] });
            await Emulation.setTouchEmulationEnabled({ enabled: v.coarse, maxTouchPoints: v.coarse ? 5 : 1 });
            const loaded = Page.loadEventFired();
            await Page.navigate({ url: BASE + '/' });
            await loaded;
            await coralReady();
            console.log(`${v.name}: at load`);
            const atLoad = await ev(`(() => ({
                inDoc: ${JSON.stringify(LAZY)}.filter(id => document.getElementById(id)),
                templates: ${JSON.stringify(LAZY)}.map(id => document.querySelectorAll('template[data-lazy="' + id + '"]').length),
                lazyElements: [...document.querySelectorAll('template[data-lazy]')].reduce((n, t) => n + t.content.querySelectorAll('*').length, 0),
                elements: document.querySelectorAll('*').length }))()`);
            assert.deepEqual(atLoad.inDoc, [], 'none of them is built at load');
            assert.deepEqual(atLoad.templates, LAZY.map(() => 1), 'each has its template');
            assert.ok(atLoad.lazyElements >= 800, `their templates hold ${atLoad.lazyElements} elements`);
            console.log(`   ${atLoad.elements} elements in the page, ${atLoad.lazyElements} more in templates`);
            if (!viewId) { // a saved view, for the Custom view editor
                viewId = await ev(`fetch('/api/views', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'QA lazy view', html: '<p>qa</p>' }) }).then(r => r.json()).then(d => d.id)`);
                assert.ok(viewId, 'a saved view');
            }
            // A page fetch log, and the Info dialog's answer for a stand-in agent.
            await ev(`(() => { const real = window.fetch; window.__reqs = [];
                window.fetch = (u, o = {}) => { const url = String((u && u.url) || u); window.__reqs.push([(o.method || 'GET').toUpperCase(), url, typeof o.body === 'string' ? o.body : '']);
                    if (url.includes('/api/sessions/live/qa-info/info')) return Promise.resolve(new Response(JSON.stringify({ agent_name: 'qa-info', agent_type: 'claude', tmux_session: 'qa', tmux_command: 'claude', working_directory: '/tmp/qa', log_path: '/tmp/qa.log', pane_title: 'qa' }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
                    return real(u, o); };
                const b = document.createElement('button'); b.id = 'qa-opener'; b.textContent = 'opener'; b.style.cssText = 'position:fixed;top:0;left:0;z-index:5;width:60px;height:44px';
                document.body.appendChild(b); return true; })()`);

            const opens = [
                ['settings-modal', `window.showSettingsModal()`],
                ['launch-modal', `window.showLaunchModal()`],
                ['job-modal', `window.showJobModal()`],
                ['create-task-modal', `window.showCreateTaskModal()`],
                ['info-modal', `(async () => { const st = (await import(${S} + 'state.js')).state; st.currentSession = { type: 'live', name: 'qa-info', agent_type: 'claude', session_id: 'qa-info-id' }; return window.showInfoModal(); })()`],
                ['default-prompts-modal', `window.showDefaultPromptsModal()`],
                ['custom-view-modal', `(async () => { await (await import(${S} + 'custom_views.js')).loadCustomViews();
                    const w = document.createElement('div'); const b = document.createElement('button'); b.dataset.onClick = 'custom-view-edit'; b.dataset.viewId = ${JSON.stringify(String(viewId))};
                    w.appendChild(b); document.body.appendChild(w); b.click(); w.remove(); return true; })()`],
            ];
            // On the desktop summaries are switched before Settings is first built, so its box has a change to follow.
            if (!v.coarse) {
                const hidden = await ev(`document.querySelector('.sidebar').classList.contains('summary-hidden')`);
                await ev(`(async () => { await window.toggleSessionSummary(); return true; })()`);
                await waitFor(`document.querySelector('.sidebar').classList.contains('summary-hidden') === ${!hidden}`, 'summaries switched');
            }
            const small = {};
            for (const [id, open] of opens) {
                await ev(`document.getElementById('qa-opener').focus(); ${open}; true`);
                await waitFor(shown(id), `${v.name}: ${id} opens`, { timeout: 8000 });
                await waitFor(`document.getElementById(${JSON.stringify(id)}).contains(document.activeElement)`, `${v.name}: focus moves into ${id}`);
                if (id === 'settings-modal') {
                    // What code set at load before Settings was lazy is set as it is built: the Show summaries box follows the list.
                    assert.equal(await ev(`document.getElementById('settings-show-summary').checked === !document.querySelector('.sidebar').classList.contains('summary-hidden')`), true,
                        `${v.name}: Show summaries matches the list`);
                }
                if (v.coarse) { const s = await ev(undersized('#' + id)); if (s.length) small[id] = s; }
                await escape();
                await waitFor(`!${shown(id)}`, `${v.name}: Escape closes ${id}`);
                await waitFor(`document.activeElement && document.activeElement.id === 'qa-opener'`, `${v.name}: focus back on the opener after ${id}`);
                console.log(`   ${id}: opens, focus in, ${v.coarse ? '44px, ' : ''}Escape closes, focus back`);
            }

            // Apply sends only what changed (#4423): one box is flipped, saved, and flipped back the same way.
            assert.deepEqual(small, {}, `${v.name}: every control in each dialog is at least 44x44`);
            if (!v.coarse) await ev(`(async () => { await window.toggleSessionSummary(); return true; })()`); // back on
            console.log(`${v.name}: Settings saves`);
            for (const pass of ['flip', 'restore']) {
                await ev(`window.__reqs.length = 0; window.showSettingsModal(); true`);
                await waitFor(shown('settings-modal'), `Settings (${pass})`);
                await waitFor(`document.getElementById('settings-cli-path-claude')?.dataset.filled === '1'`, 'Settings filled in', { timeout: 8000 });
                const want = await ev(`(() => { const b = document.getElementById('settings-show-scrollbars'); b.checked = !b.checked; b.dispatchEvent(new Event('change', { bubbles: true })); return b.checked; })()`);
                await ev(`document.querySelector('#settings-modal [data-on-click="m-apply-settings"]').click(); true`);
                const body = await waitFor(`(window.__reqs.find(([m, u]) => m === 'PUT' && new URL(u, location.href).pathname === '/api/settings') || [])[2]`, `${v.name}: Apply sends PUT /api/settings (${pass})`);
                const sent = JSON.parse(body);
                const saved = sent.settings || sent;
                assert.equal(String(saved.show_scrollbars), String(want), `${v.name}: the changed setting is saved (${pass}): ${body.slice(0, 200)}`);
                await waitFor(`!${shown('settings-modal')}`, `${v.name}: and closes Settings (${pass})`);
            }

            console.log(`${v.name}: the top-bar menu`);
            const menuBtn = `document.querySelector('[data-on-click="m-toggle-top-bar-settings"]')`;
            if (await ev(`(() => { const b = ${menuBtn}; return !!b && b.getClientRects().length > 0; })()`)) {
                // On the desktop an agent has auto-accept on before the menu is first built: Stop all is shown when it is.
                const aaOn = !v.coarse;
                if (aaOn) await ev(`(async () => { (await import(${S} + 'state.js')).state.liveSessions.push({ name: 'qa-aa', session_id: 'qa-aa-id', agent_type: 'claude', auto_accept: true }); return true; })()`);
                await ev(`${menuBtn}.click(); true`);
                await waitFor(shown('top-bar-settings-menu'), `${v.name}: the menu opens`);
                const menu = await ev(`({ items: document.querySelectorAll('#top-bar-settings-menu .overflow-menu-item').length,
                    stopAll: document.getElementById('stop-all-auto-accept').hidden,
                    servers: getComputedStyle(document.querySelector('#top-bar-settings-menu [data-hub-only]')).display })`);
                assert.ok(menu.items >= 6, `the menu has its items (${menu.items})`);
                assert.equal(menu.stopAll, !aaOn, aaOn ? 'Stop all auto-accept shown: an agent has it on' : 'Stop all auto-accept hidden with no agent on it');
                if (aaOn) await ev(`(async () => { const st = (await import(${S} + 'state.js')).state; st.liveSessions = st.liveSessions.filter(s => s.session_id !== 'qa-aa-id'); return true; })()`);
                assert.equal(menu.servers, 'none', 'Servers hidden on a server that is not a hub');
                await ev(`document.getElementById('main-content')?.click() ?? document.body.click(); true`);
                await waitFor(`!${shown('top-bar-settings-menu')}`, `${v.name}: an outside click closes the menu`);
                console.log('   opens with its items, closes on an outside click');
                // From the keyboard: Enter on the gear, Enter on Settings, Escape: focus goes back to the gear, not the page (UX on #4500).
                const enter = async () => {
                    await Input.dispatchKeyEvent({ type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r' });
                    await Input.dispatchKeyEvent({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
                };
                await ev(`${menuBtn}.focus(); true`);
                await enter();
                await waitFor(shown('top-bar-settings-menu'), `${v.name}: Enter on the gear opens the menu`);
                await ev(`document.querySelector('#top-bar-settings-menu [data-on-click="m-close-top-bar-settings-then-show-settings-modal"]').focus(); true`);
                await enter();
                await waitFor(shown('settings-modal'), `${v.name}: Enter on Settings opens it`);
                await waitFor(`document.getElementById('settings-modal').contains(document.activeElement)`, `${v.name}: focus in Settings`);
                await escape();
                await waitFor(`!${shown('settings-modal')}`, `${v.name}: Escape closes Settings`);
                await waitFor(`document.activeElement === ${menuBtn}`, `${v.name}: focus back on the gear`, { diag: `(document.activeElement && (document.activeElement.id || document.activeElement.className || document.activeElement.tagName))` });
                console.log('   from the keyboard, Settings opened from the menu gives focus back to the gear');
                // And with the mouse (UX's path): a click on the gear, a click on Settings, Escape.
                // On a fresh page, so this is Settings' first open (modals.js loads while the menu closes).
                const reloaded = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await reloaded; await coralReady();
                const clickAt = async sel => {
                    const c = await ev(`(() => { const r = document.querySelector(${JSON.stringify(sel)}).getBoundingClientRect(); return [r.left + r.width / 2, r.top + r.height / 2]; })()`);
                    for (const type of ['mousePressed', 'mouseReleased']) await Input.dispatchMouseEvent({ type, x: c[0], y: c[1], button: 'left', clickCount: 1 });
                };
                await ev(`document.activeElement && document.activeElement.blur(); true`);
                await clickAt('[data-on-click="m-toggle-top-bar-settings"]');
                await waitFor(shown('top-bar-settings-menu'), `${v.name}: a click on the gear opens the menu`);
                await clickAt('#top-bar-settings-menu [data-on-click="m-close-top-bar-settings-then-show-settings-modal"]');
                await waitFor(shown('settings-modal'), `${v.name}: a click on Settings opens it`);
                await waitFor(`document.getElementById('settings-modal').contains(document.activeElement)`, `${v.name}: focus in Settings (mouse)`);
                await escape();
                await waitFor(`!${shown('settings-modal')}`, `${v.name}: Escape closes Settings (mouse)`);
                await waitFor(`document.activeElement === ${menuBtn}`, `${v.name}: focus back on the gear (mouse)`, { diag: `(document.activeElement && (document.activeElement.id || document.activeElement.className || document.activeElement.tagName))` });
                console.log('   with the mouse too');
                // A click that does not move focus (Safari on a Mac doesn't focus a clicked button; a scripted click): still the gear.
                { const r = Page.loadEventFired(); await Page.navigate({ url: BASE + '/' }); await r; await coralReady(); } // no focus history
                await ev(`document.activeElement && document.activeElement.blur(); ${menuBtn}.click(); true`);
                await waitFor(shown('top-bar-settings-menu'), `${v.name}: the menu (no focus)`);
                await ev(`document.querySelector('#top-bar-settings-menu [data-on-click="m-close-top-bar-settings-then-show-settings-modal"]').click(); true`);
                await waitFor(shown('settings-modal'), `${v.name}: Settings (no focus)`);
                await waitFor(`document.getElementById('settings-modal').contains(document.activeElement)`, `${v.name}: focus in Settings (no focus)`);
                await escape();
                await waitFor(`!${shown('settings-modal')}`, `${v.name}: Escape closes Settings (no focus)`);
                await waitFor(`document.activeElement === ${menuBtn}`, `${v.name}: focus back on the gear after a click that moved no focus`, { diag: `(document.activeElement && (document.activeElement.id || document.activeElement.className || document.activeElement.tagName))` });
                console.log('   and after a click that moved no focus');
            } else {
                console.log('   (no top-bar menu button at this width)');
            }
        }
        await ev(`fetch('/api/views/' + ${JSON.stringify(String(viewId))}, { method: 'DELETE' }).then(r => r.status)`);
        console.log('lazy_dialogs: ok');
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
        process.exit(0);
    } catch (e) {
        console.error('FAIL', e && e.stack || e);
        try { if (viewId) await ev(`fetch('/api/views/' + ${JSON.stringify(String(viewId))}, { method: 'DELETE' }).then(r => r.status)`); } catch { /* best effort */ }
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        try { await client.close(); } catch { /* closing */ }
        process.exit(1);
    }
})();
