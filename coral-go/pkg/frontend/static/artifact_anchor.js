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
    const pre = squash(before).slice(-CONTEXT), suf = squash(after).slice(0, CONTEXT);
    const lines = lineRange(source, q);
    return { quote: q, prefix: pre, suffix: suf, start_line: lines ? lines[0] : null, end_line: lines ? lines[1] : null };
}

/** The 1-based first and last line of the first place `quote` is found in `source`, or null. The source is compared squashed like the quote (a quote crosses line breaks), and markdown source has its markers
 *  where the rendered text has none, so an exact find is best effort: a quote that is not found whole is looked for by its first and last words. */
export function lineRange(source, quote) {
    if (!source || !quote) return null;
    // the source squashed, with where each of its characters was
    let flat = '';
    const from = [];
    for (let i = 0; i < source.length; i++) {
        const ws = /\s/.test(source[i]);
        if (ws && (flat === '' || flat.endsWith(' '))) continue;
        flat += ws ? ' ' : source[i];
        from.push(i);
    }
    const lineAt = (flatIndex) => source.slice(0, from[Math.min(flatIndex, from.length - 1)]).split('\n').length;
    let start = flat.indexOf(quote);
    if (start >= 0) return [lineAt(start), lineAt(start + quote.length - 1)];
    const words = quote.split(' ');
    if (words.length < 2) return null;
    const first = words.slice(0, 3).join(' '), last = words.slice(-3).join(' ');
    start = flat.indexOf(first);
    const endAt = start >= 0 ? flat.indexOf(last, start) : -1;
    return start >= 0 && endAt >= 0 ? [lineAt(start), lineAt(endAt + last.length - 1)] : null;
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

/** The text of the document as it is compared: squashed, with for each of its characters the node and offset it came from (so a found { start, end } can become a DOM range). `nodes` are the text nodes in order. */
export function indexText(nodes) {
    let text = '';
    const at = [];
    for (const node of nodes) {
        const s = node.nodeValue;
        for (let i = 0; i < s.length; i++) {
            const ws = /\s/.test(s[i]);
            if (ws && (text === '' || text.endsWith(' '))) continue; // a leading space, or the second of a run
            text += ws ? ' ' : s[i];
            at.push({ node, offset: i });
        }
    }
    if (text.endsWith(' ')) { text = text.slice(0, -1); at.pop(); }
    return { text, at };
}
