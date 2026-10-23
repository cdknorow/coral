// Every dialog closes by its Close/Cancel control and by Escape, and focus goes back to the control that opened it (#5705; the Team settings Close was bound to the
// first header button, Rename board, #5681). One table: how each dialog opens and where it is. A dialog that cannot be opened here is listed in SKIPPED with its reason.
// CAPTURE=1 prints the table instead of asserting. Drives Chrome over CDP against the isolated dev server (agent_view_harness.js).
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const { removeBoards } = require('./helpers/board_cleanup');
const BASE = require('./test_base').BASE;

const MODAL = id => ({ root: `#${id}`, isOpen: `(() => { const m = document.getElementById('${id}'); return !!m && getComputedStyle(m).display !== 'none'; })()` });
const DIALOGS = {
    'launch-modal': { ...MODAL('launch-modal'), open: `window.showLaunchModal()` },
    'info-modal': { ...MODAL('info-modal'), open: `__M('modals.js').then(async () => { (await __M('state.js')).state.currentSession = { type: 'live', name: 'a', session_id: 'a', agent_type: 'claude' }; return (await __M('info_dialog.js')).showInfoModal(); })` },
    'resume-modal': { ...MODAL('resume-modal'), open: `(async () => { (await __M('state.js')).state.currentSession = { type: 'history', name: 'a', session_id: 'a', agent_type: 'claude' }; return (await __M('resume_dialog.js')).showResumeModal(); })()` },
    'settings-modal': { ...MODAL('settings-modal'), open: `window.showSettingsModal()` },
    'macro-modal': { ...MODAL('macro-modal'), open: `__M('controls.js').then(m => m.showMacroModal())` },
    'alert-modal': { ...MODAL('alert-modal'), open: `window.showAlertModal('Title', 'Message')` },
    'confirm-modal': { ...MODAL('confirm-modal'), open: `window.showConfirmModal('Title', 'Message', () => {})` },
    'prompt-modal': { ...MODAL('prompt-modal'), open: `window.showPromptModal('Title', 'Label', 'value', () => {})` },
    'open-folder-modal': { ...MODAL('open-folder-modal'), open: `window.showOpenFolderModal()` },
    'job-modal': { ...MODAL('job-modal'), open: `window.showJobModal()` },
    'default-prompts-modal': { ...MODAL('default-prompts-modal'), open: `window.showDefaultPromptsModal()` },
    'create-task-modal': { ...MODAL('create-task-modal'), open: `window.showCreateTaskModal()` },
    'team-agent-modal': { ...MODAL('team-agent-modal'), open: `(async () => { await __M('modals.js'); await __M('team_dialogs.js'); window.showTeamAgentModal(); })()` },
    'team-wizard-modal': { ...MODAL('team-wizard-modal'), open: `(async () => { await __M('modals.js'); await __M('team_wizard.js'); window.showTeamWizardModal(); })()` },
    'mobile-connect-modal': { ...MODAL('mobile-connect-modal'), open: `(async () => { await __M('modals.js'); await window._showMobileConnectModal(); })()` },
    'wf-create-modal': { ...MODAL('wf-create-modal'), open: `window.showWorkflowCreateModal()` },
    // Built by script (registerDialog) or native <dialog>: found by what they hold, not by an id in the page shell.
    'cli-not-found (script)': { root: '.modal:has(button[data-action=close])', isOpen: `!!document.querySelector('.modal:has(button[data-action=close])')`, open: `__M('cli_dialogs.js').then(m => m._showCLINotFoundModal('claude'))` },
    'permission-flag (script)': { root: '.modal:has(button[data-action=enable])', isOpen: `!!document.querySelector('.modal:has(button[data-action=enable])')`, open: `__M('cli_dialogs.js').then(m => { window.__pf = undefined; m._checkPermissionFlag('', 'claude').then(v => { window.__pf = v === null ? 'null' : v; }); })`, resolves: `window.__pf === 'null'` },
    'template-browser (script)': { root: '#template-browser-modal', isOpen: `!!document.getElementById('template-browser-modal')`, open: `__M('template_browser.js').then(m => { m.showTemplateBrowser('agents', () => {}); })` },
    'team-settings (native)': { root: '#team-working-mode-dialog', isOpen: `!!document.querySelector('#team-working-mode-dialog')?.open`, open: `__M('team_working_mode.js').then(m => m.showTeamWorkingMode('census-team', { workspace: false }))` },
    'team-view (native)': { root: '#team-availability-dialog', isOpen: `!!document.querySelector('#team-availability-dialog')?.open`, open: `__M('team_availability.js').then(m => m.showTeamAvailability('census-team'))` },
};

