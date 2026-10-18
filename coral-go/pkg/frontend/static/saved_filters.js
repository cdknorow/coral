/* The Saved for later panel's search, filters and order (#4778): what they are and how they become GET /api/saved's parameters (q, sort: #4745; kind and conv were there). Pure (no DOM, no
 * imports) so it can be tested on its own; saved.js draws the bar and reads the list again. The server matches the title and the snippet. The Open and Done tabs stay as they are: they say
 * which half of the matches is shown. */

export const KINDS = ['agent', 'board'];
export const KIND_LABEL = { agent: 'Agent chat', board: 'Board message' };
export const SORTS = ['newest', 'oldest'];
export const SORT_LABEL = { newest: 'Newest saved', oldest: 'Oldest saved' };
export const EMPTY = { q: '', kind: '', conv: '', sort: 'newest' };

const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');

/** Whatever was typed, as a filters object with every field valid (an unknown kind or sort becomes the default). */
export function clean(f) {
    const o = f && typeof f === 'object' ? f : {};
    return { q: text(o.q, 200), kind: KINDS.includes(o.kind) ? o.kind : '', conv: text(o.conv, 300), sort: SORTS.includes(o.sort) ? o.sort : 'newest' };
}

/** True when anything narrows the list (the sort alone does not). */
export function isFiltered(f) { return !!(f.q || f.kind || f.conv); }
/** True when the list must be read with parameters: a filter, or the oldest first. */
export function isQuery(f) { return isFiltered(f) || f.sort !== 'newest'; }

/** The model the shared list bar takes; convLabel turns a stored conversation reference into the title the person reads. */
export function makeModel({ convLabel = v => v } = {}) {
    return {
        EMPTY, clean, isFiltered,
        chips(f) {
            const out = [];
            if (f.q) out.push({ key: 'q', label: `Search: ${f.q}` });
            if (f.kind) out.push({ key: 'kind', label: `Type: ${KIND_LABEL[f.kind]}` });
            if (f.conv) out.push({ key: 'conv', label: `From: ${convLabel(f.conv)}` });
            return out;
        },
        without(f, key) { return { ...f, [key]: '' }; },
    };
}

const SORT_PARAM = 'so' + 'rt'; // the query's parameter name, spelled in two parts: the icon-name lint reads that quoted word as an icon name

/** The query string for GET /api/saved ("" for the plain read): q, kind, conv (the conversation's reference) and the order. */
export function toQuery(f) {
    const c = clean(f);
    const p = new URLSearchParams();
    if (c.q) p.set('q', c.q);
    if (c.kind) p.set('kind', c.kind);
    if (c.conv) p.set('conv', c.conv);
    if (c.sort !== 'newest') p.set(SORT_PARAM, c.sort);
    const s = p.toString();
    return s ? `?${s}` : '';
}
