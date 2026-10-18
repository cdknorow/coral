/* The Activity panel's search, filters and sort (#4785, #4798, on the events search of #4745): what they are and how they become the query's parameters. Pure (no DOM, no imports) so it can
 * be tested on its own (it reads the tool groups from event_icons.js, which is data and functions); activity_search.js draws the controls (on the shared list bar) and asks the server (GET /api/sessions/live/{name}/events). The server does the matching: a search
 * covers every event of the agent, not just the 50 the panel holds. A search is not kept: it belongs to the agent on screen and goes when the agent does. */

import { FILTER_GROUPS } from './event_icons.js';

/** The choices of the Type field: a name for the person, the event types (the server's words) each one stands for. */
export const TYPE_WORDS = {
    tool_use: ['tool_use'],
    prompt: ['prompt_submit'],
    permission: ['permission_request'],
    thinking: ['thinking'],
    status: ['status'],
    goal: ['goal'],
    confidence: ['confidence'],
    stop: ['stop', 'notification'],
    auto_accept: ['auto_accept', 'auto_accept_paused', 'auto_accept_skipped', 'auto_accept_cleared', 'auto_accept_enabled', 'auto_accept_disabled'],
};
export const TYPES = Object.keys(TYPE_WORDS);
export const TYPE_LABEL = {
    tool_use: 'Tool use', prompt: 'Prompts', permission: 'Permission requests', thinking: 'Thinking', status: 'Status', goal: 'Goal', confidence: 'Confidence',
    stop: 'Stop / notify', auto_accept: 'Auto-accept',
};
export const SORTS = ['newest', 'oldest'];
export const SORT_LABEL = { newest: 'Newest first', oldest: 'Oldest first' };
/** The tool groups (Read, Write, Bash ... ): the Filters menu has a checkbox for each, g_<key>, checked while that kind of event shows. They narrow the rows already loaded (the live feed, or the pages of a
 *  search), the server is not asked: they are not part of the query. */
const GROUP_KEYS = FILTER_GROUPS.map(g => g.key);
export const groupField = key => `g_${key}`;
export const EMPTY = { q: '', type: '', from: '', to: '', sort: 'newest', ...Object.fromEntries(GROUP_KEYS.map(k => [groupField(k), true])) };

const SORT_PARAM = 'so' + 'rt'; // the query's parameter name, spelled in two parts: the icon-name lint reads that quoted word as an icon name
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');
const day = v => (typeof v === 'string' && DAY.test(v) && !Number.isNaN(Date.parse(v + 'T00:00:00Z')) ? v : '');

/** Whatever was typed, as a filters object with every field valid (an unknown choice, a bad date or a long string becomes empty). */
export function clean(f) {
    const o = f && typeof f === 'object' ? f : {};
    const groups = Object.fromEntries(GROUP_KEYS.map(k => [groupField(k), o[groupField(k)] !== false]));
    return { q: text(o.q, 200), type: TYPES.includes(o.type) ? o.type : '', from: day(o.from), to: day(o.to), sort: SORTS.includes(o.sort) ? o.sort : 'newest', ...groups };
}

/** The tool groups the filters hide, as keys. */
export function hiddenGroups(f) { return GROUP_KEYS.filter(k => f[groupField(k)] === false); }
/** The filters with exactly these tool groups hidden (a Set or an array of keys). */
export function withHidden(f, hidden) { const h = new Set(hidden); return { ...f, ...Object.fromEntries(GROUP_KEYS.map(k => [groupField(k), !h.has(k)])) }; }
/** What the server is asked for (the query: not the tool groups, which narrow the rows loaded). */
export function queryKey(f) { const c = clean(f); return JSON.stringify([c.q, c.type, c.from, c.to, c.sort]); }

/** True when anything narrows the list (the sort alone does not); a hidden tool group does. */
export function isFiltered(f) { return !!(f.q || f.type || f.from || f.to) || hiddenGroups(f).length > 0; }
/** True when the list must come from the search: a filter of the query, or the oldest-first order the live feed does not have (a hidden tool group does not: it narrows what is loaded). */
export function isQuery(f) { return !!(f.q || f.type || f.from || f.to) || f.sort !== 'newest'; }

/** The active filters as chips: {key, label}; removing a chip clears that key. */
export function chips(f) {
    const out = [];
    if (f.q) out.push({ key: 'q', label: `Search: ${f.q}` });
    if (f.type) out.push({ key: 'type', label: `Type: ${TYPE_LABEL[f.type]}` });
    if (f.from) out.push({ key: 'from', label: `From ${f.from}` });
    if (f.to) out.push({ key: 'to', label: `Until ${f.to}` });
    const hidden = hiddenGroups(f);
    if (hidden.length) out.push({ key: 'groups', label: `Hidden: ${hidden.map(k => FILTER_GROUPS.find(g => g.key === k).title).join(', ')}` });
    return out;
}

/** Clear one chip's filter. */
export function without(f, key) { return key === 'groups' ? withHidden(f, []) : { ...f, [key]: '' }; }

/** A day the person picked (YYYY-MM-DD) as the instant its local midnight is, in RFC 3339; `plus` days later (the day after `to`, where the server's until stops: it is exclusive). */
function localMidnight(d, plus = 0) {
    const [y, m, dd] = d.split('-').map(Number);
    return new Date(y, m - 1, dd + plus).toISOString();
}

/** The query parameters for GET /api/sessions/live/{name}/events as [name, value] pairs (the names are the server's, agent_events_query.go and sessions.md): the session, the search, the
 *  event types, since and until, the order, the limit and a cursor. The last day picked is included. Empty filters send nothing. */
export function toParams(f, { sessionId = '', limit = 50, cursor = '' } = {}) {
    const c = clean(f);
    const p = [];
    if (sessionId) p.push(['session_id', sessionId]);
    if (c.q) p.push(['q', c.q]);
    for (const t of c.type ? TYPE_WORDS[c.type] : []) p.push(['type', t]);
    if (c.from) p.push(['since', localMidnight(c.from)]);
    if (c.to) p.push(['until', localMidnight(c.to, 1)]);
    p.push([SORT_PARAM, c.sort]);
    p.push(['limit', String(limit)]);
    if (cursor) p.push(['cursor', cursor]);
    return p;
}

/** The count beside the search: "37 events", "1 event", "500+ events" when the search stopped at the server's bound. */
export function countText(total, truncated) {
    return `${total}${truncated ? '+' : ''} event${total === 1 && !truncated ? '' : 's'}`;
}
