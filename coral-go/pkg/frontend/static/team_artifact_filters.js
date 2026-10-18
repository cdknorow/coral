/* The Team Artifacts panel's search, filters and sort (#4573): what they are, how they are kept per board and how they become the list route's query. Pure (no DOM, no imports)
 * so it can be tested on its own; team_artifact_bar.js draws the controls and team_artifacts.js asks the server. The server does the matching: a search covers every artifact,
 * not just the loaded page. */

export const KINDS = ['image', 'pdf', 'text', 'other'];
export const ORIGINS = ['shared', 'received', 'unlisted'];
export const SORTS = ['newest', 'oldest', 'name', 'size'];
export const KIND_LABEL = { image: 'Image', pdf: 'PDF', text: 'Text', other: 'Other' };
export const ORIGIN_LABEL = { shared: 'Shared here', received: 'Received', unlisted: 'Unlisted' };
export const SORT_LABEL = { newest: 'Newest', oldest: 'Oldest', name: 'Name', size: 'Size' };
export const EMPTY = { q: '', kind: '', origin: '', by: '', kept: false, from: '', to: '', sort: 'newest' };

const DAY = /^\d{4}-\d{2}-\d{2}$/;
const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');
const day = v => (typeof v === 'string' && DAY.test(v) && !Number.isNaN(Date.parse(v + 'T00:00:00Z')) ? v : '');

/** Whatever was stored or typed, as a filters object with every field valid (an unknown kind, a bad date or a long string becomes empty). */
export function clean(f) {
    const o = f && typeof f === 'object' ? f : {};
    const from = day(o.from), to = day(o.to);
    return {
        q: text(o.q, 200),
        kind: KINDS.includes(o.kind) ? o.kind : '',
        origin: ORIGINS.includes(o.origin) ? o.origin : '',
        by: text(o.by, 80),
        kept: o.kept === true,
        from: from && to && from > to ? to : from,
        to: from && to && from > to ? from : to,
        sort: SORTS.includes(o.sort) ? o.sort : 'newest',
    };
}

/** True when anything narrows the list (the sort alone does not). */
export function isFiltered(f) { return !!(f.q || f.kind || f.origin || f.by || f.kept || f.from || f.to); }

export const storageKey = team => `coral-team-artifacts-filters:${team}`;

/** The filters kept for a board, or the empty ones. store is browserLocal (every read and write is guarded there). */
export function loadFilters(team, store) {
    if (!team || !store) return { ...EMPTY };
    let raw = null;
    try { raw = JSON.parse(store.get(storageKey(team)) || 'null'); } catch { raw = null; }
    return clean(raw);
}
export function saveFilters(team, f, store) {
    if (!team || !store) return;
    try {
        const c = clean(f);
        if (isFiltered(c) || c.sort !== 'newest') store.set(storageKey(team), JSON.stringify(c)); else store.remove(storageKey(team));
    } catch { /* storage unavailable: the choice lasts until the page is closed */ }
}

/** The active filters as chips: {key, label}; removing a chip clears that key (the two dates are one chip). */
export function chips(f) {
    const out = [];
    if (f.q) out.push({ key: 'q', label: `Search: ${f.q}` });
    if (f.kind) out.push({ key: 'kind', label: `Type: ${KIND_LABEL[f.kind]}` });
    if (f.origin) out.push({ key: 'origin', label: `Origin: ${ORIGIN_LABEL[f.origin]}` });
    if (f.by) out.push({ key: 'by', label: `By: ${f.by}` });
    if (f.kept) out.push({ key: 'kept', label: 'Kept' });
    if (f.from || f.to) out.push({ key: 'dates', label: f.from && f.to ? `${f.from} to ${f.to}` : f.from ? `From ${f.from}` : `Until ${f.to}` });
    return out;
}

/** Clear one chip's filter. */
export function without(f, key) {
    const o = { ...f };
    if (key === 'dates') { o.from = ''; o.to = ''; } else if (key === 'kept') o.kept = false; else o[key] = '';
    return o;
}

/** A day the person picked (YYYY-MM-DD) as the instant its local midnight is, in RFC 3339 (the server's since and until). addDays moves it on (until is exclusive: the day after). */
function localMidnight(day, addDays = 0) {
    const [y, m, d] = day.split('-').map(Number);
    return new Date(y, m - 1, d + addDays).toISOString();
}

/** The query parameters for the list route (the names are the server's: team_artifacts.go ListTeamArtifacts: q, kind, origin, by, kept, since, until, sort). Empty filters send nothing;
 *  sort is always sent, which makes it a query: the reply then carries the filtered total and a cursor for the next page. */
export function toParams(f) {
    const c = clean(f);
    const p = {};
    if (c.q) p.q = c.q;
    if (c.kind) p.kind = c.kind;
    if (c.origin) p.origin = c.origin;
    if (c.by) p.by = c.by;
    if (c.kept) p.kept = 'true';
    if (c.from) p.since = localMidnight(c.from);
    if (c.to) p.until = localMidnight(c.to, 1);
    p.sort = c.sort;
    return p;
}