// Not opened here, and why (they are in modal_chrome.js's table and close by the same rules): the restart dialog opens from a live session through a function
// that is not exported; the board export dialog needs a selected board with subscribers.
const SKIPPED = { 'restart-modal': 'needs a live session; opened by an unexported function', 'mb-export-modal': 'needs a selected board with subscribers' };

// The dialogs a person opens from a real control on a screen that needs no data, and how: `prep` gets the page to that screen, `steps` are the controls pressed in turn (the last one opens the dialog, and is the
// one focus must come back to, or to the control that opened the menu it was in). Each runs again with a real mouse click on the real opener (#6188: the census's own opener cannot see a real opener losing focus).
const REAL = {
    'launch-modal': { prep: `window.switchNavTab('agents')`, steps: ['[data-on-click="launch-show"]'] },
    'open-folder-modal': { prep: `window.switchNavTab('agents')`, steps: ['#welcome-open-folder'] },
    'settings-modal': { prep: `window.switchNavTab('agents')`, steps: ['[aria-controls="top-bar-settings-menu"]', '[data-on-click="m-close-top-bar-settings-then-show-settings-modal"]'], returnsTo: 0 },
    'wf-create-modal': { prep: `window.switchNavTab('workflows')`, steps: ['[data-on-click="wf-create"]'] },
    'job-modal': { prep: `window.switchNavTab('jobs')`, steps: ['[data-on-click="job-new"], [data-on-click="m-show-job-modal"], #scheduler-new-job-btn'] },
};

