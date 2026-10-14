// Knowledge distillation survives leaving the view (#3325). The server runs a distillation in
// the background; the dashboard used to follow it only from the Knowledge panel that started
// it, so switching away lost it. Now:
//   - start → leave the tab and the team → the team header says "Distilling…" → come back:
//     the progress is shown, and no second run was started;
//   - it completes while you are elsewhere: a toast says the knowledge is ready, the chip goes,
//     and the tab shows the bundle with when it was updated;
//   - a failure: with no knowledge yet, the error and Retry; with earlier knowledge, that
//     knowledge with a note that the last run failed;
//   - a run in progress is found after a reload, without opening the tab;
//   - a run lost to a server restart (running → none) says so in a toast.
// /api/teams/detail/alpha/distill-knowledge and /knowledge are scripted in the page.
//
// Expects CORAL_TEST_URL + CDP_PORT from tests/frontend/run.sh. Exit 0 / 1.

const CDP = require('chrome-remote-interface');
const BASE = require('./test_base').BASE;

const results = [];
function check(label, cond, detail) {
    const v = cond ? 'PASS' : 'FAIL';
    results.push(v);
    console.log(`[${v}] ${label}${detail !== undefined ? ' — ' + detail : ''}`);
}

// The scripted server. window.__distill is the job status; window.__bundle the knowledge.
// sessionStorage keeps them across a reload of the page.
const STUB = `(function(){
  const load = (k, d) => { try { const v = sessionStorage.getItem(k); return v ? JSON.parse(v) : d; } catch { return d; } };
  window.__distill = load('qa-distill', { status: 'none' });
  window.__bundle = load('qa-bundle', { exists: false });
  window.__posts = 0;
  window.__save = () => { sessionStorage.setItem('qa-distill', JSON.stringify(window.__distill)); sessionStorage.setItem('qa-bundle', JSON.stringify(window.__bundle)); };
  const real = window.fetch.bind(window);
  const json = (b, s = 200) => Promise.resolve(new Response(JSON.stringify(b), { status: s, headers: { 'Content-Type': 'application/json' } }));
  window.fetch = (url, opts = {}) => {
    const u = String(url); const m = (opts.method || 'GET').toUpperCase();
    if (u.includes('/api/teams/detail/alpha/distill-knowledge')) {
      if (m === 'POST') {
        if (window.__distill.status === 'running') return json(window.__distill);
        window.__posts++;
        window.__distill = { team_name: 'alpha', status: 'running', progress: { phase: 'starting', total: 0, completed: 0 }, started_at: new Date().toISOString() };
        return json(window.__distill, 202);
      }
      return json(window.__distill);
    }
    if (u.includes('/api/teams/detail/alpha/knowledge')) { window.__bundleReads = (window.__bundleReads || 0) + 1; return json(window.__bundle); }
    if (u.includes('/api/board/alpha/messages/all')) return json({ total: 0, messages: [] });
    if (u.includes('/api/board/alpha/status')) return json({ summary: {}, observed_at: new Date().toISOString(), agents: [], unassigned_tasks: [], health_report: [] });
    if (u.includes('/api/board/alpha/working-mode/presets')) return json({ presets: [] });
    if (u.includes('/api/board/alpha/working-mode')) return json({ mode: 'none', dependency_guidance: false, custom_instructions: '' });
    if (u.includes('/api/board/alpha/tasks')) return json({ tasks: [], next_cursor: '', total_open: 0, total_completed: 0 });
    if (u.includes('/api/board/alpha/paused')) return json({ paused: false });
    if (u.includes('/api/board/alpha/subscribers')) return json([]);
    return real(url, opts);
  };
})();`;

const BUNDLE = { exists: true, index: '# Team alpha\\n\\nWhat the team knows.', agents: [{ name: 'lead', content: '# Lead\\n\\nNotes.' }] };

