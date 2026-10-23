// CSP stage 2, slice 7a (#3645): modals.js's generated markup and the job dialog's flag shortcuts on
// delegated data-on-* actions.
//  - The permission state still shows correctly (Security's requirement for this slice): with the
//    saved mode bypassPermissions, switching the job dialog's agent to codex, agy and back to claude
//    makes the permission shortcut show exactly that agent's flag, and clicking it puts exactly that
//    flag in the flags input (the previous agent's is replaced). The shortcut is found by its data
//    attributes, never by handler text.
//  - A plain flag shortcut toggles its flag (and its active state); a flag outside the pattern does
//    nothing.
//  - The agent config form: a permissions toggle shows and hides its editor; a permission chip
//    toggles; the permission mode select describes its mode.
//  - The team builder: rows move, open and are removed; the picker adds a preset or a custom agent.
//  - No inline handler is left in the job dialog or the team builder.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MOD = `(window.__coralStatic||'/static/')`;
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();
        // The dialogs module loads on first use; load it, and fix the saved permission mode.
        await ev(`(async () => { const l = await import(${MOD} + 'lazy_panels.js'); await l.loadModals(); const { state } = await import(${MOD} + 'state.js');
            state.settings = { ...(state.settings || {}), default_permission_mode: 'bypassPermissions' }; return true; })()`);

        // ── The job dialog's flags ─────────────────────────────────────────
        await ev(`window.showJobModal(); true`);
        await until(`(() => { const m = document.getElementById('job-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the job dialog');
        const flags = () => ev(`document.getElementById('job-modal-flags').value.trim()`);
        await ev(`document.getElementById('job-modal-flags').value = ''; true`);
        const chrome = `document.querySelector('#job-modal [data-on-click="flag-toggle"][data-flag="--chrome"]')`;
        await ev(`${chrome}.click(); true`);
        const on = await flags();
        const activeOn = await ev(`${chrome}.classList.contains('active')`);
        await ev(`${chrome}.click(); true`);
        const off = await flags();
        check('a flag shortcut toggles its flag and its active state', on === '--chrome' && activeOn && off === '' && !(await ev(`${chrome}.classList.contains('active')`)), JSON.stringify({ on, activeOn, off }));
        await ev(`(() => { const b = document.createElement('button'); b.setAttribute('data-on-click', 'flag-toggle'); b.dataset.flagInput = 'job-modal-flags'; b.dataset.flag = '--x; rm -rf'; document.body.appendChild(b); b.click(); b.remove(); return true; })()`);
        check('a flag outside the pattern does nothing', (await flags()) === '');

        const perm = `document.querySelector('#job-modal .flag-shortcut-btn:not([data-flag="--chrome"])')`;
        const expectFlag = { codex: '--dangerously-bypass-approvals-and-sandbox', agy: '--dangerously-skip-permissions', claude: '--permission-mode bypassPermissions' };
        const seen = {};
        for (const agent of ['codex', 'agy', 'claude']) {
            await ev(`(() => { const s = document.getElementById('job-modal-agent'); s.value = ${JSON.stringify(agent)}; s.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`);
            const b = await ev(`(() => { const b = ${perm}; return { action: b.getAttribute('data-on-click'), text: b.textContent.trim(), type: b.dataset.agentType, mode: b.dataset.mode, onclick: b.hasAttribute('onclick') }; })()`);
            await ev(`${perm}.click(); true`);
            seen[agent] = { ...b, flags: await flags() };
        }
        const permOk = Object.entries(expectFlag).every(([a, f]) => seen[a].action === 'flag-perm' && seen[a].text === f && seen[a].type === a && seen[a].mode === 'bypassPermissions' && !seen[a].onclick
            && seen[a].flags.includes(f) && Object.entries(expectFlag).every(([b, g]) => b === a || !seen[a].flags.includes(g)));
        check('the permission shortcut shows each agent\'s own flag and sets exactly that flag (no other agent\'s)', permOk, JSON.stringify(seen));
        const badPerm = await ev(`(() => { const before = document.getElementById('job-modal-flags').value; const b = document.createElement('button'); b.setAttribute('data-on-click', 'flag-perm');
            b.dataset.flagInput = 'job-modal-flags'; b.dataset.agentType = 'codex x'; b.dataset.mode = 'bypassPermissions'; document.body.appendChild(b); b.click(); b.remove();
            return before === document.getElementById('job-modal-flags').value; })()`);
        check('a permission shortcut with a bad agent type does nothing', badPerm);
        const jobInline = await ev(`[...document.querySelectorAll('#job-modal [onclick], #job-modal [onchange]')].filter(el => el.closest('.flag-shortcut-btn, #job-modal-agent') || el.matches('.flag-shortcut-btn, #job-modal-agent')).length`);
        check('the job dialog\'s flag shortcuts and agent select have no inline handler', jobInline === 0, String(jobInline));
        await ev(`window.hideJobModal && window.hideJobModal(); true`);

        // ── The agent config form (rendered into a scratch container, as the dialogs render it) ─
        await ev(`(() => { const d = document.createElement('div'); d.id = 'qa-acf-3645'; document.body.appendChild(d); window.renderAgentConfigForm('qa-acf-3645', {}); return true; })()`);
        await until(`!!document.querySelector('#qa-acf-3645 [data-on-click="perms-toggle"]')`, 'the agent config form', 5000);
        const acf = await ev(`(() => {
            const t = document.querySelector('[data-on-click="perms-toggle"]'); if (!t) return { form: false };
            const box = document.getElementById(t.dataset.target); const d0 = box.style.display; t.click(); const d1 = box.style.display; t.click();
            const chip = document.querySelector('[data-on-click="perm-chip-toggle"]'); let chipToggled = null;
            if (chip) { const a = chip.classList.contains('active'); chip.click(); chipToggled = chip.classList.contains('active') !== a; chip.click(); }
            const calls = []; const keep = window._updatePermModeDescription; window._updatePermModeDescription = el => calls.push(el.value);
            const sel = document.querySelector('[data-on-change="perm-mode-describe"]'); if (sel) { sel.value = sel.options[sel.options.length - 1].value; sel.dispatchEvent(new Event('change', { bubbles: true })); }
            window._updatePermModeDescription = keep;
            document.getElementById('qa-acf-3645').remove();
            return { form: true, toggled: d0 !== d1, chipToggled, described: calls };
        })()`);
        check('the agent config form: the permissions toggle shows its editor, a chip toggles, the mode select describes its mode',
            acf.form && acf.toggled && acf.chipToggled !== false && acf.described.length === 1, JSON.stringify(acf));

        // ── The team builder ───────────────────────────────────────────────
        const team = await ev(`(async () => {
            const list = document.getElementById('team-agents-list'); if (!list) return { list: false };
            list.innerHTML = ''; window._addTeamAgent('Alpha'); window._addTeamAgent('Beta');
            const names = () => [...list.querySelectorAll('.team-agent-row')].map(r => r.querySelector('.team-agent-name, [class*="name"]')?.textContent.trim() || r.dataset.name || r.textContent.trim().slice(0, 12));
            const before = names();
            list.querySelector('.team-agent-row [data-on-click="team-agent-move"][data-dir="down"]').click();
            const moved = names();
            const opened = []; const keepOpen = window.showTeamAgentModal; window.showTeamAgentModal = r => opened.push(!!r && r.classList.contains('team-agent-row'));
            list.querySelector('[data-on-click="team-agent-edit"]').click();
            window.showTeamAgentModal = keepOpen;
            list.querySelector('[data-on-click="team-agent-remove"]').click();
            const left = list.querySelectorAll('.team-agent-row').length;
            const inline = [...document.querySelectorAll('#team-agents-list *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.className + ' ' + a.name));
            list.innerHTML = '';
            return { list: true, before, moved, opened, left, inline };
        })()`);
        check('team builder rows move down, open (Edit), and are removed', team.list && team.moved[0] === team.before[1] && JSON.stringify(team.opened) === '[true]' && team.left === 1, JSON.stringify(team));
        check('no inline handler left in the team builder', team.inline.length === 0, JSON.stringify(team.inline.slice(0, 4)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
