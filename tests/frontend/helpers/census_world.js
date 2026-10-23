'use strict';
// The data the screens that are empty on a fresh server need, shared by the touch-target census (phone) and the contrast census (desktop): a History chat, workflows with a run, scheduled jobs
// (the paths the dashboard's own suites stub), and httpJson against the isolated server.
const BASE = require('../test_base').BASE;
// Data for the screens that are empty on a fresh server: a History chat, a workflow with a run, a scheduled job (the paths the dashboard's own suites stub).
const WF = { id: 7, name: 'Nightly build', description: 'Builds nightly', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const WF2 = { id: 8, name: 'Weekly report', description: 'Weekly', enabled: true, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] }, WF3 = { id: 9, name: 'Release check', description: 'Release', enabled: false, steps: [{ name: 's1', type: 'shell', command: 'echo hi' }] };
const RUN = { id: 31, workflow_id: 7, workflow_name: 'Nightly build', status: 'failed', trigger_type: 'manual', started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:01:00Z',
    steps: [{ index: 0, name: 'Install', type: 'shell', status: 'completed', exit_code: 0, output_tail: 'added 214 packages', files: ['stdout.txt'], started_at: '2026-10-15T10:00:00Z', finished_at: '2026-10-15T10:00:20Z' },
        { index: 1, name: 'Test', type: 'shell', status: 'failed', exit_code: 3, output_tail: 'boom', error_tail: 'npm ERR! Test failed\nnpm ERR! exit code 3', files: ['stdout.txt', 'stderr.txt'], started_at: '2026-10-15T10:00:20Z', finished_at: '2026-10-15T10:01:00Z' },
        { index: 2, name: 'Review', type: 'agent', status: 'skipped' }] };
// A run that passed, with an agent step that has its conversation (a "View agent" link) and files: the run page when nothing went wrong (#6639).
const RUN_OK = { id: 32, workflow_id: 7, workflow_name: 'Nightly build', status: 'completed', trigger_type: 'schedule', started_at: '2026-10-16T10:00:00Z', finished_at: '2026-10-16T10:02:00Z',
    steps: [{ index: 0, name: 'Install', type: 'shell', status: 'completed', exit_code: 0, output_tail: 'added 214 packages', files: ['stdout.txt'], started_at: '2026-10-16T10:00:00Z', finished_at: '2026-10-16T10:00:20Z' },
        { index: 1, name: 'Review', type: 'agent', status: 'completed', session_id: 'census-run-agent', session_name: 'census-run-agent', files: ['summary.md'], started_at: '2026-10-16T10:00:20Z', finished_at: '2026-10-16T10:02:00Z' }] };
// A run that is still going: the page re-reads it every few seconds (#6639).
const RUN_LIVE = { id: 34, workflow_id: 7, workflow_name: 'Nightly build', status: 'running', trigger_type: 'manual', started_at: '2026-10-17T10:00:00Z',
    steps: [{ index: 0, name: 'Install', type: 'shell', status: 'completed', exit_code: 0, output_tail: 'added 214 packages', started_at: '2026-10-17T10:00:00Z', finished_at: '2026-10-17T10:00:20Z' },
        { index: 1, name: 'Test', type: 'shell', status: 'running', output_tail: 'running 12 of 40', started_at: '2026-10-17T10:00:20Z' }] };
// A verified update that is ready to install, with two agents the relaunch would restart (#6315): what GET /api/system/update answers once the scene sets window.__updStatus.
const UPDATE_STAGED = { enabled: true, installed: '1.4.159', state: 'staged', staged: { version: '1.4.160', staged_at: '2026-10-23T03:00:00Z', days_ready: 0, verified: true, source: 'subgentic/coral' },
    relaunch: { available: true, reason: '', hub_allowed: false, tmux_agents_restart: false, ack: 'ack-1', restarts: [{ session_id: 'census-r1', name: 'release-agent', state: 'working', resumes: true }, { session_id: 'census-r2', name: 'docs-agent', state: 'waiting', resumes: false }] },
    last_result: null, checked_at: '2026-10-23T03:05:00Z', auto: true };
