// Kept worktrees in Settings > Storage and cleanup (#6743, the dashboard side of #6736): the list of worktrees a stop kept because they hold work (GET /api/worktrees), each with its name, kind, branch, folder and
// what keeps it (Unsaved changes, Unmerged commits, In use); Remove on each that is not in use (POST /api/worktrees/remove {id}, never forced), whose answer {state, path, branch, reason} is said in the
// status that is always in the page and the list is read again; no Remove for a worktree in use, and none through Coral Hub (the list is for reading there). 1280 and 390 px. The server's answers are the page's fetch
// (Backend's #6736 shape). Drives Chrome over CDP through agent_view_harness.js against the isolated dev server.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const S = `(window.__coralStatic || '/static/')`;

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    const shot = async name => { const d = process.env.CORAL_SCREENSHOT_DIR; if (!d) return; await sleep(300); /* sleep-ok: a screenshot waits out transitions */ fs.mkdirSync(d, { recursive: true }); fs.writeFileSync(`${d}/${name}.png`, Buffer.from((await Page.captureScreenshot({ format: 'png' })).data, 'base64')); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 900, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.then(() => true)`);
            await ev(`(() => { const real = window.fetch; const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
                window.__reads = 0; window.__removes = [];
                window.__wt = [
                    { id: 'w1', kind: 'team', name: 'crew-1', path: '/data/worktrees/crew-1', repo_path: '/work/repo', branch: 'coral-team/crew-1', dirty: true, unmerged: false, in_use: false },
                    { id: 'w2', kind: 'job', name: 'nightly-agent', path: '/data/worktrees/job-5-run-2', repo_path: '/work/repo', branch: 'coral-job/5-2', dirty: false, unmerged: true, in_use: false },
                    { id: 'w3', kind: 'team', name: 'crew-2', path: '/data/worktrees/crew-2', repo_path: '/work/other', branch: 'coral-team/crew-2', dirty: false, unmerged: false, in_use: true } ];
                window.__answers = { w1: { state: 'kept', path: '/data/worktrees/crew-1', branch: 'coral-team/crew-1', reason: 'it has uncommitted changes' }, w2: { state: 'removed', path: '/data/worktrees/job-5-run-2', branch: 'coral-job/5-2', reason: 'its branch is kept: it has commits that are not merged' } };
                window.fetch = (u, o) => { const p = new URL(String(u), location.origin).pathname, m = (o && o.method) || 'GET';
                    if (p === '/api/worktrees' && m === 'GET') { window.__reads++; return json({ worktrees: window.__wt }); }
                    if (p === '/api/worktrees/remove' && m === 'POST') { const id = JSON.parse(o.body).id; window.__removes.push(id); const a = window.__answers[id]; if (a && a.state === 'removed') window.__wt = window.__wt.filter(x => x.id !== id); return json(a || { error: 'no such worktree' }, a ? 200 : 404); }
                    return real(u, o); };
                return true; })()`);
            await ev(`window.showSettingsModal('cleanup'); true`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 3`, 'the kept worktrees', 8000);
            await waitFor(`!document.getElementById('settings-pane-cleanup').hidden && !document.getElementById('wt-status').closest('[hidden]')`, 'the Storage and cleanup page to be shown (the rows are drawn into it before it is', 8000); // (the status check below reads the page as the person sees it: a slow runner drew the rows before the group was shown)

            console.log(`${tag}: the list`);
            const rows = await ev(`[...document.querySelectorAll('#wt-list .wt-item')].map(li => ({ name: li.querySelector('.wt-name').textContent, kind: li.querySelector('.wt-kind').textContent, branch: (li.querySelector('code') || {}).textContent, path: li.querySelector('.wt-path').textContent,
                badges: [...li.querySelectorAll('.wt-badge')].map(b => b.textContent), remove: li.querySelector('.wt-remove') ? li.querySelector('.wt-remove').getAttribute('aria-label') : null }))`);
            check(`${tag}: each worktree shows its name, kind, branch, folder and what keeps it`, JSON.stringify(rows.map(r => [r.name, r.kind, r.branch, r.badges.join('+')])) === JSON.stringify([['crew-1', 'team', 'coral-team/crew-1', 'Unsaved changes'], ['nightly-agent', 'job run', 'coral-job/5-2', 'Unmerged commits'], ['crew-2', 'team', 'coral-team/crew-2', 'In use']]) && rows.every(r => r.path.startsWith('/data/worktrees/')), JSON.stringify(rows));
            check(`${tag}: Remove is named for its worktree, and absent for the one in use`, rows[0].remove === 'Remove the worktree of crew-1 if it is clean' && rows[1].remove === 'Remove the worktree of nightly-agent if it is clean' && rows[2].remove === null, JSON.stringify(rows.map(r => r.remove)));
            check(`${tag}: the status is in the page, empty, with its role`, await ev(`document.getElementById('wt-status').getAttribute('role') === 'status' && document.getElementById('wt-status').textContent === '' && !document.getElementById('wt-status').closest('[hidden]')`));
            await ev(`document.getElementById('settings-worktrees-row').scrollIntoView(); true`);
            await shot(`worktrees-${w}`);

            console.log(`${tag}: Remove`);
            await ev(`document.querySelector('#wt-list .wt-item[data-id="w1"] .wt-remove').click(); true`);
            await waitFor(`document.getElementById('wt-status').textContent !== ''`, 'the answer', 6000);
            const a1 = await ev(`({ text: document.getElementById('wt-status').textContent, removes: window.__removes, reads: window.__reads, items: document.querySelectorAll('#wt-list .wt-item').length })`);
            check(`${tag}: a refusal says it was kept and why and where, and the worktree stays`, a1.text === 'The worktree was kept: it has uncommitted changes. It is at /data/worktrees/crew-1, on branch coral-team/crew-1.' && a1.items === 3 && a1.removes.join() === 'w1' && a1.reads >= 2, JSON.stringify(a1));
            await ev(`document.querySelector('#wt-list .wt-item[data-id="w2"] .wt-remove').click(); true`);
            await waitFor(`/was removed/.test(document.getElementById('wt-status').textContent)`, 'the removal answer', 6000);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2`, 'the list to be read again', 6000);
            const a2 = await ev(`({ text: document.getElementById('wt-status').textContent, names: [...document.querySelectorAll('#wt-list .wt-name')].map(n => n.textContent) })`);
            check(`${tag}: a removal says so and what stays (its branch), and the worktree leaves the list`, a2.text === 'The worktree was removed. Its branch is kept: it has commits that are not merged.' && a2.names.join() === 'crew-1,crew-2', JSON.stringify(a2));

            console.log(`${tag}: what Remove will do, and where the focus goes`);
            await ev(`(() => { window.__keep = window.__wt;
                window.__wt = [
                    { id: 'w1', kind: 'team', name: 'crew-1', path: '/data/worktrees/crew-1', repo_path: '/work/repo', branch: 'coral-team/crew-1', dirty: true, unmerged: false, in_use: false, checked: true },
                    { id: 'w4', kind: 'team', name: 'crew-4', path: '/data/worktrees/crew-4', repo_path: '/work/repo', branch: 'coral-team/crew-4', dirty: false, unmerged: false, in_use: false, checked: true },
                    { id: 'w5', kind: 'job', name: 'crew-5', path: '/data/worktrees/crew-5', repo_path: '/work/repo', branch: 'coral-job/5-3', dirty: false, unmerged: false, in_use: false, checked: false } ];
                window.__answers = { w4: { state: 'removed', path: '/data/worktrees/crew-4', branch: 'coral-team/crew-4', reason: '' }, w5: { state: 'removed', path: '/data/worktrees/crew-5', branch: 'coral-job/5-3', reason: '' }, w1: { state: 'removed', path: '/data/worktrees/crew-1', branch: 'coral-team/crew-1', reason: '' } };
                return import(${S} + 'worktrees_panel.js').then(m => m.loadWorktrees()).then(() => true); })()`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 3`, 'the three rows', 6000);
            const lab = await ev(`[...document.querySelectorAll('#wt-list .wt-item')].map(li => ({ id: li.dataset.id, text: li.querySelector('.wt-remove').textContent, aria: li.querySelector('.wt-remove').getAttribute('aria-label'), badges: [...li.querySelectorAll('.wt-badge')].map(b => b.textContent) }))`);
            check(`${tag}: a clean row says Remove; one holding work, or not checked, says Remove if clean (never shown as clean)`, JSON.stringify(lab.map(r => [r.id, r.text, r.badges.join('+')])) === JSON.stringify([['w1', 'Remove if clean', 'Unsaved changes'], ['w4', 'Remove', ''], ['w5', 'Remove if clean', 'Not checked']]) && lab[2].aria === 'Remove the worktree of crew-5 if it is clean' && lab[1].aria === 'Remove the worktree of crew-4', JSON.stringify(lab));
            const focused = () => ev(`(() => { const a = document.activeElement; const li = a && a.closest && a.closest('.wt-item'); return li ? li.dataset.id + ':' + a.className.split(' ').pop() : (a && a.id) || (a && a.tagName); })()`);
            await ev(`document.querySelector('#wt-list .wt-item[data-id="w4"] .wt-remove').focus(); document.querySelector('#wt-list .wt-item[data-id="w4"] .wt-remove').click(); true`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2`, 'w4 to leave', 6000);
            check(`${tag}: after Remove the focus is on the next row's Remove`, (await focused()) === 'w5:wt-remove', await focused());
            await ev(`document.querySelector('#wt-list .wt-item[data-id="w5"] .wt-remove').click(); true`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 1`, 'w5 to leave', 6000);
            check(`${tag}: and from the last row the focus goes to the row before`, (await focused()) === 'w1:wt-remove', await focused());
            await ev(`document.querySelector('#wt-list .wt-item[data-id="w1"] .wt-remove').click(); true`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 0`, 'the list to empty', 6000);
            check(`${tag}: with no row left the focus goes to the Kept worktrees title`, (await focused()) === 'wt-title', await focused());
            await ev(`window.__wt = window.__keep; window.__answers = {}; true`);
            await ev(`import(${S} + 'worktrees_panel.js').then(m => m.loadWorktrees()).then(() => true)`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2`, 'the two rows again', 6000);

            console.log(`${tag}: a team is named by its name, with its board id beside it (#6943)`);
            await ev(`(async () => { const sb = await import(${S} + 'server_base.js'); sb.noteBoardLabel('local', 'b-6de2yafh7f', 'desktop-1'); window.__keep2 = window.__wt; window.__wt = [{ id: 'w9', kind: 'team', name: 'b-6de2yafh7f', path: '/data/worktrees/b-6de2yafh7f', repo_path: '/work/repo', branch: 'coral-team/b-6de2yafh7f', dirty: true, unmerged: false, in_use: false, checked: true }, { id: 'w8', kind: 'team', name: 'b-nolabel1', path: '/data/worktrees/b-nolabel1', repo_path: '/work/repo', branch: 'coral-team/b-nolabel1', dirty: false, unmerged: true, in_use: false, checked: true }];
                const m = await import(${S} + 'worktrees_panel.js'); await m.loadWorktrees(); return true; })()`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2`, 'the two team rows', 6000);
            const named = await ev(`[...document.querySelectorAll('#wt-list .wt-item')].map(li => ({ name: li.querySelector('.wt-name').textContent, id: (li.querySelector('.wt-id') || {}).textContent || '', aria: li.querySelector('.wt-remove').getAttribute('aria-label') }))`);
            check(`${tag}: a team with a known name shows the name first and its board id beside it; one without shows the id once`, named[0].name === 'desktop-1' && named[0].id === 'b-6de2yafh7f' && named[0].aria === 'Remove the worktree of desktop-1 if it is clean' && named[1].name === 'b-nolabel1' && named[1].id === '', JSON.stringify(named));
            await ev(`window.__wt = window.__keep2; import(${S} + 'worktrees_panel.js').then(m => m.loadWorktrees()).then(() => true)`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2`, 'the rows again', 6000);

            console.log(`${tag}: through Coral Hub, and empty`);
            await ev(`window.__hub = import(${S} + 'worktrees_panel.js').then(m => m.loadWorktrees({ canRemove: false })).then(() => true)`);
            await waitFor(`document.querySelectorAll('#wt-list .wt-item').length === 2 && document.querySelectorAll('#wt-list .wt-remove').length === 0`, 'the list without Remove', 6000);
            check(`${tag}: through Coral Hub the list shows and there is no Remove, with the reason said`, await ev(`!document.getElementById('wt-hub-note').hidden && /for reading/.test(document.getElementById('wt-hub-note').textContent)`));
            await ev(`window.__wt = []; import(${S} + 'worktrees_panel.js').then(m => m.loadWorktrees()).then(() => true)`);
            await waitFor(`!document.getElementById('wt-empty').hidden`, 'the empty line', 6000);
            check(`${tag}: no kept worktrees says so in two words`, (await ev(`document.getElementById('wt-empty').textContent`)) === 'None kept.');
            await ev(`window.hideSettingsModal && window.hideSettingsModal(); true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} kept worktree checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
