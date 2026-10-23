'use strict';
// The fixture of the comments scale suites (#5255): a file and 500 comments on it, and the page script that stubs the artifact and its comments routes to serve them.
//   fixture('markdown'): about 1 MB of Markdown, 2,600 paragraphs under a heading every 25; fixture('code'): a 5,000-line source file.
// 400 threads, each on a quote found once in the file, spread evenly through it; 100 replies; every fifth thread resolved.

const THREADS = 400, REPLIES = 100;

function build(kind) {
    const lines = [];
    const quotes = [];
    if (kind === 'markdown') {
        const filler = 'The backfill reads a batch, writes the new columns and commits, so a long run holds the write lock only for one batch at a time and the readers carry on with the old columns until the flag flips.';
        for (let i = 0; i < 2600; i++) {
            if (i % 25 === 0) lines.push(`## Section ${i / 25}`, '');
            const marker = `Marker ${String(i).padStart(4, '0')} says the lock is taken once`;
            lines.push(`${marker}. ${filler} ${filler.slice(0, 60 + (i % 90))}.`, '');
            quotes.push(marker);
        }
        return { filename: 'plan.md', content: lines.join('\n'), quotes };
    }
    for (let i = 0; i < 5000; i++) {
        const stmt = `result_${i} := compute(${i}, "tag-${i}")`;
        lines.push(`\t${stmt} // step ${i} of the migration`);
        quotes.push(stmt);
    }
    return { filename: 'migrate.go', content: lines.join('\n') + '\n', quotes };
}

/** { filename, content, size, comments } with the comments in the server's own shape (snake_case, as the stub answers them). */
function fixture(kind) {
    const f = build(kind);
    const comments = [];
    const t0 = Date.parse('2026-10-19T00:00:00Z');
    for (let i = 0; i < THREADS; i++) {
        const q = f.quotes[Math.floor(i * f.quotes.length / THREADS)];
        const c = { id: i + 1, parent_id: null, anchor: { quote: q, prefix: '', suffix: '', start_line: null, end_line: null }, body: `Thread ${i}: please check this one before the backfill runs.`, author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date(t0 + i * 60000).toISOString() };
        if (i % 5 === 0) c.resolved_at = new Date(t0 + i * 60000 + 1000).toISOString();
        comments.push(c);
    }
    for (let i = 0; i < REPLIES; i++) comments.push({ id: THREADS + i + 1, parent_id: (i * 4) % THREADS + 1, anchor: null, body: `Reply ${i}: agreed.`, author: { kind: 'hub', issuer: 'coral-hub', subject: 'hub-user-3f2a', name: 'Reviewer' }, created_at: new Date(t0 + (THREADS + i) * 60000).toISOString() });
    return { filename: f.filename, content: f.content, size: f.content.length, comments, mediaType: kind === 'markdown' ? 'text/markdown' : 'text/plain' };
}

const HEX = 'e'.repeat(64);

/** The page script: the artifact's bytes and its comments routes, answered from `fx` (the owner may do everything; a post is added to the list). */
function STUB(fx) {
    return `(() => {
        const real = window.fetch; window.__HEX = ${JSON.stringify(HEX)}; window.__cs = ${JSON.stringify(fx.comments)}; window.__next = 100000; window.__calls = 0;
        const json = (o, status) => Promise.resolve(new Response(JSON.stringify(o), { status: status || 200, headers: { 'Content-Type': 'application/json' } }));
        const can = c => ({ reply: c.parent_id == null, edit: true, delete: true, resolve: c.parent_id == null });
        const view = c => ({ ...c, deleted: false, mine: true, can: can(c) });
        window.fetch = (u, o) => {
            const url = String(u), method = (o && o.method) || 'GET';
            // a board with agents for the Send to agent picker (#6475): one that can be asked, one busy, one asleep, one not running
            if (/\\/api\\/board\\/[^/]+\\/status/.test(url)) return json({ agents: [
                { name: 'QA', role: 'QA Engineer', agent_type: 'claude', availability: 'available' }, { name: 'Backend', role: 'Backend Dev', agent_type: 'codex', availability: 'busy' },
                { name: 'Sleepy', role: 'UI Dev', agent_type: 'claude', availability: 'sleeping' }, { name: 'Gone', role: 'Docs Writer', agent_type: 'claude', availability: 'offline' }],
                summary: {}, unassigned_tasks: [], health_report: [], observed_at: new Date().toISOString() });
            if (url.includes('/api/artifacts/' + window.__HEX + '/comments')) {
                window.__calls++;
                if (method === 'GET') return json({ comments: window.__cs.map(view), locked: false, you: { kind: 'owner', name: 'Operator', can_comment: true, reason: '', can_lock: true }, limits: { body: 8192, quote: 2048, comments: 500 } });
                if (method === 'POST') { const b = JSON.parse(o.body); const c = { id: window.__next++, parent_id: b.parent_id == null ? null : b.parent_id, anchor: b.anchor, body: b.body, author: { kind: 'owner', issuer: 'local', subject: 'Operator' }, created_at: new Date().toISOString() }; window.__cs.push(c); return json(view(c), 201); }
                return json({ error: 'nope' }, 404);
            }
            if (url.includes('/api/artifacts/' + window.__HEX)) return Promise.resolve(new Response(${JSON.stringify(fx.content)}, { status: 200, headers: { 'Content-Type': ${JSON.stringify(fx.mediaType)} } }));
            return real(u, o);
        };
        return true; })()`;
}

module.exports = { fixture, STUB, HEX, THREADS, REPLIES };