// This computer linked to a Coral Hub (the sentence under the relaunch switch, #6306, is only there then): what GET /api/cloud/status answers once the scene sets window.__cloudStatus.
// Not connected yet, and waiting for the approval with a code on screen (#6579): what GET /api/cloud/status answers once the scene sets window.__cloudStatus.
const CLOUD_IDLE = { state: 'idle', configured: true, hub_url: 'https://hub.example.invalid', url: 'https://hub.example.invalid', max_mode: 'view', max_mode_saved: false };
const CLOUD_WAITING = { state: 'enrolling', configured: true, hub_url: 'https://hub.example.invalid', url: 'https://hub.example.invalid', user_code: 'WXYZ-1234', verification_url: 'https://hub.example.invalid/connect?code=WXYZ-1234', max_mode: 'view', max_mode_saved: true };
const CLOUD_LINKED = { state: 'connected', configured: true, url: 'https://hub.example.invalid', max_mode: 'launch', mode: 'launch', hub_relaunch_updates: true, allow_hub_auto_accept: true, redact_tokens: true, hub_cross_team_settings: true };
// Analytics with something in it (#6432): the Cost view's summary, tables, chart, teams, branches and call metrics, so the screen is walked, measured and listened to with data, not as "No data".
const TOKEN_USAGE = { totals: { input_tokens: 12400, output_tokens: 2100, cache_read_tokens: 800, cache_write_tokens: 300, num_sessions: 5, cost_usd: 6.4 },
    by_agent_type: [{ agent_type: 'claude', num_sessions: 4, input_tokens: 12000, output_tokens: 2000, cache_read_tokens: 800, cache_write_tokens: 300, cost_usd: 6.1 }, { agent_type: 'codex', num_sessions: 1, input_tokens: 400, output_tokens: 100, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.3 }],
    by_model: [{ model: 'claude-opus-5-5', agent_type: 'claude', num_sessions: 3, input_tokens: 9000, output_tokens: 1500, cache_read_tokens: 800, cache_write_tokens: 300, cost_usd: 5.0 }, { model: 'claude-sonnet-5-5', agent_type: 'claude', num_sessions: 1, input_tokens: 3000, output_tokens: 500, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 1.1 }, { model: 'gpt-5', agent_type: 'codex', num_sessions: 1, input_tokens: 400, output_tokens: 100, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.3 }],
    by_thinking: [{ agent_type: 'claude', model: 'claude-opus-5-5', thinking: 'high', input_tokens: 6000, output_tokens: 900, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 4000, cost_usd: 3.0, num_calls: 9 }, { agent_type: 'claude', model: 'claude-sonnet-5-5', thinking: 'low', input_tokens: 3000, output_tokens: 500, cache_read_tokens: 0, cache_write_tokens: 0, thinking_tokens: 0, cost_usd: 1.1, num_calls: 4 }],
    by_agent: [{ session_id: 'cost-a1', agent_name: 'Frontend Dev', board_name: 'checkout-redesign', folder: 'shop-app', requests: 12, input_tokens: 5000, output_tokens: 700, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 3.2, first_seen: '2026-10-22T00:00:00Z', last_seen: '2026-10-22T01:00:00Z' }, { session_id: 'cost-a2', agent_name: 'QA Engineer', board_name: 'checkout-redesign', folder: 'shop-app', requests: 4, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.6, first_seen: '2026-10-22T00:00:00Z', last_seen: '2026-10-22T00:30:00Z' }] };
