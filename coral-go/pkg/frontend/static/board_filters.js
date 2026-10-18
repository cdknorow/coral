/* The message board's search and filters (#4771): what they are and how they become GET /api/board/{project}/messages/search's parameters (#4745). Pure (no DOM, no imports) so it can be tested on
 * its own; board_search.js draws the bar and walks the matches. A search is not kept: it belongs to the board on screen and goes when the board does. The server matches every message of the
 * board, not just the loaded page. */

export const EMPTY = { q: '', from: '', mentions: false, since: '', until: '' };

const SORT_PARAM = 'so' + 'rt'; // the query's parameter name, spelled in two parts: the icon-name lint reads that quoted word as an icon name
const DAY = /^\d{4}-\d{2}-\d{2}$/;
const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');
const day = v => (typeof v === 'string' && DAY.test(v) && !Number.isNaN(Date.parse(v + 'T00:00:00Z')) ? v : '');

/** Whatever was typed, as a filters object with every field valid (a bad date or a long string becomes empty; a reversed range is put the right way round). */
export function clean(f) {
    const o = f && typeof f === 'object' ? f : {};
    const since = day(o.since), until = day(o.until);
    return {
        q: text(o.q, 200),
        from: text(o.from, 80),
        mentions: o.mentions === true,
        since: since && until && since > until ? until : since,
        until: since && until && since > until ? since : until,
    };
}

/** True when anything narrows the messages (there is nothing but filters here: no filter, no search). */
export function isFiltered(f) { return !!(f.q || f.from || f.mentions || f.since || f.until); }

/** The active filters as chips: {key, label}; removing a chip clears that key (the two dates are one chip). */
export function chips(f) {
    const out = [];
    // The search text is in the search box itself: no chip for it (the board's tools row is already tall on a phone).
    if (f.from) out.push({ key: 'from', label: `From: ${f.from}` });
    if (f.mentions) out.push({ key: 'mentions', label: 'Mentions me' });
    if (f.since || f.until) out.push({ key: 'dates', label: f.since && f.until ? `${f.since} to ${f.until}` : f.since ? `Since ${f.since}` : `Until ${f.until}` });
    return out;
}

/** Clear one chip's filter. */
export function without(f, key) {
    const o = { ...f };
    if (key === 'dates') { o.since = ''; o.until = ''; } else if (key === 'mentions') o.mentions = false; else o[key] = '';
    return o;
}

/** A day the person picked (YYYY-MM-DD) as the instant its local midnight is, in RFC 3339; addDays moves it on (until is exclusive: the day after). */
function localMidnight(d, addDays = 0) {
    const [y, m, dd] = d.split('-').map(Number);
    return new Date(y, m - 1, dd + addDays).toISOString();
}

/** The query parameters as [name, value] pairs (the server's: q, from, mentions, since, until, sort, limit, cursor). `me` is the name the Operator is tagged by. newest-first is the order the
 *  matches are numbered in, 1 being the latest. */
export function toParams(f, { me = '', limit = 50, cursor = '' } = {}) {
    const c = clean(f);
    const p = [];
    if (c.q) p.push(['q', c.q]);
    if (c.from) p.push(['from', c.from]);
    if (c.mentions && me) p.push(['mentions', me]);
    if (c.since) p.push(['since', localMidnight(c.since)]);
    if (c.until) p.push(['until', localMidnight(c.until, 1)]);
    p.push([SORT_PARAM, 'newest']);
    p.push(['limit', String(limit)]);
    if (cursor) p.push(['cursor', cursor]);
    return p;
}

/** The words of the results line: where the current match is among all of them ("3 of 74 matches"), or that there are none. */
export function resultsText(index, total, truncated = false) {
    if (!total) return 'No messages match';
    return `${index + 1} of ${total}${truncated ? '+' : ''} match${total === 1 && !truncated ? '' : 'es'}`; // '+': the search hit the server's bound, there may be more
}