(async () => {
    const client = await CDP({ port: Number(process.env.CDP_PORT || 9222) });
    const { Page, Runtime, Emulation } = client;
    const exceptions = [];
    const ev = async expression => {
        const r = await Runtime.evaluate({ expression, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300));
        return r.result.value;
    };
    const { waitFor, coralReady } = require('./helpers/wait').bind(ev);
    const until = (x, what, timeout = 5000) => waitFor(x, what, { soft: true, timeout });
    const S = `(window.__coralStatic||'/static/')`;
    const boot = async () => {
        await Page.navigate({ url: BASE }); await Page.loadEventFired();
        await coralReady();
        await ev(`(async () => {
            const { state } = await import(${S}+'state.js');
            const render = await import(${S}+'render.js');
            const tc = await import(${S}+'team_context.js');
            const kd = await import(${S}+'knowledge_distill.js');
            kd.distillHooks.pollMs = 150;
            window.__tc = tc; window.__render = render; window.__state = state; window.__kd = kd;
            // The app's own live list (re-renders, e.g. for the Distilling chip, redraw from it).
            window._coralSetLiveSessions([{ session_id: 'alpha-1', name: 'alpha-1', agent_type: 'claude', board_project: 'alpha', display_name: 'Alpha agent', working_directory: '/tmp/alpha' }]);
            return true;
        })()`);
    };
    const panelText = () => ev(`(document.getElementById('team-knowledge-view') || {}).textContent || ''`);
    const chip = () => ev(`!!document.querySelector('.session-group-header .team-distill-chip')`);
    const toasts = () => ev(`[...document.querySelectorAll('.toast .toast-message')].map(t => t.textContent)`);
    const openKnowledge = () => ev(`window.__tc.enterTeamContext('alpha', { tab: 'knowledge' }); true`);
    const leave = async () => {
        await ev(`(() => { window.switchAgenticTab('tasks', 'top'); const c = document.querySelector('.team-center-close'); if (c) c.click(); return true; })()`);
        await until(`!window.__state.selectedTeam`, 'the team view closed');
    };

    try {
        await Promise.all([Page.enable(), Runtime.enable()]);
        Runtime.exceptionThrown(e => exceptions.push((((e.exceptionDetails || {}).exception || {}).description || 'exception').slice(0, 200)));
        // A clean scripted server for the first load only; a later reload keeps its state.
        const clean = await Page.addScriptToEvaluateOnNewDocument({ source: `sessionStorage.removeItem('qa-distill'); sessionStorage.removeItem('qa-bundle');` });
        await Page.addScriptToEvaluateOnNewDocument({ source: STUB });
        await Emulation.setDeviceMetricsOverride({ width: 1400, height: 900, deviceScaleFactor: 1, mobile: false });
        await boot();
        await Page.removeScriptToEvaluateOnNewDocument({ identifier: clean.identifier });

        // ── Start, leave, come back ──
        await openKnowledge();
        await until(`/No distilled knowledge yet/.test((document.getElementById('team-knowledge-view') || {}).textContent || '')`, 'the empty knowledge view');
        check('no knowledge yet: the empty view with Distill', /No distilled knowledge yet/.test(await panelText()));
        await ev(`[...document.querySelectorAll('#team-knowledge-view button')].find(b => /Distill/.test(b.textContent)).click(); true`);
        await until(`window.__posts === 1 && /Starting knowledge distillation/.test(document.getElementById('team-knowledge-view').textContent)`, 'the run started');
        check('Distill starts one run and shows its progress', (await ev('window.__posts')) === 1 && /Starting knowledge distillation/.test(await panelText()));

        await leave();
        await until(`!!document.querySelector('.session-group-header .team-distill-chip')`, 'the Distilling chip');
        check('away from the team: its header says Distilling…', await chip());
        await ev(`window.__distill.progress = { phase: 'extracting', agent: 'lead', total: 4, completed: 2 }; window.__bundleReads = 0; true`);
        await openKnowledge();
        await until(`/Pass 1: Extracting knowledge/.test((document.getElementById('team-knowledge-view') || {}).textContent || '')`, 'the progress on return');
        const back = await panelText();
        check('back on the Knowledge tab: the run in progress is shown', /Pass 1: Extracting knowledge — lead/.test(back) && /2\/4 agents/.test(back), back.slice(0, 160));
        check('coming back started no second run', (await ev('window.__posts')) === 1);
        check('while it runs, the tab shows the run and does not load the old knowledge', (await ev('window.__bundleReads')) === 0, String(await ev('window.__bundleReads')));

        // ── It completes while you are elsewhere ──
        await leave();
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__bundle = ${JSON.stringify(BUNDLE)}; window.__distill = { team_name: 'alpha', status: 'complete', started_at: '2026-10-14T06:00:00Z', finished_at: '2026-10-14T06:05:00Z' }; true`);
        await until(`[...document.querySelectorAll('.toast .toast-message')].some(t => /Knowledge for alpha is ready/.test(t.textContent))`, 'the ready toast');
        check('completed elsewhere: a toast says the knowledge is ready', (await toasts()).some(t => /Knowledge for alpha is ready/.test(t)), JSON.stringify(await toasts()));
        await until(`!document.querySelector('.session-group-header .team-distill-chip')`, 'the chip gone');
        check('the Distilling chip is gone', !(await chip()));
        await openKnowledge();
        await until(`!!document.querySelector('#team-knowledge-view .knowledge-viewer')`, 'the bundle');
        const done = await panelText();
        check('the tab shows the knowledge, with when it was updated', /What the team knows/.test(done) && /Updated /.test(done), done.slice(0, 160));

        // ── A partial run (QA's #3325 note 2): complete, but some agents couldn't be distilled ──
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); true`);
        await ev(`[...document.querySelectorAll('#team-knowledge-view button')].find(b => b.classList.contains('knowledge-redistill-btn')).click(); true`);
        await until(`window.__posts === 2`, 'the partial re-distill');
        await ev(`window.__distill = { team_name: 'alpha', status: 'complete', finished_at: '2026-10-14T06:07:00Z', failed_agents: [{ agent: 'lead', error: 'claude CLI failed: exit status 1' }, { agent: 'qa', error: '' }], collaboration_error: 'claude CLI timed out after 2m0s' }; true`);
        await until(`!!document.querySelector('#team-knowledge-view .knowledge-run-note.is-partial')`, 'the partial note');
        const partial = await ev(`document.querySelector('#team-knowledge-view .knowledge-run-note').textContent`);
        check('partial: the note names the agents that could not be distilled, and why',
            /2 agents couldn't be distilled: lead \(claude CLI failed: exit status 1\); qa\. They keep their earlier knowledge, if any/.test(partial), partial);
        check('partial: and the failed collaboration pass', /The collaboration pass failed \(claude CLI timed out after 2m0s\)/.test(partial), partial);
        check('partial: the knowledge is still shown', /What the team knows/.test(await panelText()));
        await until(`[...document.querySelectorAll('.toast .toast-message')].some(t => /couldn't be distilled/.test(t.textContent))`, 'the partial toast');
        check('partial: the toast says it is ready, but 2 agents could not be distilled',
            (await toasts()).some(t => /Knowledge for alpha is ready, but 2 agents couldn't be distilled/.test(t)), JSON.stringify(await toasts()));

        // ── A failure with earlier knowledge: that knowledge, with a note ──
        await ev(`[...document.querySelectorAll('#team-knowledge-view button')].find(b => b.classList.contains('knowledge-redistill-btn')).click(); true`);
        await until(`window.__posts === 3`, 'the re-distill');
        await ev(`window.__distill = { team_name: 'alpha', status: 'failed', error: 'claude exited 1', finished_at: '2026-10-14T06:10:00Z' }; true`);
        await until(`/The last distillation failed: claude exited 1/.test((document.getElementById('team-knowledge-view') || {}).textContent || '')`, 'the failure note');
        const failedOld = await panelText();
        check('failed with earlier knowledge: that knowledge, and a note that the last run failed', /What the team knows/.test(failedOld) && /The last distillation failed: claude exited 1/.test(failedOld), failedOld.slice(0, 200));
        check('a failure toast too', (await toasts()).some(t => /Knowledge distillation for alpha failed: claude exited 1/.test(t)), JSON.stringify(await toasts()));

        // ── A failure with no knowledge yet: the error and Retry ──
        await ev(`window.__bundle = { exists: false }; true`);
        await leave();
        await openKnowledge();
        await until(`/Distillation failed: claude exited 1/.test((document.getElementById('team-knowledge-view') || {}).textContent || '')`, 'the failure view');
        check('failed with no knowledge: the error and Retry', /Distillation failed: claude exited 1/.test(await panelText()) && (await ev(`[...document.querySelectorAll('#team-knowledge-view button')].some(b => b.textContent.trim() === 'Retry')`)));
        await ev(`[...document.querySelectorAll('#team-knowledge-view button')].find(b => b.textContent.trim() === 'Retry').click(); true`);
        await until(`window.__posts === 4`, 'Retry starts a run');
        check('Retry starts a run', (await ev('window.__posts')) === 4);

        // ── A run in progress is found after a reload, without opening the tab ──
        await ev(`window.__distill.progress = { phase: 'formatting', total: 4, completed: 4 }; window.__save(); true`);
        await Page.reload({}); await Page.loadEventFired();
        await boot();
        await until(`!!document.querySelector('.session-group-header .team-distill-chip')`, 'the chip after a reload');
        check('after a reload the running distillation is found: the header says Distilling…', await chip());
        await ev(`window.__distill = { team_name: 'alpha', status: 'complete', finished_at: '2026-10-14T06:20:00Z' }; window.__save(); true`);
        await until(`[...document.querySelectorAll('.toast .toast-message')].some(t => /Knowledge for alpha is ready/.test(t.textContent))`, 'the ready toast after the reload');
        check('and it is followed to the end (toast)', (await toasts()).some(t => /Knowledge for alpha is ready/.test(t)));

        // ── The server restarts mid-run: the status goes back to 'none' ──
        await ev(`document.querySelectorAll('.toast').forEach(t => t.remove()); window.__distill = { team_name: 'alpha', status: 'running', progress: { phase: 'extracting', agent: 'lead', total: 4, completed: 1 } }; window.__save(); true`);
        await ev(`(async () => { const m = await import((window.__coralStatic||'/static/')+'knowledge_distill.js'); await m.refreshDistill('alpha'); return true; })()`);
        await until(`!!document.querySelector('.session-group-header .team-distill-chip')`, 'the chip for the new run');
        await ev(`window.__distill = { status: 'none' }; window.__save(); true`);
        await until(`[...document.querySelectorAll('.toast .toast-message')].some(t => /was interrupted/.test(t.textContent))`, 'the interrupted toast');
        check('lost to a restart: a toast says it was interrupted', (await toasts()).some(t => /The distillation for alpha was interrupted \(Coral restarted\); start it again/.test(t)), JSON.stringify(await toasts()));
        await until(`!document.querySelector('.session-group-header .team-distill-chip')`, 'the chip to go');
        check('and the Distilling chip is gone', !(await chip()));
        check('no page exceptions', exceptions.length === 0, JSON.stringify(exceptions));
    } finally {
        await client.close();
    }
})().then(() => {
    const failed = results.filter(r => r === 'FAIL').length;
    console.log(`\n${results.length - failed}/${results.length} checks passed`);
    process.exit(failed ? 1 : 0);
}).catch(err => { console.error(err); process.exit(1); });
