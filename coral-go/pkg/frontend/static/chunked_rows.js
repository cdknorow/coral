/* One way to draw a long list of rows (#5329, #5336): the first rows at once, the rest a few frames later, so a 300-row list is not one stall on a slow computer (300 artifact rows were 160 ms at 4x slowdown).
 *
 *   appendRows(container, items, build, { whole })      build(item) returns a node; for lists that build DOM rows (the artifact tables)
 *   setIcon(node, svgMarkup)                            an icon parsed once and cloned after that, not parsed again for every row
 *
 * `whole` draws every row at once. A caller passes it when chunking would hurt: a list the person has scrolled (the box would be shorter than its scroll position in the middle of the draw, and the
 * browser would clamp it to the top), one the person is acting in (the focus goes back to a row that is not there yet), or any redraw of a list already shown. Chunks stop when the container is gone
 * (the panel was drawn again), and a page nobody is looking at gets no frames, so it goes on a timer there.
 *
 * Measured and left alone (#5336, at 300 rows, 4x slowdown): Browse's directory (112 ms to its rows) and the changed files (148 ms): chunking the changed files made it slower (183 ms to the first rows, 323
 * to all of them: the cost is the browser's style and layout of the rows, which splitting adds to), and Browse already draws only what a directory returns. Not used by the Tasks list or the board's messages: both are paged by the server (50 a page, "Load earlier") and drawn as one string; the board opened at 2,000 messages in 65 ms at 4x (#5329) with the
 * page's script at 67 ms, so there is no long draw to split, and chunking a string there would mean building rows as DOM, a rewrite for no measured gain. */

export const FIRST_ROWS = 40, ROWS_PER_FRAME = 60;

const later = fn => (document.hidden ? setTimeout(fn, 16) : requestAnimationFrame(fn));

const generations = new WeakMap(); // container -> the latest drawing of it: a drawing that is no longer the latest stops (a container drawn again in place, like a list's element, would get both)

function pump(container, count, drawRange, { first, perFrame, whole }) {
    const gen = (generations.get(container) || 0) + 1;
    generations.set(container, gen);
    const now = !whole && count > first ? first : count;
    drawRange(0, now);
    if (now >= count) return;
    let at = now;
    const more = () => {
        if (!container.isConnected || generations.get(container) !== gen) return; // the panel was drawn again, or this container was: this list is gone
        const stop = Math.min(count, at + perFrame);
        drawRange(at, stop);
        at = stop;
        if (at < count) later(more);
    };
    later(more);
}

/** Append `items` to `container` as nodes from build(item). */
export function appendRows(container, items, build, { whole = false, first = FIRST_ROWS, perFrame = ROWS_PER_FRAME } = {}) {
    pump(container, items.length, (a, b) => { for (let i = a; i < b; i++) container.append(build(items[i])); }, { first, perFrame, whole });
}

const iconTemplates = new Map();
/** Put the SVG `html` in `node`: parsed once, cloned after. */
export function setIcon(node, html) {
    let tpl = iconTemplates.get(html);
    if (!tpl) { tpl = document.createElement('template'); tpl.innerHTML = html; iconTemplates.set(html, tpl); }
    node.replaceChildren(tpl.content.cloneNode(true));
}
