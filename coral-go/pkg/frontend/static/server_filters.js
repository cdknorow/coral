/* The Servers list's search (#4778): text over a machine's name, id and address. Pure (no DOM, no imports), client side: the list is the one the tab already holds (the hub's 20 machines).
 * server_search.js draws the box and applies it to the list. */

export const EMPTY = { q: '' };

const text = (v, max) => (typeof v === 'string' ? v.replace(/[\u0000-\u001f\u007f]/g, ' ').trim().slice(0, max) : '');

/** Whatever was typed, as a filters object with every field valid. */
export function clean(f) { return { q: text(f && f.q, 200) }; }
export function isFiltered(f) { return !!f.q; }
export function chips() { return []; } // the search is in the box: no chip for it
export function without(f, key) { return { ...f, [key]: '' }; }

/** Whether a server (a row of /api/servers: label, id, url, host) matches: every word of the search is in its name, id or address, in any case. */
export function matches(s, f) {
    const q = clean(f).q.toLowerCase();
    if (!q) return true;
    const hay = [s && s.label, s && s.id, s && s.url, s && s.host].filter(v => typeof v === 'string').join(' ').toLowerCase();
    return q.split(/\s+/).every(w => hay.includes(w));
}

/** The count's words: "3 of 20 machines" while searching, "20 machines" otherwise (the noun is the screen's: server on a self-hosted hub, machine on the hosted one). */
export function countText(shown, total, noun, filtered) {
    const word = (n) => (n === 1 ? noun : noun + 's');
    return filtered ? `${shown} of ${total} ${word(total)}` : `${total} ${word(total)}`;
}
