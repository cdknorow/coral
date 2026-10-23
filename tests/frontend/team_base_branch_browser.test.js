// The Launch Team dialog's Base Branch (#6707, UI Accessibility's #6702 finding 2): it starts as the repo's own current branch (the is-git answer's `branch`), not "main" in a repo that has master, and a branch
// the person typed is kept when the folder changes; a launch the server refuses for it (400, code base_branch_missing, field base_branch) is said under the field, as an alert, with the field marked, More
// options opened and the focus on it, not in a toast; the line goes when the field is edited. Drives Chrome over CDP through agent_view_harness.js against the isolated dev server; the answers are the page's fetch.
const assert = require('node:assert/strict');
const { connect } = require('./agent_view_harness');
const BASE = require('./test_base').BASE;
const MISSING = 'The base branch main does not exist in /work/repo. Its branches: master. Choose one under More options > Base Branch.';

(async () => {
    const { client, Page, Emulation, ev, waitFor, sleep, PRELOAD, install } = await connect();
    const results = [];
    const check = (label, cond, detail) => { results.push(!!cond); console.log(`[${cond ? 'PASS' : 'FAIL'}] ${label}${detail !== undefined ? ' - ' + detail : ''}`); };
    try {
        await Page.enable(); await Page.addScriptToEvaluateOnNewDocument({ source: PRELOAD });
        for (const [w, h, mobile] of [[1280, 800, false], [390, 844, true]]) {
            const tag = `${w}px`;
            await Emulation.setDeviceMetricsOverride({ width: w, height: h, deviceScaleFactor: 1, mobile }); await Emulation.setTouchEmulationEnabled({ enabled: mobile });
            await Page.navigate({ url: BASE + '/' }); await Page.loadEventFired(); await install();
            await ev(`window.__coralReady.startup('liveList', 'settings')`);
            await ev(`(async () => { window.__launches = []; window.__repoBranch = 'master'; window.__launchAnswer = null;
                const real = window.fetch; const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
                window.fetch = (u, o = {}) => { const p = new URL(String(u), location.href), m = o.method || 'GET';
                    if (p.pathname === '/api/filesystem/is-git') return json({ is_git: true, branch: p.searchParams.get('path') === '/work/other' ? 'develop' : window.__repoBranch });
                    if (p.pathname === '/api/sessions/launch-team' && m === 'POST') { window.__launches.push(JSON.parse(o.body)); return window.__launchAnswer ? json(window.__launchAnswer.body, window.__launchAnswer.status) : json({ agents: [{ name: 'A', session_id: 's' }] }); }
                    if (p.pathname === '/api/board/projects') return json([]);
                    return real(u, o); };
                await window.showLaunchModal(); window.hideLaunchModal(); return true; })()`);
            const click = sel => ev(`document.querySelector(${JSON.stringify(sel)}).click(); true`);
            const type = (id, v) => ev(`{ const i = document.getElementById(${JSON.stringify(id)}); i.value = ${JSON.stringify(v)}; i.dispatchEvent(new Event('input', { bubbles: true })); i.dispatchEvent(new Event('change', { bubbles: true })); } true`);
            await ev(`window.showLaunchModal(); window._selectLaunchType('team'); true`);
            await waitFor(`getComputedStyle(document.getElementById('launch-step-team')).display !== 'none' && document.querySelectorAll('#team-template-list [role=option]').length > 0`, 'the team step');
            await type('team-board-name', 'crew-1'); await type('launch-team-dir', '/work/repo');
            await waitFor(`document.getElementById('launch-team-base-branch').value === 'master'`, 'Base Branch to take the repo\'s branch', 6000);
            check(`${tag}: Base Branch starts as the repo's current branch (master), not "main"`, (await ev(`document.getElementById('launch-team-base-branch').value`)) === 'master' && (await ev(`document.getElementById('launch-team-base-branch').placeholder`)) !== 'main');
            await type('launch-team-dir', '/work/other');
            await waitFor(`document.getElementById('launch-team-base-branch').value === 'develop'`, 'Base Branch to follow another repo', 6000);
            check(`${tag}: another folder, another repo's branch (develop) while nothing was typed`, true);
            await ev(`{ const i = document.getElementById('launch-team-base-branch'); i.value = 'release'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
            await type('launch-team-dir', '/work/repo'); await sleep(700); // sleep-ok: the folder check is debounced; nothing to wait for but a change that must NOT happen
            check(`${tag}: a branch the person typed (release) is kept when the folder changes`, (await ev(`document.getElementById('launch-team-base-branch').value`)) === 'release');

            // a launch refused for the branch
            await ev(`{ const c = document.getElementById('launch-team-worktree'); if (!c.checked) { c.checked = true; c.dispatchEvent(new Event('change', { bubbles: true })); } } true`);
            await ev(`{ const i = document.getElementById('launch-team-base-branch'); i.value = 'main'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
            await ev(`window.__launchAnswer = { status: 400, body: { error: ${JSON.stringify(MISSING)}, code: 'base_branch_missing', field: 'base_branch', branches: ['master'], default_branch: 'master' } }; document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
            await ev(`document.querySelector('#team-template-list [role=option]').click(); true`); // a template: the members the launch needs
            await waitFor(`document.querySelectorAll('#team-agents-list .team-agent-row').length > 0`, 'the template members');
            await ev(`document.querySelector('#launch-step-team .team-step-tab[data-step="start"]').click(); true`);
            await click('#launch-team-go');
            await waitFor(`!!document.getElementById('launch-team-base-branch-error')`, 'the inline error', 6000);
            await sleep(300); // sleep-ok: the absence of a toast
            const e = await ev(`(() => { const i = document.getElementById('launch-team-base-branch'), l = document.getElementById('launch-team-base-branch-error');
                return { invalid: i.getAttribute('aria-invalid'), described: (i.getAttribute('aria-describedby') || '').split(' ').includes('launch-team-base-branch-error'), role: l.getAttribute('role'), text: l.textContent,
                    open: !!i.closest('details') && i.closest('details').open, focus: document.activeElement === i, toasts: [...document.querySelectorAll('.toast')].map(t => t.textContent.trim()), visible: l.getBoundingClientRect().height > 0 }; })()`);
            check(`${tag}: the refusal is said under Base Branch, as an alert, with the field marked and described, More options open and the focus on it`, e.invalid === 'true' && e.described && e.role === 'alert' && e.text === MISSING && e.open && e.focus && e.visible, JSON.stringify(e));
            check(`${tag}: and not as a toast (a toast is gone in five seconds and cut to two lines on a phone)`, e.toasts.length === 0, JSON.stringify(e.toasts));
            check(`${tag}: the dialog stays open to fix it`, await ev(`getComputedStyle(document.getElementById('launch-modal')).display !== 'none'`));
            await ev(`{ const i = document.getElementById('launch-team-base-branch'); i.value = 'master'; i.dispatchEvent(new Event('input', { bubbles: true })); } true`);
            check(`${tag}: editing the field takes the line and the mark away`, (await ev(`!document.getElementById('launch-team-base-branch-error') && !document.getElementById('launch-team-base-branch').hasAttribute('aria-invalid')`)));
            await ev(`window.__launchAnswer = null; window.hideLaunchModal(); true`);
        }
    } finally { await client.close(); }
    if (results.some(r => !r)) { console.error(`${results.filter(r => !r).length} of ${results.length} failed`); process.exit(1); }
    console.log(`\nAll ${results.length} Base Branch checks passed`);
})().catch(e => { console.error(e); process.exit(1); });
