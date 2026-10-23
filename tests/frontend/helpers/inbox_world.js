// The stand-in routes the Inbox's Tasks for you and Saved for later panels read (three open tasks, four saved items), over the real isolated server: the page's fetch answers these and passes every other route on (shared by the Inbox suites).
const INBOX_WORLD = `(() => {
    const real = window.fetch; window.__reqs = []; window.__pwn = undefined;
    const ago = ms => new Date(Date.now() - ms).toISOString();
    const TASKS = [
        { id: 1, board_id: 'team-a', board_label: 'team-a', title: 'Pick the release date', priority: 'high', status: 'pending', created_by: 'Orchestrator', created_at: ago(7200000), updated_at: ago(7200000), revision: 1 },
        { id: 2, board_id: 'team-a', board_label: 'team-a', title: 'Check <img src=x onerror="window.__pwn=1"> the logo', priority: 'medium', status: 'in_progress', created_by: 'Designer', created_at: ago(86400000 * 3), updated_at: ago(86400000 * 3) },
        { id: 3, board_id: 'team-b', board_label: 'team-b', title: 'Approve the budget', priority: 'low', status: 'review_pending', created_by: 'Lead', created_at: ago(60000), updated_at: ago(60000) },
    ];
    const BODY = { 1: 'Which day?\\n\\n- **Monday** the 4th\\n- Friday the 8th\\n\\nSee [the plan](https://example.com/plan) and [click](javascript:alert(1)).\\n\\n<script>window.__pwn=1</script>\\n\\n![t](https://evil.example/p.png)',
        2: 'Plain text.', 3: '' };
    const json = (b, status = 200) => Promise.resolve(new Response(JSON.stringify(b), { status, headers: { 'content-type': 'application/json' } }));
    const SAVED = [11, 12, 13, 14].map(id => ({ id, kind: 'agent', ref: 'sess-' + id, key: String(id).padStart(20, '0'), title: 'Agent ' + id, snippet: 'snippet ' + id, agent_type: 'claude', saved_at: ago(id * 1000), said_at: ago(id * 2000) }));
    const MSG = { 11: { text: 'The **whole** message\\n\\n- a\\n- b\\n\\n<b>not bold</b>', partial: false, reason: '' }, 12: { text: '', partial: false, reason: 'gone' },
        13: { text: 'Part of a very long message', partial: true, reason: '' }, 14: null };
    window.fetch = (u, o = {}) => {
        const url = new URL(String(u), location.origin), m = (o.method || 'GET').toUpperCase(), p = url.pathname;
        window.__reqs.push({ m, p, q: url.search, body: o.body ? String(o.body) : null });
        if (p === '/api/board/tasks' && m === 'GET') return json({ tasks: TASKS, total: TASKS.length });
        let r = /^\\/api\\/board\\/([^/]+)\\/tasks\\/(\\d+)$/.exec(p);
        if (r && m === 'GET' && window.__failTask === Number(r[2])) return json({ error: 'the board is busy' }, 500);
        if (r && m === 'GET') { const t = TASKS.find(x => x.id === Number(r[2])); return t ? json({ ...t, body: BODY[t.id] }) : json({ error: 'task not found' }, 404); }
        r = /^\\/api\\/board\\/([^/]+)\\/tasks\\/(\\d+)\\/reply$/.exec(p);
        if (r && m === 'POST') {
            const id = Number(r[2]);
            if (id === 1) return json({ ok: true, message_id: 9, task_id: 1, notified: ['Orchestrator'] });
            if (id === 2) return json({ ok: true, message_id: 10, task_id: 2, notified: [], warnings: ['creator_not_reachable'] });
            return json({ error: 'Your Coral Hub access is view only.' }, 403);
        }
        if (p === '/api/saved' && m === 'GET') return json({ items: SAVED, open: SAVED.length, done: 0 });
        r = /^\\/api\\/saved\\/(\\d+)\\/message$/.exec(p);
        if (r && m === 'GET') { const x = MSG[Number(r[1])]; return x ? json(x) : json({ error: 'the transcript could not be read' }, 500); }
        r = /^\\/api\\/artifacts\\/([0-9a-f]{64})\\/meta$/.exec(p);
        if (r && m === 'GET') {
            const c = r[1][0];
            if (c === 'a') return json({ id: r[1] });
            if (c === 'b') return json({ error: 'not yours' }, 403);
            if (c === 'c') return json({ error: 'not found' }, 404);
            return Promise.reject(new TypeError('network'));
        }
        if (p.startsWith('/api/sessions/history/')) return json({ found: true, index: 1 });
        return real(u, o);
    };
    return true; })()`;

module.exports = { INBOX_WORLD };
