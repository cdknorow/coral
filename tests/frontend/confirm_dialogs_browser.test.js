// #3702: the prompts that used to be the browser's native window.confirm now use the dashboard's
// confirm dialog: closing a preview tab with unsaved edits ("Discard Changes") and leaving the team
// settings with unsaved edits ("Unsaved Team Settings"). The real dialog is used, not a stub. For each:
// it asks with the right wording, Cancel keeps state, and Yes acts. (The panel Delete prompt is covered
// by agent_ui.test.js, Delete Workflow by actions_workflows_browser.test.js.)
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const MOD = `(window.__coralStatic || '/static/')`;

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
    const sleep = ms => new Promise(r => setTimeout(r, ms));
    const dialogOpen = `(() => { const m = document.getElementById('confirm-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`;
    const dialogText = () => ev(`({ title: document.getElementById('confirm-modal-title').textContent, message: document.getElementById('confirm-modal-message').textContent })`);
    const cancel = () => ev(`document.querySelector('#confirm-modal .btn:not(#confirm-modal-yes)').click(); true`);
    const yes = () => ev(`document.getElementById('confirm-modal-yes').click(); true`);
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url);
                if (/\\/file-content\\?/.test(u) && (opts.method || 'GET').toUpperCase() === 'GET') return Promise.resolve(new Response(JSON.stringify({ content: 'hello world' }), { headers: { 'content-type': 'application/json' } }));
                return inner(url, opts);
            };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // ── Closing a preview tab with unsaved edits ───────────────────────
        await ev(`(async () => { const { state } = await import(${MOD} + 'state.js'); state.currentSession = { type: 'live', session_id: 'sid-3702', name: 'terminal-3702', agent_type: 'terminal' };
            window.__pp = await import(${MOD} + 'preview_pane.js'); return true; })()`);
        const tabsHtml = () => ev(`document.getElementById('preview-tabs')?.innerHTML || ''`);
        await ev(`window.__pp.openPreviewTab('/tmp/a-3702.txt', 0, { mode: 'edit' }); window.__pp.openPreviewTab('/tmp/b-3702.txt', 0, { mode: 'edit' }); true`);
        await until(`document.querySelectorAll('#preview-tabs .preview-tab').length === 2`, 'two tabs');
        // Wait for the editor to hold the file, then edit it (CodeMirror, or the fallback textarea).
        await until(`!!document.querySelector('.inline-preview-fallback-editor') || !!document.querySelector('#preview-body .cm-content')`, 'the editor', 15000);
        await ev(`(() => { const ta = document.querySelector('.inline-preview-fallback-editor');
            if (ta) { ta.value += ' edited'; ta.dispatchEvent(new Event('input', { bubbles: true })); return 'textarea'; }
            const view = window.CoralCM.EditorView.findFromDOM(document.querySelector('#preview-body .cm-editor'));
            view.dispatch({ changes: { from: view.state.doc.length, insert: ' edited' } }); return 'cm'; })()`);
        await until(`/preview-tab-name[^>]*>[^<]*•/.test(document.getElementById('preview-tabs').innerHTML)`, 'the tab marked as edited');
        const dirtyTab = await ev(`(() => { const t = [...document.querySelectorAll('#preview-tabs .preview-tab')].find(el => el.querySelector('.preview-tab-name').textContent.includes('•')); return t ? t.querySelector('.preview-tab-name').textContent : null; })()`);
        check('an edited tab is marked with a dot', !!dirtyTab, String(dirtyTab));
        const closeDirty = `(() => { const t = [...document.querySelectorAll('#preview-tabs .preview-tab')].find(el => el.querySelector('.preview-tab-name').textContent.includes('•')); t.querySelector('.preview-tab-close').click(); return true; })()`;
        const tabCount = () => ev(`document.querySelectorAll('#preview-tabs .preview-tab').length`);
        await ev(closeDirty);
        await until(dialogOpen, 'the discard confirmation');
        const asked = await dialogText();
        check('closing an edited tab asks first, naming the file', asked.title === 'Discard Changes' && /^Discard unsaved changes to [ab]-3702\.txt\?$/.test(asked.message), JSON.stringify(asked));
        check('...and the tab stays while the dialog is open', (await tabCount()) === 2);
        await cancel();
        await sleep(300); // sleep-ok: shows that Cancel closes nothing (no condition to wait for)
        check('Cancel closes the dialog and keeps the edited tab', !(await ev(dialogOpen)) && (await tabCount()) === 2);
        await ev(closeDirty);
        await until(dialogOpen, 'the discard confirmation again');
        await yes();
        await until(`document.querySelectorAll('#preview-tabs .preview-tab').length === 1`, 'the tab closed');
        check('Yes closes the edited tab', (await tabCount()) === 1);
        // A tab without edits closes with no question.
        await ev(`document.querySelector('#preview-tabs .preview-tab .preview-tab-close').click(); true`);
        await until(`!document.querySelector('#preview-tabs .preview-tab')`, 'the clean tab closed');
        check('a tab with no edits closes without asking', !(await ev(dialogOpen)) && (await tabCount()) === 0);

        // ── Leaving team settings with unsaved edits ───────────────────────
        await ev(`(async () => { window.__tw = await import(${MOD} + 'team_working_mode.js');
            const sec = document.createElement('section'); sec.id = 'team-settings-workspace';
            const form = document.createElement('form'); const input = document.createElement('input'); input.name = 'custom_instructions'; input.value = 'saved';
            form.append(input); sec.append(form); document.body.append(sec);
            const snap = () => JSON.stringify(Array.from(form.elements).filter(el => el.name).map(el => ({ name: el.name, type: el.type, value: el.value })));
            form.dataset.initialState = snap(); window.__form = { input, form }; return true; })()`);
        check('with nothing unsaved, leaving resolves true without a dialog', (await ev(`window.__tw.confirmTeamWorkspaceChange().then(v => v)`)) === true && !(await ev(dialogOpen)));
        await ev(`window.__form.input.value = 'edited'; window.__answer = null; window.__tw.confirmTeamWorkspaceChange().then(v => { window.__answer = v; }); true`);
        await until(dialogOpen, 'the unsaved-settings confirmation');
        const askedTeam = await dialogText();
        check('leaving with unsaved settings asks first', askedTeam.title === 'Unsaved Team Settings' && askedTeam.message === 'Team settings have unsaved changes. Leave without saving?', JSON.stringify(askedTeam));
        check('...and stays undecided while the dialog is open', (await ev(`window.__answer`)) === null);
        await cancel();
        await until(`window.__answer !== null`, 'the answer');
        check('Cancel resolves false (stay, edits kept)', (await ev(`window.__answer`)) === false && (await ev(`window.__form.input.value`)) === 'edited');
        await ev(`window.__answer = null; window.__tw.confirmTeamWorkspaceChange().then(v => { window.__answer = v; }); true`);
        await until(dialogOpen, 'the confirmation again');
        await yes();
        await until(`window.__answer !== null`, 'the answer again');
        check('Yes resolves true (leave)', (await ev(`window.__answer`)) === true);
        // Escape and a replaced dialog count as Cancel, so a navigation never hangs on an unanswered prompt.
        await ev(`window.__answer = null; window.__tw.confirmTeamWorkspaceChange().then(v => { window.__answer = v; }); true`);
        await until(dialogOpen, 'the confirmation a third time');
        await ev(`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); true`);
        await until(`window.__answer !== null`, 'the answer after Escape');
        check('Escape resolves false', (await ev(`window.__answer`)) === false);
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
