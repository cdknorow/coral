/* Where a comment sits in a document (#5182): the quote it was made on, found again whenever the document is drawn. No DOM here, only strings (artifact_comments_dom.js maps them
 * to the page), so every case is a plain test. An anchor is { quote, prefix, suffix, start_line, end_line } or { whole: true }: the quote is the selected text, prefix and suffix are
 * about 80 characters either side of it (they tell repeats apart), and the lines are where the quote is found in the file's source (best effort, null when the rendered text is not in it). Text is
 * compared with every run of white space as one space, so the same words match across a re-render. */

export const QUOTE_MAX = 2048;
export const CONTEXT = 80;

/** Every run of white space as one space, ends trimmed: what a quote is compared as. */
export const squash = (s) => String(s == null ? '' : s).replace(/\s+/g, ' ').trim();

/** The anchor for the text `quote` that sits between `before` and `after` in the document's text (all three as drawn); null when nothing but white space is selected. */
export function buildAnchor(before, quote, after, source) {
    const q = squash(quote).slice(0, QUOTE_MAX);
    if (!q) return null;
    // only the ends of what is before and after matter, so the rest of a megabyte is not squashed for each change of a selection (#5255); 40 characters of raw text for each that is kept, for runs of white space
    const pre = squash(String(before).slice(-CONTEXT * 40)).slice(-CONTEXT), suf = squash(String(after).slice(0, CONTEXT * 40)).slice(0, CONTEXT);
    const lines = lineRange(source, q);
    return { quote: q, prefix: pre, suffix: suf, start_line: lines ? lines[0] : null, end_line: lines ? lines[1] : null };
}

/** The 1-based first and last line of the first place `quote` is found in `source`, or null. The source is compared squashed like the quote (a quote crosses line breaks), and markdown source has its markers
 *  where the rendered text has none, so an exact find is best effort: a quote that is not found whole is looked for by its first and last words. */
export function lineRange(source, quote) {
    if (!source || !quote) return null;
    const { flat, from } = squashedSource(source);
    const lineAt = (flatIndex) => linesBefore(source, from[Math.min(flatIndex, from.length - 1)]) + 1;
    let start = flat.indexOf(quote);
    if (start >= 0) return [lineAt(start), lineAt(start + quote.length - 1)];
    const words = quote.split(' ');
    if (words.length < 2) return null;
    const first = words.slice(0, 3).join(' '), last = words.slice(-3).join(' ');
    start = flat.indexOf(first);
    const endAt = start >= 0 ? flat.indexOf(last, start) : -1;
    return start >= 0 && endAt >= 0 ? [lineAt(start), lineAt(endAt + last.length - 1)] : null;
}

// The line breaks in source before position `at`.
function linesBefore(source, at) {
    let n = 0;
    for (let i = source.indexOf('\n'); i >= 0 && i < at; i = source.indexOf('\n', i + 1)) n++;
    return n;
}

// The source squashed, with where each of its characters was; the last one is kept, because a selection builds an anchor on every change while it is dragged (a megabyte took seconds a time, #5255).
let lastSquashed = null;
function squashedSource(source) {
    if (lastSquashed && lastSquashed.source === source) return lastSquashed;
    const from = new Uint32Array(source.length);
    const pieces = [];
    let n = 0, run = -1, prevSpace = true; // prevSpace is true at the start: a leading space is dropped
    for (let i = 0; i < source.length; i++) {
        if (isSpace(source.charCodeAt(i))) {
            if (run >= 0) { pieces.push(source.slice(run, i)); run = -1; }
            if (!prevSpace) { pieces.push(' '); from[n++] = i; prevSpace = true; }
        } else {
            if (run < 0) run = i;
            from[n++] = i; prevSpace = false;
        }
    }
    if (run >= 0) pieces.push(source.slice(run));
    lastSquashed = { source, flat: pieces.join(''), from: from.subarray(0, n) };
    return lastSquashed;
}

/** Where in `text` (already squashed) the anchor's quote sits: { start, end } or null (the text changed). Several places: the one whose surroundings share the most with the stored prefix and suffix, the first on a tie. */
export function locateQuote(text, anchor) {
    if (!anchor || anchor.whole || !anchor.quote) return null;
    const q = squash(anchor.quote);
    if (!q) return null;
    const hits = [];
    for (let i = text.indexOf(q); i >= 0; i = text.indexOf(q, i + 1)) hits.push(i);
    if (!hits.length) return null;
    if (hits.length === 1) return { start: hits[0], end: hits[0] + q.length };
    const pre = squash(anchor.prefix), suf = squash(anchor.suffix);
    let best = hits[0], bestFit = -1;
    for (const i of hits) {
        const fit = sharedTail(text.slice(Math.max(0, i - pre.length - 1), i).trimEnd(), pre) + sharedHead(text.slice(i + q.length, i + q.length + suf.length + 1).trimStart(), suf);
        if (fit > bestFit) { best = i; bestFit = fit; }
    }
    return { start: best, end: best + q.length };
}

// How many characters two strings share at their ends (the end of the text before the quote with the end of the stored prefix), or at their starts.
function sharedTail(a, b) { let n = 0; while (n < a.length && n < b.length && a[a.length - 1 - n] === b[b.length - 1 - n]) n++; return n; }
function sharedHead(a, b) { let n = 0; while (n < a.length && n < b.length && a[n] === b[n]) n++; return n; }

// JavaScript's \s, by code (a regular expression per character is the slow part of indexing a megabyte).
const isSpace = (c) => c === 32 || (c >= 9 && c <= 13) || (c > 127 && (c === 0xa0 || c === 0x1680 || (c >= 0x2000 && c <= 0x200a) || c === 0x2028 || c === 0x2029 || c === 0x202f || c === 0x205f || c === 0x3000 || c === 0xfeff));

/** The text of the document as it is compared: squashed, with for each of its characters the node and offset it came from (so a found { start, end } can become a DOM range). `nodes` are the text nodes in order.
 *  Returns { text, length, at(i) }: at(i) is { node, offset } of character i. The map is two typed arrays, not an object a character (a megabyte of text was a million objects and several seconds, #5255). */
export function indexText(nodes) {
    let total = 0;
    for (const node of nodes) total += node.nodeValue.length;
    const which = new Uint32Array(total), where = new Uint32Array(total);
    const pieces = [];
    let n = 0, prevSpace = true; // true at the start: a leading space is dropped
    for (let k = 0; k < nodes.length; k++) {
        const s = nodes[k].nodeValue;
        let run = -1; // where the current run of characters that are kept as they are began
        for (let i = 0; i < s.length; i++) {
            if (isSpace(s.charCodeAt(i))) {
                if (run >= 0) { pieces.push(s.slice(run, i)); run = -1; }
                if (!prevSpace) { pieces.push(' '); which[n] = k; where[n] = i; n++; prevSpace = true; } // the first of a run is one space; the rest are dropped
            } else {
                if (run < 0) run = i;
                which[n] = k; where[n] = i; n++; prevSpace = false;
            }
        }
        if (run >= 0) pieces.push(s.slice(run));
    }
    if (n > 0 && prevSpace) { pieces.pop(); n--; } // no trailing space
    return { text: pieces.join(''), length: n, at: (i) => ({ node: nodes[which[i]], offset: where[i] }) };
}
