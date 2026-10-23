// Settings > Terminal backend (#3634): a row with the two backends and Security's wording, what
// runs now, and a change that applies when Coral restarts.
//  - Picking the other backend shows "Switches to … when Coral restarts". Apply asks first;
//    Cancel saves nothing and keeps Settings open.
//  - Going to pty with agents running: the confirm says how many; confirming puts them to sleep
//    BEFORE the backend is saved, then restarts (or, where Coral can't restart itself, says to).
//  - A system with no choice (Windows, no tmux) shows pty as fixed, no radios.
//  - Under pty with agents left in the old tmux server: a banner with End them, which asks, ends
//    them and goes away.
// The built-in terminal (pty) is the default (#6064): this server runs it, so each state the row shows (an install that chose tmux, the default, the one-time notice, leftover agents) is served by a fetch wrapper.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const open = async () => {
        const l = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await l;
        await coralReady();
    };
    // A fetch wrapper: records the calls in order; window.__tbFake overrides the status GET and
    // fakes sleep-all, restart and end-leftover (they would act on the shared test server).
    const wrap = () => ev(`(() => {
        if (window.__tbWrapped) return true;
        window.__tbWrapped = true; window.__tbCalls = [];
        const inner = window.fetch;
        window.fetch = (url, opts = {}) => {
            const u = String(url), m = (opts.method || 'GET').toUpperCase();
            if (/\\/api\\/system\\/terminal-backend|sleep-all|\\/api\\/system\\/restart/.test(u)) window.__tbCalls.push(m + ' ' + u.replace(/^https?:\\/\\/[^/]+/, '') + (opts.body && m === 'PUT' ? ' ' + opts.body : ''));
            const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'content-type': 'application/json' } }));
            const f = window.__tbFake;
            if (f && m === 'GET' && /\\/api\\/system\\/terminal-backend$/.test(u) && f.status) return json(typeof f.status === 'function' ? f.status() : f.status);
            if (f && /sleep-all/.test(u)) return json({ ok: true, slept: 2 });
            if (f && /\\/api\\/system\\/restart/.test(u)) return json({ ok: true }, 202);
            if (f && f.put && m === 'PUT' && /\\/api\\/system\\/terminal-backend$/.test(u)) return json({ saved: 'pty', running: 'tmux', can_restart: true });
            if (f && /end-leftover/.test(u)) { f.ended = true; return json({ ended: 2, left: 0 }); }
            if (f && /notice-seen/.test(u)) { f.seen = true; return json({ ok: true }); }
            return inner(url, opts);
        };
        return true; })()`);
    const settingsOpen = () => ev(`!!document.getElementById('settings-modal') && getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`);
    const row = () => ev(`(() => { const r = document.getElementById('settings-terminal-backend');
        return { hidden: r.hidden, now: document.getElementById('tb-now').textContent, choice: !document.getElementById('tb-choice').hidden,
            fixed: !document.getElementById('tb-fixed').hidden, checked: (document.querySelector('input[name="terminal-backend"]:checked') || {}).value || null,
            pending: document.getElementById('tb-pending').textContent, pendingHiddenAttr: document.getElementById('tb-pending').hasAttribute('hidden'),
            texts: [...document.querySelectorAll('#settings-terminal-backend .tb-t')].map(x => x.textContent.trim()),
            ptyText: (document.querySelector('#tb-opt-pty + span .tb-d') || {}).textContent || '' }; })()`);
    const pick = v => ev(`(() => { const r = document.getElementById('tb-opt-${v}'); r.checked = true; r.dispatchEvent(new Event('change')); return true; })()`);
    const confirmState = () => ev(`({ shown: getComputedStyle(document.getElementById('confirm-modal')).display !== 'none', title: document.getElementById('confirm-modal-title').textContent,
        msg: document.getElementById('confirm-modal-message').textContent, yes: document.getElementById('confirm-modal-yes').textContent })`);
    // The real saved value, past the fake.
    const saved = () => ev(`(() => { const f = window.__tbFake; window.__tbFake = null; return fetch('/api/system/terminal-backend').then(r => r.json()).then(j => { window.__tbFake = f; return j.saved; }); })()`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message, .toast')].map(t => t.textContent.trim())`);
    const guard = await require('./helpers/settings_guard').guardSettings(); // the settings this suite changes are put back in its finally (#4849)
    try {
        await Page.enable();
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await open();
        await wrap();

        // Verify the native Windows row before the cross-platform stand-in cases.
        if (process.platform === 'win32') {
            await ev(`window.showSettingsModal(); true`);
            await until(`document.getElementById('settings-terminal-backend')?.hidden === false`, 'the native Windows backend row');
            const native = await row();
            check('Windows runs pty with no backend choice', !native.hidden && native.now === 'Running now: pty' && !native.choice && native.fixed, JSON.stringify(native));
            await ev(`window.hideSettingsModal(); true`);
        }

        // ── The row for an install that chose tmux (this server itself runs the pty default) ─────────
        await ev(`window.__tbFake = { status: () => ({ saved: 'tmux', running: 'tmux', next: 'tmux', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 }) }; window.showSettingsModal(); true`);
        await until(`document.getElementById('settings-terminal-backend')?.hidden === false`, 'the backend row');
        let r = await row();
        check('Settings shows the backend row: running tmux, tmux picked, both options', !r.hidden && r.now === 'Running now: tmux' && r.choice && !r.fixed && r.checked === 'tmux'
            && r.texts[0] === "tmux: agents keep running when you quit Coral, and can reach each other's terminals" && r.texts[1] === 'pty: agents stop when you quit Coral and wake next time (default)', JSON.stringify(r));
        { // the pty title says its agents wake next time only while Wake agents after a restart is on (#6263); a pty Coral with the choice, where the wake box is shown
            await ev(`window.__tbFake = { status: () => ({ saved: 'pty', running: 'pty', next: 'pty', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 }) };
                import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.loadTerminalBackendRow()).then(() => true)`);
            await until(`document.getElementById('settings-terminal-backend')?.hidden === false && !document.getElementById('tb-autowake').hidden`, 'the wake box');
            const wakes = () => ev(`(() => { const words = n => n.nodeType === 3 ? n.nodeValue : (n.hidden ? '' : [...n.childNodes].map(words).join('')); return words(document.querySelectorAll('#settings-terminal-backend .tb-t')[1]).replace(/\\s+/g, ' ').trim(); })()`); // the words not under a hidden element (innerText is the whole text while the pane is not drawn)
            const tick = on => ev(`(() => { const b = document.getElementById('tb-autowake-on'); b.checked = ${on}; b.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            await tick(true);
            check('with Wake agents after a restart on, the pty title says they wake next time', (await wakes()) === 'pty: agents stop when you quit Coral and wake next time (default)', await wakes());
            await tick(false);
            check('with it off, the pty title no longer says they wake', (await wakes()) === 'pty: agents stop when you quit Coral (default)', await wakes());
            await tick(true);
            // back to the tmux install the checks below run on
            await ev(`window.__tbFake = { status: () => ({ saved: 'tmux', running: 'tmux', next: 'tmux', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 }) };
                import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.loadTerminalBackendRow()).then(() => true)`);
            await until(`document.getElementById('tb-now').textContent === 'Running now: tmux'`, 'the tmux row again');
        }
        check("the pty text has Security's crash wording", /puts them to sleep first when it stops cleanly, and marks them asleep at the next start after a crash/.test(r.ptyText), r.ptyText);
        check('nothing pending to start with', r.pending === '', r.pending);
        await pick('pty');
        r = await row();
        check('picking pty says it switches when Coral restarts', r.pending === 'Switches to pty when Coral restarts. Apply asks first.', r.pending);

        // ── Apply, then Cancel: nothing saved, Settings stays open ──────────
        await ev(`window.__tbFake = { status: () => ({ saved: 'tmux', running: 'tmux', next: 'tmux', forced: false, choice: true, restart_required: false, can_restart: true, running_agents: 2, leftover_tmux_agents: 0 }) }; true`);
        await ev(`window.showSettingsModal(); true`);
        await until(`document.getElementById('tb-now').textContent === 'Running now: tmux'`, 'the row again');
        await pick('pty');
        await ev(`window.__tbCalls.length = 0; window.applySettings(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm');
        let c = await confirmState();
        check('Apply asks: "Switch to pty?", says 2 agents end and are put to sleep first', c.shown && c.title === 'Switch to pty?' && /ends the 2 running agents\. Coral puts them to sleep first/.test(c.msg)
            && c.yes === 'Sleep 2 agents and restart on pty', JSON.stringify(c));
        await ev(`window.hideConfirmModal(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display === 'none'`, 'the confirm closed');
        const afterCancel = { open: await settingsOpen(), saved: await saved(), calls: await ev(`window.__tbCalls.filter(c => !c.startsWith('GET'))`), yes: (await confirmState()).yes };
        check('Cancel: nothing slept or saved, Settings stays open, the confirm button label is restored', afterCancel.open && afterCancel.saved === '' && afterCancel.calls.length === 0 && afterCancel.yes !== 'Sleep 2 agents and restart on pty', JSON.stringify(afterCancel));

        // ── Apply and confirm, on this Coral (it can't restart itself): sleep first, then save ──
        await ev(`window.__tbCalls.length = 0; window.applySettings(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm again');
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`window.__tbCalls.some(c => c.startsWith('PUT'))`, 'the save');
        await until(`(!document.getElementById('settings-modal') || getComputedStyle(document.getElementById('settings-modal')).display === 'none')`, 'Settings closed');
        let calls = await ev(`window.__tbCalls.filter(c => !c.startsWith('GET'))`);
        check('confirming puts the agents to sleep BEFORE saving pty',
            JSON.stringify(calls) === JSON.stringify(['POST /api/sessions/live/sleep-all', 'PUT /api/system/terminal-backend {"backend":"pty"}']), JSON.stringify(calls));
        check('the setting is saved on the server (for the next start)', await saved() === 'pty');
        const t = await toasts();
        check("where Coral can't restart itself, it says to quit and start it again; Settings closes",
            t.some(x => /quit Coral and start it again to switch to pty/.test(x)) && !(await settingsOpen()), JSON.stringify(t));
        await ev(`(() => { const f = window.__tbFake; window.__tbFake = null; return fetch('/api/system/terminal-backend', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ backend: 'tmux' }) }).then(() => { window.__tbFake = f; return true; }); })()`);

        // ── Where Coral can restart itself: the restart follows the save ────
        await ev(`window.__tbFake.put = true; window.showSettingsModal(); true`);
        await until(`document.getElementById('tb-now').textContent === 'Running now: tmux'`, 'the row once more');
        await pick('pty');
        await ev(`window.__tbCalls.length = 0; window.applySettings(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the confirm once more');
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`window.__tbCalls.some(c => c.startsWith('POST /api/system/restart'))`, 'the restart');
        calls = await ev(`window.__tbCalls.filter(c => !c.startsWith('GET'))`);
        check('where it can, Coral restarts after the save: sleep, save, restart',
            JSON.stringify(calls) === JSON.stringify(['POST /api/sessions/live/sleep-all', 'PUT /api/system/terminal-backend {"backend":"pty"}', 'POST /api/system/restart']), JSON.stringify(calls));
        await ev(`delete window.__tbFake.put; true`);

        // ── No choice here: pty fixed ──────────────────────────────────────
        await ev(`window.__tbFake = { status: { saved: '', running: 'pty', forced: false, choice: false, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0 } }; window.showSettingsModal(); true`);
        await until(`document.getElementById('tb-now').textContent === 'Running now: pty'`, 'the fixed row');
        r = await row();
        check('with no choice (Windows, no tmux) the row shows pty as fixed, no radios', !r.hidden && !r.choice && r.fixed && r.now === 'Running now: pty', JSON.stringify(r));
        await ev(`window.hideSettingsModal(); true`);

        // ── The built-in terminal became the default: one line, once, for an install that ran on the old default (#6064) ──
        await ev(`window.__tbFake = { status: () => ({ saved: '', running: 'pty', next: 'pty', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: 0, default_notice: !window.__tbFake.seen }) };
            import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.checkTerminalNotices()).then(() => true)`);
        await until(`!document.getElementById('default-notice-banner').hidden`, 'the default notice');
        const notice = await ev(`document.getElementById('default-notice-banner').textContent.replace(/\\s+/g, ' ').trim()`);
        check('the line says agents now stop on quit and where to keep them running', notice === 'info Agents now stop when you quit Coral. To keep them running, choose tmux in Settings > Terminal. Got it', notice);
        await ev(`document.querySelector('#default-notice-banner .default-notice-link').click(); true`);
        await until(`getComputedStyle(document.getElementById('settings-modal')).display !== 'none'`, 'Settings from the link');
        await until(`!!document.getElementById('settings-pane-terminal') && !document.getElementById('settings-pane-terminal').hidden`, 'the Terminal tab');
        check('its link opens Settings on the Terminal tab', await ev(`!!document.getElementById('settings-pane-terminal') && !document.getElementById('settings-pane-terminal').hidden && getComputedStyle(document.getElementById('settings-pane-terminal')).display !== 'none'`));
        await ev(`window.hideSettingsModal(); window.__tbCalls.length = 0; document.getElementById('default-notice-dismiss').click(); true`);
        await until(`document.getElementById('default-notice-banner').hidden`, 'the notice dismissed');
        check('after Got it, focus is on the Agents search box, not the page body', await ev(`document.activeElement && document.activeElement.id === 'agent-search-input'`), await ev(`document.activeElement ? document.activeElement.tagName + '#' + document.activeElement.id : 'none'`));
        check('Got it is recorded on the server (not in the browser) and the line goes', await ev(`window.__tbFake.seen === true && window.__tbCalls.includes('POST /api/system/terminal-backend/notice-seen')`), JSON.stringify(await ev(`window.__tbCalls`)));
        await ev(`import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.checkTerminalNotices()).then(() => true)`);
        check('once dismissed it is not shown again', await ev(`document.getElementById('default-notice-banner').hidden`));

        // ── Under pty, agents left in the old tmux server ───────────────────
        await ev(`window.__tbFake = { status: () => ({ saved: 'pty', running: 'pty', forced: false, choice: true, restart_required: false, can_restart: false, running_agents: 0, leftover_tmux_agents: window.__tbFake.ended ? 0 : 2 }) };
            import((window.__coralStatic || '/static/') + 'terminal_backend.js').then(m => m.checkLeftoverTmux()).then(() => true)`);
        await until(`!document.getElementById('leftover-tmux-banner').hidden`, 'the banner');
        const banner = await ev(`document.getElementById('leftover-tmux-banner').textContent.replace(/\\s+/g, ' ').trim()`);
        check("the banner counts them and says a wake is refused until they're ended", /2 agents are still running in Coral's old tmux server\./.test(banner) && /Waking one of them is refused until they are ended, so no second copy starts\./.test(banner), banner);
        await ev(`document.getElementById('leftover-tmux-end').click(); true`);
        await until(`getComputedStyle(document.getElementById('confirm-modal')).display !== 'none'`, 'the End them confirm');
        c = await confirmState();
        check('End them asks first', c.yes === 'End them' && /stop at once/.test(c.msg), JSON.stringify(c));
        await ev(`document.getElementById('confirm-modal-yes').click(); true`);
        await until(`document.getElementById('leftover-tmux-banner').hidden`, 'the banner gone');
        check('confirmed, they are ended and the banner goes', await ev(`document.getElementById('leftover-tmux-banner').hidden && window.__tbFake.ended === true`));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        try { await ev(`fetch('/api/system/terminal-backend', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ backend: 'tmux' }) }).then(() => true)`); } catch { /* best effort */ }
        await guard.restore().catch(e => console.error('settings restore: ' + (e && e.message)));
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
