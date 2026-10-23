'use strict';
// The data the screens that are empty on a fresh server need, shared by the touch-target census (phone) and the contrast census (desktop): a History chat, workflows with a run, scheduled jobs
// (the paths the dashboard's own suites stub), and httpJson against the isolated server.
const BASE = require('../test_base').BASE;
// Data for the screens that are empty on a fresh server: a History chat, a workflow with a run, a scheduled job (the paths the dashboard's own suites stub).
const WF = { id: 7, name: 'Nightly build', description: 'Builds nightly', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const WF2 = { id: 8, name: 'Weekly report', description: 'Weekly', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] }, WF3 = { id: 9, name: 'Release check', description: 'Release', enabled: false, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const RUN = { id: 31, workflow_id: 7, workflow_name: 'Nightly build', status: 'failed', trigger_type: 'manual', started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z',
    steps: [{ index: 0, name: 's1', type: 'shell', status: 'failed', exit_code: 1, output_tail: 'boom' }] };
const JOB = { id: 5, name: 'Hourly job', cron_expr: '0 * * * *', enabled: true, agent_type: 'claude', prompt: 'p', repo_path: '/tmp', consecutive_failures: 0 };
const JOB2 = { ...JOB, id: 6, name: 'Daily job', cron_expr: '0 9 * * *' }, JOB3 = { ...JOB, id: 7, name: 'Weekly job', cron_expr: '0 9 * * 1', enabled: false };
const DATA_WORLD = `(() => {
    const inner = window.fetch;
    const json = b => Promise.resolve(new Response(JSON.stringify(b), { headers: { 'content-type': 'application/json' } }));
    window.fetch = (url, opts = {}) => {
        const u = String(url && url.url || url).replace(/^https?:\\/\\/[^/]+/, ''), path = u.split('?')[0], m = (opts.method || 'GET').toUpperCase();
        if (m === 'GET') {
            // the agent's changed files: two, on every OS (the Files panel's rows must each name their buttons, which one row cannot show; #6152)
            if (/^\\/api\\/sessions\\/live\\/[^/]+\\/files$/.test(path)) return json({ files: [{ filepath: 'src/main.go', status: 'M' }, { filepath: 'docs/README.md', status: '??' }], is_git: true });
            if (path === '/api/workflows') return json({ workflows: [${JSON.stringify(WF)}, ${JSON.stringify(WF2)}, ${JSON.stringify(WF3)}] });
            if (path === '/api/workflows/7') return json(${JSON.stringify(WF)});
            if (/^\\/api\\/workflows\\/7\\/runs/.test(path)) return json({ runs: [${JSON.stringify(RUN)}] });
            if (path === '/api/workflows/runs/31') return json(${JSON.stringify(RUN)});
            if (path === '/api/scheduled/jobs') return json({ jobs: [${JSON.stringify(JOB)}, ${JSON.stringify(JOB2)}, ${JSON.stringify(JOB3)}] });
            if (path === '/api/scheduled/jobs/5') return json(${JSON.stringify(JOB)});
            if (/^\\/api\\/scheduled\\/jobs\\/5\\/runs/.test(path)) return json({ runs: [] });
            if (path === '/api/sessions/history') return json({ sessions: [{ session_id: 'census-chat', summary_title: 'Release notes chat', source_type: 'claude' }, { session_id: 'census-chat-2', summary_title: 'Build fix chat', source_type: 'claude' }, { session_id: 'census-chat-3', summary_title: 'Planning chat', source_type: 'codex' }], total: 3, page: 1, page_size: 50 });
            if (path === '/api/sessions/history/census-chat') return json({ agent_type: 'claude', messages: [{ type: 'user', content: 'hello' }, { type: 'assistant', text: 'Hi there.' }], total: 2, start: 0 });
            if (path === '/api/sessions/history/census-chat/notes') return json({ notes_md: 'A note' });
            if (path === '/api/sessions/history/census-chat/git') return json({ commits: [] });
            if (/^\\/api\\/sessions\\/history\\/census-chat\\/(tags|events|tasks|agent-notes|files)$/.test(path) || path === '/api/tags') return json([]);
        }
        return inner(url, opts);
    };
})();`;
async function httpJson(method, p, body) { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; }

module.exports = { WF, RUN, JOB, DATA_WORLD, httpJson };
