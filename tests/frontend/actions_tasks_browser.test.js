// CSP stage 2, slice 4b (#3645): the board's tasks on delegated data-on-* actions, against real
// tasks on a real board. Each moved action still does what its inline handler did:
//  - the list: sort headers (only a known sort), Hide done, a row opens its task;
//  - the dialog: Close, Edit then Cancel or Save, Cancel Task through its "Yes, Cancel" step (and
//    No, Go Back), Complete through its form (and Back), Publish a draft, Nudge / Remind / Stop
//    reminders on a claimed task, a dependency link opening that task;
//  - the dependency picker: + Add dependency, a checkbox making a tag, the tag's × removing it;
//  - a task's coral:// artifact link opens the preview (only an artifact URI);
//  - a bad id does nothing, and no inline handler is left in the list or the dialog.
'use strict';

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
const check = (label, ok, detail) => { results.push(!!ok); console.log(`[${ok ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' — ' + detail : ''}`); };
const BOARD = 'tasks-3645';

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime } = client;
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 400));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
const { removeBoards } = require('./helpers/board_cleanup');
    const until = (expr, what, ms = 8000) => waitFor(expr, what, { timeout: ms, soft: true });
    const MODS = `const tasks = await import((window.__coralStatic||'/static/')+'tasks.js'); const { state } = await import((window.__coralStatic||'/static/')+'state.js');`;
    try {
        await Page.enable();
        await Page.addScriptToEvaluateOnNewDocument({ source: `(() => {
            window.__req = [];
            const inner = window.fetch;
            window.fetch = (url, opts = {}) => {
                const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, '');
                if (/\\/api\\/board\\/[^/]+\\/tasks\\/\\d+\\//.test(u) || /\\/api\\/board\\/[^/]+\\/tasks\\/\\d+$/.test(u)) window.__req.push((opts.method || 'GET').toUpperCase() + ' ' + u.split('?')[0]);
                return inner(url, opts);
            };
        })();` });
        const loaded = Page.loadEventFired();
        await Page.navigate({ url: BASE + '/' });
        await loaded;
        await coralReady();

        // Real tasks: a dependency, an open task blocked by it, a draft, and one a subscriber claims.
        const made = await ev(`(async () => {
            const call = async (method, path, body) => { const r = await fetch('/api/board/${BOARD}' + path, { method, headers: { 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
                if (!r.ok) throw new Error(path + ': ' + r.status + ' ' + await r.text()); return r.json().catch(() => ({})); };
            await call('POST', '/subscribe', { subscriber_id: 'Worker', job_title: 'Worker', session_name: 'claude-worker-3645' });
            const dep = await call('POST', '/tasks', { title: 'the dependency', body: 'b', created_by: 'Operator' });
            const open = await call('POST', '/tasks', { title: 'open task', body: 'see coral://artifacts/${'a'.repeat(64)}', created_by: 'Operator', blocked_by: [dep.id] });
            const draft = await call('POST', '/tasks', { title: 'a draft', body: 'b', created_by: 'Operator', draft: true });
            const claimed = await call('POST', '/tasks', { title: 'claimed task', body: 'b', created_by: 'Operator', assigned_to: 'Worker' });
            await call('POST', '/tasks/claim', { subscriber_id: 'Worker' }).catch(() => null);
            return { dep: dep.id, open: open.id, draft: draft.id, claimed: claimed.id };
        })()`);
        const task = id => ev(`fetch('/api/board/${BOARD}/tasks/${id}').then(r => r.json()).then(j => j.task || j)`);
        // The team's board view, as board_task_paging sets it: the dialogs act on state.selectedTeam.
        const load = () => ev(`(async () => { ${MODS} tasks.stopBoardTaskPoll(); state.currentSession = null; state.selectedTeam = ${JSON.stringify(BOARD)};
            await tasks.loadBoardTasks(${JSON.stringify(BOARD)}); return true; })()`);
        await load();
        await until(`document.querySelectorAll('#board-task-list [data-on-click="task-open"]').length >= 3`, 'the task list');
        const row = id => `document.querySelector('#board-task-list [data-on-click="task-open"][data-task-id="${id}"]')`;
        const dlgOpen = () => ev(`(() => { const m = document.getElementById('task-detail-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`);
        const foot = sel => `document.querySelector('#task-detail-modal [data-on-click="${sel}"]')`;

        // ── The list ───────────────────────────────────────────────────────
        const sorted = await ev(`(async () => { ${MODS}
            const order = () => [...document.querySelectorAll('#board-task-list [data-on-click="task-open"]')].map(r => r.dataset.taskId).join(',');
            const before = order();
            document.querySelector('#board-task-list [data-on-click="task-sort"][data-sort="id"]').click();
            const a = order();
            document.querySelector('#board-task-list [data-on-click="task-sort"][data-sort="id"]').click();
            const b = order();
            const bad = document.createElement('span'); bad.setAttribute('data-on-click', 'task-sort'); bad.dataset.sort = '__proto__'; document.body.appendChild(bad); bad.click(); bad.remove();
            return { before, a, b, after: order() };
        })()`);
        check('the ID header sorts the list and again reverses it; an unknown sort does nothing', sorted.a !== sorted.b && sorted.a.split(',').reverse().join(',') === sorted.b && sorted.after === sorted.b, JSON.stringify(sorted));

        // ── The dialog: open, edit ─────────────────────────────────────────
        await ev(`${row(made.open)}.click(); true`);
        await until(`(() => { const m = document.getElementById('task-detail-modal'); return !!m && getComputedStyle(m).display !== 'none'; })()`, 'the task dialog');
        check('a row opens its task', await dlgOpen());
        await ev(`${foot('task-edit')}.click(); true`);
        await until(`!!${foot('task-edit-save')}`, 'the edit form');
        await ev(`${foot('task-edit-cancel')}.click(); true`);
        check('Edit, then Cancel, returns to the task', await until(`!!${foot('task-edit')}`, 'the task footer', 3000).then(() => ev(`!!${foot('task-edit')}`)));
        await ev(`${foot('task-edit')}.click(); true`);
        await until(`!!${foot('task-edit-save')}`, 'the edit form again');
        await ev(`(() => { const t = document.querySelector('#task-detail-modal input[type="text"], #task-detail-modal #task-edit-title'); if (t) t.value = 'open task (edited)'; ${foot('task-edit-save')}.click(); return true; })()`);
        await until(`window.__req.some(r => /^(PUT|PATCH) .*\\/tasks\\/${made.open}$/.test(r))`, 'the save', 5000);
        check('Save sends the task', (await ev(`window.__req`)).some(r => new RegExp(`^(PUT|PATCH) .*/tasks/${made.open}$`).test(r)), JSON.stringify(await ev(`window.__req`)));

        // A dependency link opens that task.
        await ev(`${row(made.open)} && ${row(made.open)}.click(); true`);
        await until(`!!document.querySelector('#task-detail-modal .task-dep-link[data-on-click="task-open"]')`, 'the dependency link');
        await ev(`document.querySelector('#task-detail-modal .task-dep-link[data-on-click="task-open"]').click(); true`);
        check('a dependency link opens that task', await until(`document.getElementById('task-detail-modal').textContent.includes('the dependency')`, "the dependency's dialog", 3000)
            .then(() => ev(`document.getElementById('task-detail-modal').textContent.includes('the dependency')`)));

        // ── Cancel through its confirmation (on the dependency, in the board's own context) ─
        await load();
        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${made.dep}); return true; })()`);
        await until(`document.getElementById('task-detail-modal-title').textContent === 'Task #${made.dep}' && !!${foot('task-cancel')}`, "the dependency's Cancel Task");
        await ev(`${foot('task-cancel')}.click(); true`);
        await until(`!!${foot('task-cancel-do')}`, 'the cancel confirmation');
        check('Cancel Task asks first ("Yes, Cancel" / "No, Go Back"), and asking sends nothing', !(await ev(`window.__req`)).some(r => r.includes(`/tasks/${made.dep}/cancel`)) && await ev(`!!${foot('task-cancel-back')}`));
        await ev(`${foot('task-cancel-back')}.click(); true`);
        check('No, Go Back restores the footer', await until(`!!${foot('task-cancel')}`, 'the footer', 3000).then(() => ev(`!!${foot('task-cancel')}`)));
        await ev(`${foot('task-cancel')}.click(); true`);
        await until(`!!${foot('task-cancel-do')}`, 'the cancel confirmation again');
        await ev(`window.__req.length = 0; ${foot('task-cancel-do')}.click(); true`);
        await until(`window.__req.some(r => r === 'POST /api/board/${BOARD}/tasks/${made.dep}/cancel')`, 'the cancel request', 5000);
        check('Yes, Cancel sends the cancel', (await ev(`window.__req`)).includes(`POST /api/board/${BOARD}/tasks/${made.dep}/cancel`), JSON.stringify(await ev(`window.__req`)));
        // A cancelled task is stored as skipped.
        await until(`fetch('/api/board/${BOARD}/tasks/${made.dep}').then(r => r.json()).then(j => ['skipped', 'cancelled'].includes((j.task || j).status))`, 'cancelled', 5000);
        check('Yes, Cancel cancels it', ['skipped', 'cancelled'].includes((await task(made.dep)).status), JSON.stringify((await task(made.dep)).status));

        // ── Publish a draft ────────────────────────────────────────────────
        await load();
        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${made.draft}); return true; })()`);
        await until(`!!${foot('task-publish')}`, "the draft's Publish");
        await ev(`${foot('task-publish')}.click(); true`);
        await until(`fetch('/api/board/${BOARD}/tasks/${made.draft}').then(r => r.json()).then(j => (j.task || j).status !== 'draft')`, 'published', 5000);
        check('Publish publishes a draft', (await task(made.draft)).status !== 'draft', JSON.stringify(await task(made.draft)));

        // ── Nudge, Remind, Stop on a claimed task; Complete through its form ─
        await load();
        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${made.claimed}); return true; })()`);
        await until(`!!${foot('task-nudge')}`, "the claimed task's Nudge");
        // Remind asks for its interval in its own dialog (#6329; window.prompt would block the page and does not exist in the native app): accept the 5 minutes it offers.
        await ev(`window.prompt = () => { throw new Error('window.prompt must not be used'); }; window.__req.length = 0; ${foot('task-nudge')}.click(); ${foot('task-remind')}.click(); true`);
        await until(`document.getElementById('reminder-dialog')?.open === true`, 'the reminder dialog', 5000);
        await ev(`document.querySelector('#reminder-dialog .reminder-go').click(); true`);
        await until(`window.__req.some(r => r.includes('/nudge')) && window.__req.some(r => r.startsWith('POST') && r.includes('/reminder'))`, 'nudge and remind', 5000);
        await ev(`${foot('task-remind-stop')} && ${foot('task-remind-stop')}.click(); true`);
        await until(`window.__req.some(r => r.startsWith('DELETE') && r.includes('/reminder'))`, 'stop', 5000);
        const nr = await ev(`window.__req`);
        check('Nudge, Remind and Stop reminders reach the task', ['/nudge', 'POST', 'DELETE'].every(k => nr.some(r => r.includes(k))), JSON.stringify(nr));
        await ev(`${foot('task-complete')}.click(); true`);
        await until(`!!${foot('task-complete-do')}`, 'the completion form');
        await ev(`${foot('task-complete-back')}.click(); true`);
        check('Complete opens its form, and Back returns', await until(`!!${foot('task-complete')}`, 'the footer', 3000).then(() => ev(`!!${foot('task-complete')}`)));
        await ev(`${foot('task-complete')}.click(); true`);
        await until(`!!${foot('task-complete-do')}`, 'the completion form again');
        await ev(`${foot('task-complete-do')}.click(); true`);
        await until(`fetch('/api/board/${BOARD}/tasks/${made.claimed}').then(r => r.json()).then(j => (j.task || j).status === 'completed')`, 'completed', 5000);
        check('Complete completes it', (await task(made.claimed)).status === 'completed', JSON.stringify(await task(made.claimed)));

        // ── Close; a bad id; the artifact link ─────────────────────────────
        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${made.open}); return true; })()`);
        await until(`!!${foot('task-close')}`, 'the dialog');
        await ev(`${foot('task-close')}.click(); true`);
        check('Close closes the dialog', !(await dlgOpen()));
        const bad = await ev(`(() => { let opened = null; const keep = window.showTaskDetailModal; window.showTaskDetailModal = id => { opened = id; };
            const b = document.createElement('div'); b.setAttribute('data-on-click', 'task-open'); b.dataset.taskId = '1 OR 1'; document.body.appendChild(b); b.click(); b.remove();
            window.showTaskDetailModal = keep; return opened; })()`);
        check('a task link with a bad id does nothing', bad === null, String(bad));
        const art = await ev(`(() => { const seen = []; const keep = window.openFilePreview; window.openFilePreview = u => seen.push(u);
            const mk = uri => { const a = document.createElement('a'); a.href = '#'; a.setAttribute('data-on-click', 'task-artifact-open'); a.dataset.artifactUri = uri; document.body.appendChild(a); const e = new MouseEvent('click', { bubbles: true, cancelable: true }); a.dispatchEvent(e); a.remove(); return e.defaultPrevented; };
            const p1 = mk('coral://artifacts/${'b'.repeat(64)}'), p2 = mk('javascript:alert(1)');
            window.openFilePreview = keep; return { seen, p1, p2 }; })()`);
        check('the artifact link opens only a coral:// artifact, and never navigates', JSON.stringify(art.seen) === JSON.stringify(['coral://artifacts/' + 'b'.repeat(64)]) && art.p1 && art.p2, JSON.stringify(art));

        // ── The dependency picker (the create-task dialog) ─────────────────
        await ev(`(async () => { ${MODS}
            if (typeof window.showCreateTaskModal === 'function') await window.showCreateTaskModal();
            return true;
        })()`);
        // The picker renders after the dialog loads its tasks: wait for its link (up to 3 s).
        await waitFor(`!!document.querySelector('#create-task-deps [data-on-click="dep-picker-toggle"]')`, 'the dependency picker link', { timeout: 3000, soft: true });
        const picker = await ev(`(async () => { ${MODS}
            const box = document.getElementById('create-task-deps');
            const add = box && box.querySelector('[data-on-click="dep-picker-toggle"]');
            if (!add) return { box: !!box };
            const list = document.getElementById('create-task-deps-list');
            const d0 = list.style.display; add.click(); const d1 = list.style.display;
            const cb = list.querySelector('input[type="checkbox"][data-on-change="dep-picker-change"]');
            cb.click();
            const tags1 = document.querySelectorAll('#create-task-deps-tags .dep-tag').length;
            document.querySelector('#create-task-deps-tags [data-on-click="dep-tag-remove"]').click();
            const tags2 = document.querySelectorAll('#create-task-deps-tags .dep-tag').length;
            window.hideCreateTaskModal && window.hideCreateTaskModal();
            return { box: true, toggled: d0 !== d1, tags1, tags2, unchecked: !cb.checked };
        })()`);
        check('+ Add dependency shows the list; a checkbox makes a tag; its × removes it', picker.toggled && picker.tags1 === 1 && picker.tags2 === 0 && picker.unchecked, JSON.stringify(picker));

        await ev(`(async () => { ${MODS} tasks.showTaskDetailModal(${made.open}); return true; })()`);
        await until(`!!${foot('task-close')}`, 'the dialog for the census');
        const inline = await ev(`[...document.querySelectorAll('#board-task-list *, #task-detail-modal *, #create-task-deps *')].flatMap(el => [...el.attributes].filter(a => /^on/i.test(a.name)).map(a => el.tagName + '.' + el.className + ' ' + a.name))`);
        check('no inline handler left in the task list, the task dialog or the dependency picker', inline.length === 0, JSON.stringify(inline.slice(0, 5)));
    } catch (e) {
        check('the test ran', false, e.stack || String(e));
    } finally {
        await removeBoards(ev, [BOARD]);
        await client.close();
    }
    const failed = results.filter(r => !r).length;
    console.log(`\n${results.length - failed}/${results.length} passed`);
    process.exit(failed ? 1 : 0);
})();
