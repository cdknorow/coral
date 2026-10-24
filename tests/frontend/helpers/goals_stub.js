// The goals routes as the dashboard's own page sees them (#7113): a page script that makes `fetch` answer a board's goals and a goal's tasks from fixtures, for the suites that show the Goals view before
// (or without) a server that has the routes: goals_view_browser and the keyboard, phone-target and contrast censuses' Goals scenes. Everything else goes to the real fetch.
const NOW = Date.now();
const iso = (agoMs) => new Date(NOW - agoMs).toISOString();
const GOALS = [
    { id: 1, title: 'Ship the new installer', outcome: 'Everyone can install in one step.', done_when: 'The installer test is green on three systems', state: 'active', driver: 'Orchestrator', updated_at: iso(0), rollup: { done: 5, active: 2, blocked: 1, queued: 1, total: 9 } },
    { id: 2, title: 'Fix the flaky suites', outcome: 'No suite is retried.', done_when: 'Ten runs in a row are green', state: 'blocked', blocked_on: 'the Operator', driver: 'QA', updated_at: iso(3600e3), rollup: { done: 1, active: 0, blocked: 1, queued: 0, total: 2 }, decision: 'Spend a day on the slowest suite?', recommendation: 'Yes: it fails one run in ten.', if_silent: 'The suite stays as it is.' },
    { id: 3, title: 'Docs pass', state: 'proposed', rollup: { done: 0, active: 0, blocked: 0, queued: 0, total: 0 }, updated_at: iso(86400e3) },
    { id: 4, title: 'Old cleanup', state: 'done', rollup: { done: 3, active: 0, blocked: 0, queued: 0, total: 3 }, updated_at: iso(5 * 86400e3) },
    { id: 5, title: 'A retired idea', state: 'dropped', rollup: { done: 0, active: 0, blocked: 0, queued: 0, total: 0 }, updated_at: iso(9 * 86400e3) },
];
const TASKS = { 1: [{ id: 101, title: 'Build the installer', assigned_to: 'goal-qa', status: 'in_progress', status_line: 'signing' }, { id: 102, title: 'Test on Windows', assigned_to: 'nobody-here', status: 'blocked' }, { id: 103, title: 'Write notes', status: 'pending' }], 2: [] };

/** The page script: installs the stub (window.__goals, window.__tasks, window.__goalsMode ok|fail|refuse, window.__goalsPosts). */
const GOALS_STUB = `(() => { const real = window.fetch; window.__goalsMode = 'ok'; window.__goalsPosts = []; window.__goals = ${JSON.stringify(GOALS)}; window.__tasks = ${JSON.stringify(TASKS)};
    const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
    window.fetch = (u, o) => { const url = new URL(String(u), location.origin), m = (o && o.method) || 'GET';
        if (/^\\/api\\/board\\/[^/]+\\/goals$/.test(url.pathname)) {
            if (m === 'POST') { const b = JSON.parse(o.body); window.__goalsPosts.push(b); if (window.__goalsMode === 'refuse') return json({ error: 'title is too long', code: 'goal_text_too_long' }, 400); window.__goals = [{ id: 9, title: b.title, state: 'active', rollup: {}, updated_at: new Date().toISOString() }, ...window.__goals]; return json({ id: 9 }, 201); }
            if (window.__goalsMode === 'fail') return json({ error: 'boom' }, 500);
            window.__goalsListQuery = url.search; return json({ goals: window.__goals, require_goal: window.__requireGoal === true }); }
        if (/^\\/api\\/board\\/[^/]+\\/subscribers$/.test(url.pathname)) return json([{ subscriber_id: 'qa-eng', job_title: 'QA' }, { subscriber_id: 'orch', job_title: 'Orchestrator' }, { subscriber_id: 'orch', job_title: 'Orchestrator' }]);
        const t = /^\\/api\\/board\\/[^/]+\\/tasks$/.exec(url.pathname); if (t && url.searchParams.get('goal')) return json({ tasks: window.__tasks[url.searchParams.get('goal')] || [], next_cursor: '', totals: {} });
        return real(u, o); }; return true; })()`;

module.exports = { GOALS, TASKS, GOALS_STUB };
