// CSP stage 2, slice 7b (#3645): the dialogs in modals.html on delegated data-on-* actions.
//  - Census: every data-on-* element in the loaded page names a registered action (a typo or a
//    missing registration would leave a dead control).
//  - The dialogs: Close buttons close their dialog; a backdrop closes it but a click inside doesn't;
//    the launch dialog's type buttons switch the type (an unlisted type does nothing); the job
//    dialog's type switch; a folder browser toggles; Enter in the open-folder input submits; the
//    team dialog's worktree checkbox shows its branch row.
//  - Restart's confirm button is on the shared destructive list.
//  - No inline handler is left in modals.html's dialogs.
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
    const shown = id => `(() => { const m = document.getElementById(${JSON.stringify(id)}); return !!m && getComputedStyle(m).display !== 'none'; })()`;
    try {
        await Page.enable();
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // ── Census ─────────────────────────────────────────────────────────
        const census = await ev(`import(${MOD} + 'actions.js').then(a => {
            const names = new Set();
            for (const el of document.querySelectorAll('*')) for (const at of el.attributes) if (at.name.startsWith('data-on-')) names.add(at.value);
            return { count: names.size, missing: [...names].filter(n => !a.hasAction(n)) };
        })`);
        check('every data-on-* control in the page names a registered action', census.count > 50 && census.missing.length === 0, JSON.stringify(census));

        // ── Close buttons, backdrops, Enter ────────────────────────────────
        await ev(`window.showOpenFolderModal(); true`);
        await until(shown('open-folder-modal'), 'the open folder dialog');
        const inside = await ev(`(() => { document.querySelector('#open-folder-modal .modal-content').click(); return ${shown('open-folder-modal')}; })()`);
        await ev(`document.getElementById('open-folder-modal').dispatchEvent(new MouseEvent('click', { bubbles: true })); true`);
        check('the open-folder dialog: a click inside keeps it, its backdrop closes it', inside && !(await ev(shown('open-folder-modal'))));
        await ev(`window.showOpenFolderModal(); true`);
        await until(shown('open-folder-modal'), 'the open folder dialog again');
        const submitted = await ev(`(() => { let n = 0; const keep = window.submitOpenFolder; window.submitOpenFolder = () => { n++; };
            const i = document.querySelector('#open-folder-modal [data-on-keydown="m-open-folder-key"]'); i.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true }));
            i.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })); window.submitOpenFolder = keep; return n; })()`);
        check('Enter in the open-folder input submits it (another key doesn’t)', submitted === 1, String(submitted));
        await ev(`document.querySelector('#open-folder-modal [data-on-click="m-hide-open-folder-modal"]').click(); true`);
        check('its Cancel closes it', !(await ev(shown('open-folder-modal'))));

        // ── The launch dialog's types ──────────────────────────────────────
        await ev(`window.showLaunchModal(); true`);
        await until(shown('launch-modal'), 'the launch dialog');
        const types = await ev(`(() => { const calls = []; const keep = window._selectLaunchType; window._selectLaunchType = t => calls.push(t);
            for (const b of document.querySelectorAll('#launch-modal [data-on-click="m-select-launch-type"]')) b.click();
            const bad = document.createElement('button'); bad.setAttribute('data-on-click', 'm-select-launch-type'); bad.dataset.arg = 'root'; document.body.appendChild(bad); bad.click(); bad.remove();
            window._selectLaunchType = keep; return calls; })()`);
        check('the launch dialog’s type buttons pass their type; an unlisted type does nothing',
            ['agent', 'terminal', 'team', 'back'].every(t => types.includes(t)) && !types.includes('root'), JSON.stringify(types));
        const browser = await ev(`(() => { const calls = []; const keep = window.toggleBrowser; window.toggleBrowser = (...a) => calls.push(a);
            document.querySelector('#launch-modal [data-on-click="m-toggle-browser"]').click(); window.toggleBrowser = keep; return calls; })()`);
        check('a folder browser button toggles its browser (its four ids)', browser.length === 1 && browser[0].length === 4 && browser[0].every(x => /^[a-z0-9-]+$/.test(x)), JSON.stringify(browser));
        const wt = await ev(`(() => { const c = document.querySelector('[data-on-change="m-team-worktree"]'); const r = document.getElementById('team-worktree-branch-row'); if (!c || !r) return null;
            c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); const on = r.style.display; c.checked = false; c.dispatchEvent(new Event('change', { bubbles: true })); return { on, off: r.style.display }; })()`);
        check('the team worktree checkbox shows and hides its branch row', wt && wt.on === '' && wt.off === 'none', JSON.stringify(wt));
        await ev(`document.querySelector('#launch-modal [data-on-click="m-hide-launch-modal"]').click(); true`);
        check('the launch dialog’s Cancel closes it', !(await ev(shown('launch-modal'))));

        // ── The job dialog's type switch ───────────────────────────────────
        await ev(`window.showJobModal(); true`);
        await until(shown('job-modal'), 'the job dialog');
        const jt = await ev(`(() => { const calls = []; const keep = window.switchJobType; window.switchJobType = t => calls.push(t);
            for (const b of document.querySelectorAll('#job-modal [data-on-click="m-switch-job-type"]')) b.click(); window.switchJobType = keep; return calls; })()`);
        check('the job dialog’s type switch passes its type', JSON.stringify(jt.sort()) === JSON.stringify(['prompt', 'workflow']), JSON.stringify(jt));
        await ev(`document.querySelector('#job-modal [data-on-click="m-hide-job-modal"]').click(); true`);
        check('the job dialog’s Cancel closes it', !(await ev(shown('job-modal'))));

        const listed = await ev(`import(${MOD} + 'actions.js').then(a => a.DESTRUCTIVE_ACTIONS.has('m-confirm-restart'))`);
        check('the restart dialog’s confirm button is on the shared destructive list', listed);

        const inline = await ev(`[...document.querySelectorAll('.modal *, .modal')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => (el.id || el.className) + ' ' + a.name)).slice(0, 8)`);
        check('no inline handler left in the dialogs', inline.length === 0, JSON.stringify(inline));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