(async () => {
    const { client, Page, Emulation, ev, waitFor, PRELOAD, install } = await connect();
    const { Input } = client;
    const key = async (k, code, vk) => { await Input.dispatchKeyEvent({ type: 'keyDown', key: k, code, windowsVirtualKeyCode: vk }); await Input.dispatchKeyEvent({ type: 'keyUp', key: k, code, windowsVirtualKeyCode: vk }); };
    const fresh = async () => {
        await Emulation.setDeviceMetricsOverride({ width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
        await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
        await ev(`window.__coralReady.startup('liveList', 'settings')`);
        await ev(`(() => { window.__M = f => import((window.__coralStatic || '/static/') + f); const t = document.createElement('button'); t.id = 'opener'; /* z-index 5: above the sidebar's pinned New Agent footer (2) that sits in this corner (#6159) */ t.textContent = 'opener'; t.style.cssText = 'position:fixed;left:4px;bottom:4px;z-index:5'; document.body.append(t); })(); true`);
    };
    // The control a person would press to close it: a visible button named Close, Cancel, Done or ×, or the close button class.
    const CLOSER = root => `(() => { const m = document.querySelector('${root}'); const vis = e => e.getClientRects().length > 0 && getComputedStyle(e).visibility !== 'hidden';
        const bs = [...m.querySelectorAll('button')].filter(vis); return bs.find(b => b.classList.contains('modal-close-btn')) || bs.find(b => /^(close|cancel|done|ok|×|✕)$/i.test((b.textContent || b.getAttribute('aria-label') || '').trim())) || null; })()`;
    const rows = []; let failed = 0;
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        await client.Emulation.setFocusEmulationEnabled({ enabled: true });
        for (const [name, d] of Object.entries(DIALOGS)) {
            const row = { name };
            for (const how of ['button', 'escape']) {
                await fresh();
                try {
                    const r = await ev(`(() => { const b = document.getElementById('opener').getBoundingClientRect(); return [b.x + b.width / 2, b.y + b.height / 2]; })()`); // a real click: what opens most dialogs, and what records the opener (focusin)
                    await Input.dispatchMouseEvent({ type: 'mousePressed', x: r[0], y: r[1], button: 'left', clickCount: 1 }); await Input.dispatchMouseEvent({ type: 'mouseReleased', x: r[0], y: r[1], button: 'left', clickCount: 1 });
                    await ev(`(async () => { await (${d.open}); return true; })()`);
                    await waitFor(d.isOpen, `${name} opens`, 8000);
                } catch (e) { row[how] = 'cannot open: ' + String(e.message).slice(0, 60); continue; }
                let selector = '';
                if (how === 'button') {
                    selector = await ev(`(() => { const b = ${CLOSER(d.root)}; if (!b) return ''; const s = b.id ? '#' + b.id : b.getAttribute('data-on-click') ? '[data-on-click=' + b.getAttribute('data-on-click') + ']' : b.className; b.click(); return s || 'button'; })()`);
                    row.control = selector || 'NONE';
                    if (!selector) { row[how] = 'no close control'; continue; }
                } else await key('Escape', 'Escape', 27);
                const closed = await waitFor(`!(${d.isOpen})`, `${name} closes`, 4000).then(() => true, () => false);
                const focused = closed && await waitFor(`document.activeElement && document.activeElement.id === 'opener'`, `${name} focus`, 2000).then(() => true, () => false);
                if (closed && d.resolves) { const settled = await waitFor(d.resolves, `${name} resolves its promise`, 2000).then(() => true, () => false); if (!settled) { row[how] = 'closed, its promise never resolved'; continue; } }
                row[how] = closed ? (focused ? 'ok' : 'closed, focus lost to ' + await ev(`(document.activeElement && (document.activeElement.id || document.activeElement.tagName)) || 'none'`)) : 'STAYS OPEN';
            }
            rows.push(row);
            const rl = REAL[name];
            if (!rl) continue;
            const rrow = { name: name + ' (real opener)' };
            for (const how of ['button', 'escape']) {
                await fresh();
                try {
                    await ev(`(async () => { await (${rl.prep}); return true; })()`);
                    await new Promise(r => setTimeout(r, 600)); // sleep-ok: the screen the opener is on settles
                    for (let i = 0; i < rl.steps.length; i++) {
                        const sel = rl.steps[i];
                        const at = await ev(`(() => { const e = [...document.querySelectorAll(${JSON.stringify(sel)})].find(e => e.getClientRects().length && !e.closest('[hidden]')); if (!e) return null; e.scrollIntoView({ block: 'center' }); document.querySelectorAll('[data-census-real]').forEach(x => x.removeAttribute('data-census-real')); e.setAttribute('data-census-real', ''); const b = e.getBoundingClientRect(); return [b.x + b.width / 2, b.y + b.height / 2]; })()`);
                        if (!at) throw new Error('no visible ' + sel);
                        await Input.dispatchMouseEvent({ type: 'mousePressed', x: at[0], y: at[1], button: 'left', clickCount: 1 }); await Input.dispatchMouseEvent({ type: 'mouseReleased', x: at[0], y: at[1], button: 'left', clickCount: 1 });
                        if (i === (rl.returnsTo ?? rl.steps.length - 1) && rl.returnsTo !== undefined) await ev(`document.querySelector('[data-census-real]').setAttribute('data-census-return', ''); true`);
                        await new Promise(r => setTimeout(r, 300)); // sleep-ok: a menu opens before its item is pressed
                    }
                    await waitFor(d.isOpen, `${name} opens from its real opener`, 8000);
                } catch (e) { rrow[how] = 'cannot open: ' + String(e.message).slice(0, 80); continue; }
                if (how === 'button') {
                    const selector = await ev(`(() => { const b = ${CLOSER(d.root)}; if (!b) return ''; b.click(); return 'button'; })()`);
                    if (!selector) { rrow[how] = 'no close control'; continue; }
                } else await key('Escape', 'Escape', 27);
                const closed = await waitFor(`!(${d.isOpen})`, `${name} closes`, 4000).then(() => true, () => false);
                const target = rl.returnsTo !== undefined ? '[data-census-return]' : '[data-census-real]';
                const focused = closed && await waitFor(`document.activeElement && document.activeElement.matches('${target}')`, `${name} focus`, 2000).then(() => true, () => false);
                rrow[how] = closed ? (focused ? 'ok' : 'closed, focus lost to ' + await ev(`(document.activeElement && (document.activeElement.id || document.activeElement.tagName)) || 'none'`)) : 'STAYS OPEN';
            }
            rows.push(rrow);
        }
        if (process.env.CAPTURE_OUT) require('node:fs').writeFileSync(process.env.CAPTURE_OUT, rows.map(r => `${r.name.padEnd(24)} control=${String(r.control).padEnd(44)} button: ${r.button}; escape: ${r.escape}`).join('\n') + '\n');
        for (const r of rows) console.log(`${r.name.padEnd(24)} control=${String(r.control).padEnd(40)} button: ${r.button}; escape: ${r.escape}`);
        if (!process.env.CAPTURE) {
            for (const r of rows) for (const how of ['button', 'escape']) if (r[how] !== 'ok') { failed++; console.error(`FAIL ${r.name} by ${how}: ${r[how]}`); }
            assert.equal(failed, 0, `${failed} dialog closes failed`);
            assert.ok(rows.length >= 20, `the census covers ${rows.length} dialogs (${Object.keys(SKIPPED).length} skipped)`);
        }
    } finally { await client.close(); }
})().catch(e => { console.error(e); process.exit(1); });
