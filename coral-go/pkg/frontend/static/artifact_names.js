/* The names artifacts were stored under (#297, QA's #278 row 7), for the links that showed only "artifact 81b837f8". A name is
 * learnt from GET /api/artifacts/<hex>/meta, once per artifact per page, and only an answer of exactly 200 counts; until then,
 * and when there is no name, the link keeps its short id. A name is the uploader's text: it is only ever set as text (never
 * markup), without controls or the characters that reorder or hide text, and cut to a length a link can show. */

const names = new Map(); // hex -> the stored name ('' when it has none or can't be read)
const asked = new Set();
const MAX_NAME = 80;

// C0/C1 controls, DEL, the soft hyphen, the format characters that reorder or hide text (bidi embeddings and isolates,
// zero-width marks), and the invisible tag characters (U+E0000-E007F, Security on #128). Written as escapes: never the characters.
const UNSAFE = /[\u0000-\u001f\u007f-\u009f\u00ad\u061c\u200b-\u200f\u202a-\u202e\u2060-\u2069\ufeff\u{E0000}-\u{E007F}]/gu;
// A name that reads as an address (https://bank.example/login, www.example.com) shows as the short id instead, so a link's
// text never looks like another site: it only ever opens the artifact (Security on #128).
const LOOKS_LIKE_ADDRESS = /:\/\/|^www\./i;

/** name as a link may show it: no controls or reordering characters, spaces collapsed, at most MAX_NAME characters; '' (the
 *  short id is shown) for a name that reads as an address. */
export function cleanArtifactName(name) {
    const n = String(name == null ? '' : name).replace(UNSAFE, '').replace(/\s+/g, ' ').trim();
    if (LOOKS_LIKE_ADDRESS.test(n)) return '';
    const chars = [...n];
    return chars.length > MAX_NAME ? chars.slice(0, MAX_NAME - 1).join('') + '…' : n;
}

/** What a link to the artifact reads: its stored name once known, else "artifact" and its first 8 hex digits. */
export function artifactLabel(hex) {
    return names.get(hex) || `artifact ${String(hex).slice(0, 8)}`;
}

/** Remember a name already read (from a /meta answer another view fetched). */
export function rememberArtifactName(hex, name) {
    names.set(hex, cleanArtifactName(name));
}

/** Ask for the names of these artifacts (each at most once per page) and relabel their links on the page as each answers. */
export function learnArtifactNames(hexes) {
    for (const hex of hexes) {
        if (asked.has(hex)) continue;
        asked.add(hex);
        fetch(`/api/artifacts/${hex}/meta`)
            .then(r => (r.status === 200 ? r.json() : null))
            .then(d => {
                rememberArtifactName(hex, d && d.name);
                const label = names.get(hex);
                if (!label) return;
                for (const a of document.querySelectorAll(`a.mb-artifact-ref[data-artifact="${hex}"]`)) {
                    a.textContent = label;
                    if (a.hasAttribute('aria-label')) a.setAttribute('aria-label', `${label}: id ${hex}`); // a screen reader's name, where the link has one
                }
            })
            .catch(() => { names.set(hex, ''); });
    }
}