const TOKEN_TIMESERIES = { buckets: [0, 1, 2, 3, 4, 5].map(i => ({ bucket: `2026-10-22T0${i}:00:00Z`, timestamp: `2026-10-22T0${i}:00:00Z`, input_tokens: 1000 + i * 300, output_tokens: 200 + i * 40, cache_read_tokens: 100, cache_write_tokens: 50, num_requests: 3 + i, cost_usd: 0.5 + i * 0.2, cumulative_cost: 0.5 * (i + 1) + 0.1 * i * (i + 1) })) };
const TOKEN_TEAMS = { teams: [{ board_name: 'checkout-redesign', num_agents: 4, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 4.5 }, { board_name: 'docs-sprint', num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }] };
const TOKEN_BRANCHES = { branches: [{ repo_name: 'shop-app', branch: 'main', board_name: 'checkout-redesign', pr_number: 0, num_agents: 4, input_tokens: 9000, output_tokens: 1000, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 4.5 }, { repo_name: 'shop-app', branch: 'fix/address-step', board_name: 'docs-sprint', pr_number: 12, num_agents: 2, input_tokens: 900, output_tokens: 90, cache_read_tokens: 0, cache_write_tokens: 0, cost_usd: 0.9 }] };
const CALL_METRICS = { rows: [{ operation: 'POST /api/sessions/live/send', type: 'http', agent: 'Frontend Dev', team: 'checkout-redesign', calls: 14, errors: 1, avg_ms: 220 }, { operation: 'GET /api/board/messages', type: 'http', agent: 'QA Engineer', team: 'checkout-redesign', calls: 40, errors: 0, avg_ms: 35 }] };
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
            if (/^\\/api\\/workflows\\/7\\/runs/.test(path)) return json({ runs: [${JSON.stringify(RUN)}, ${JSON.stringify(RUN_OK)}] });
            if (path === '/api/workflows/runs/31') return json(${JSON.stringify(RUN)});
            if (path === '/api/workflows/runs/32') return json(${JSON.stringify(RUN_OK)});
            if (path === '/api/workflows/runs/34') return json(${JSON.stringify(RUN_LIVE)});
            if (path === '/api/scheduled/jobs') return json({ jobs: [${JSON.stringify(JOB)}, ${JSON.stringify(JOB2)}, ${JSON.stringify(JOB3)}] });
            if (path === '/api/scheduled/jobs/5') return json(${JSON.stringify(JOB)});
            if (/^\\/api\\/scheduled\\/jobs\\/5\\/runs/.test(path)) return json({ runs: [] });
            if (path === '/api/sessions/history') return json({ sessions: [{ session_id: 'census-chat', summary_title: 'Release notes chat', source_type: 'claude' }, { session_id: 'census-chat-2', summary_title: 'Build fix chat', source_type: 'claude' }, { session_id: 'census-chat-3', summary_title: 'Planning chat', source_type: 'codex' }], total: 3, page: 1, page_size: 50 });
            if (path === '/api/cloud/status' && window.__cloudStatus) return json(window.__cloudStatus);
            if (path === '/api/token-usage/summary') return json(${JSON.stringify(TOKEN_USAGE)});
            if (path === '/api/token-usage/timeseries') return json(${JSON.stringify(TOKEN_TIMESERIES)});
            if (path === '/api/token-usage/by-team') return json(${JSON.stringify(TOKEN_TEAMS)});
            if (path === '/api/token-usage/by-branch') return json(${JSON.stringify(TOKEN_BRANCHES)});
            if (path === '/api/call-metrics/summary') return json(${JSON.stringify(CALL_METRICS)});
            // A send to the census's stand-in agent is answered at once. The real server holds it until the CLI's input box is up (the stand-in has none) and refuses it after a long wait, "The agent isn't ready for messages yet", which showed a composer error in the middle of whichever scene was running by then: the 160 gate's red in keyboard_census_desktop (#6524).
            if (m === 'POST' && /^\\/api\\/sessions\\/live\\/[^/]+\\/send$/.test(path)) return json({ status: 'sent' });
            if (path === '/api/system/update') return json(window.__updStatus || { enabled: false });
            if (path === '/api/sessions/history/search' && /[?&]q=zzz/.test(u)) return json({ status: 'complete', results: [] });
            if (path === '/api/sessions/history/search') return json({ status: 'complete', results: [{ session_id: 'census-chat', type: 'agent', hits: [{ role: 'user', excerpt: 'Please check the release notes', locator: { session_id: 'census-chat', message_index: 0 } }, { role: 'assistant', excerpt: 'The release notes are ready', locator: { session_id: 'census-chat', message_index: 1 } }] }, { session_id: 'census-chat-2', type: 'agent', hits: [{ role: 'user', excerpt: 'Fix the build before the release', locator: { session_id: 'census-chat-2', message_index: 0 } }] }] });
            if (path === '/api/sessions/history/census-chat') return json({ agent_type: 'claude', messages: [{ type: 'user', content: 'hello' }, { type: 'assistant', text: 'Hi there.' }], total: 2, start: 0 });
            if (path === '/api/sessions/history/census-chat/notes') return json({ notes_md: 'A note' });
            if (path === '/api/sessions/history/census-chat/git') return json({ commits: [] });
            if (/^\\/api\\/sessions\\/history\\/census-chat\\/(tags|events|tasks|agent-notes|files)$/.test(path) || path === '/api/tags') return json([]);
        }
        return inner(url, opts);
    };
})();`;
async function httpJson(method, p, body) { const r = await fetch(BASE + p, { method, headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) }); let data = null; try { data = await r.json(); } catch { /* no body */ } return { status: r.status, data }; }

module.exports = { WF, RUN, RUN_OK, RUN_LIVE, JOB, UPDATE_STAGED, CLOUD_LINKED, CLOUD_IDLE, CLOUD_WAITING, DATA_WORLD, httpJson };
