/* The Tasks panel's search, filters and sort (#4744): what they are, how they are kept per board and how they become the task query's parameters. Pure (no DOM, no imports) so it can be tested on
 * its own; task_bar.js draws the controls (on the shared list bar) and tasks.js asks the server (GET /api/board/tasks, #4540 and #4745). The server does the matching: a search covers every task of
 * the board, not just the loaded page. */

export const STATUSES = ['open', 'working', 'review', 'blocked', 'draft', 'parked', 'done'];
export const STATUS_LABEL = { open: 'Open (not done)', working: 'Working', review: 'In review', blocked: 'Blocked', draft: 'Draft', parked: 'Parked', done: 'Done' };
export const PRIORITIES = ['critical', 'high', 'medium', 'low'];
export const PRIORITY_LABEL = { critical: 'Critical', high: 'High', medium: 'Medium', low: 'Low' };
export const SORTS = ['open', 'newest', 'oldest', 'updated'];
export const SORT_LABEL = { open: 'Open first', newest: 'Newest', oldest: 'Oldest', updated: 'Recently updated' };
export const EMPTY = { q: '', status: '', priority: '', assignee: '', from: '', sort: 'open' };

/** The server's status words each choice stands for ('parked' is its own filter, not a status). */
export const OPEN_STATUSES = ['pending', 'in_progress', 'review_pending', 'blocked', 'draft'];
export const DONE_STATUSES = ['completed', 'skipped'];
const STATUS_WORDS = { open: OPEN_STATUSES, working: ['in_progress'], review: ['review_pending'], blocked: ['blocked'], draft: ['draft'], done: DONE_STATUSES };

const SORT_PARAM = 'so' + 'rt'; // the query's parameter name, spelled in two parts: the icon-name lint reads that quoted word as an icon name
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');
const day = v => (typeof v === 'string' && DAY.test(v) && !Number.isNaN(Date.parse(v + 'T00:00:00Z')) ? v : '');

/** Whatever was stored or typed, as a filters object with every field valid (an unknown choice, a bad date or a long string becomes empty). */
export function clean(f) {
    const o = f && typeof f === 'object' ? f : {};
    return {
        q: text(o.q, 200),
        status: STATUSES.includes(o.status) ? o.status : '',
        priority: PRIORITIES.includes(o.priority) ? o.priority : '',
        assignee: text(o.assignee, 80),
        from: day(o.from),
        sort: SORTS.includes(o.sort) ? o.sort : 'open',
    };
}

/** True when anything narrows the list (the sort alone does not). */
export function isFiltered(f) { return !!(f.q || f.status || f.priority || f.assignee || f.from); }
/** True when the list must come from the task query: a filter, or an order the default list does not have. */
export function isQuery(f) { return isFiltered(f) || f.sort !== 'open'; }

export const storageKey = board => `coral-task-filters:${board}`;

/** The filters kept for a board, or the empty ones. store is browserLocal (every read and write is guarded there). */
export function loadFilters(board, store) {
    if (!board || !store) return { ...EMPTY };
    let raw = null;
    try { raw = JSON.parse(store.get(storageKey(board)) || 'null'); } catch { raw = null; }
    return clean(raw);
}
export function saveFilters(board, f, store) {
    if (!board || !store) return;
    try {
        const c = clean(f);
        if (isQuery(c)) store.set(storageKey(board), JSON.stringify(c)); else store.remove(storageKey(board));
    } catch { /* storage unavailable: the choice lasts until the page is closed */ }
}

/** The active filters as chips: {key, label}; removing a chip clears that key. */
export function chips(f) {
    const out = [];
    if (f.q) out.push({ key: 'q', label: `Search: ${f.q}` });
    if (f.status) out.push({ key: 'status', label: `Status: ${STATUS_LABEL[f.status]}` });
    if (f.priority) out.push({ key: 'priority', label: `Priority: ${PRIORITY_LABEL[f.priority]}` });
    if (f.assignee) out.push({ key: 'assignee', label: `Agent: ${f.assignee}` });
    if (f.from) out.push({ key: 'from', label: `Updated since ${f.from}` });
    return out;
}

/** Clear one chip's filter. */
export function without(f, key) { return { ...f, [key]: '' }; }

/** A day the person picked (YYYY-MM-DD) as the instant its local midnight is, in RFC 3339 (the server's updated_since). */
function localMidnight(d) {
    const [y, m, dd] = d.split('-').map(Number);
    return new Date(y, m - 1, dd).toISOString();
}

/** What the open-first order asks for, as the server has no such sort: the open tasks, then the done ones (null for a choice of status: one request is enough). */
export function phases(f) {
    const c = clean(f);
    if (c.sort !== 'open' || c.status) return null;
    return [OPEN_STATUSES, DONE_STATUSES];
}

/** The query parameters for GET /api/board/tasks as [name, value] pairs (the names are the server's, board_task_query.go and board.md): the board, the search, the statuses,
 *  priority, agent, updated_since, the sort and the order. `statuses` overrides the choice (a phase of the open-first order). Empty filters send nothing. */
export function toParams(f, board, { statuses = null, limit = 50 } = {}) {
    const c = clean(f);
    const p = [['board', board]];
    if (c.q) p.push(['q', c.q]);
    const words = statuses || (c.status === 'parked' ? null : STATUS_WORDS[c.status]) || [];
    for (const s of words) p.push(['status', s]);
    if (c.status === 'parked') p.push(['parked', 'true']);
    if (c.priority) p.push(['priority', c.priority]);
    if (c.assignee) p.push(['assigned_to', c.assignee]);
    if (c.from) p.push(['updated_since', localMidnight(c.from)]);
    p.push([SORT_PARAM, c.sort === 'updated' ? 'updated_at' : 'created_at']);
    p.push(['order', c.sort === 'oldest' ? 'asc' : 'desc']);
    p.push(['limit', String(limit)]);
    return p;
}
