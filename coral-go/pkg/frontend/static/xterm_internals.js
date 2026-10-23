/* Everything this page reads from xterm.js that xterm does not document (#5411). The vendored xterm is 5.5.0 (vendor/README.md); its public API (Terminal, the addons) is used directly all over
 * xterm_renderer.js, but five things have no public way, and each is reached only here, with the fallback that applies when the field is not there:
 *
 *   screenElement(term)      the .xterm-screen element (xterm's own DOM class: the drawn size of the terminal).          Fallback: null  (the cursor box and the drawn width are then 0 / none).
 *   cellHeight(term)         one row's height in CSS px, the renderer's own measurement (term._core._renderService      Fallback: 0     (_fit then derives it from the screen element, and only if it is
 *                            .dimensions.css.cell.height: what the fit addon's proposal uses too).                                     a plausible cell, else it keeps the addon's rows).
 *   clearRenderer(term)      repaint from scratch before a resize (term._core._renderService.clear(), as              Fallback: false (a resize still happens; the old pixels may show until the next write).
 *                            FitAddon.fit() does).
 *   onUserInput(term, fn)    fn() runs just before onData for a key, paste, IME or mouse report (term._core.coreService  Fallback: false (the caller forwards every onData, as before the filter existed:
 *                            .onUserInput): the difference between what a person typed and xterm's own answer to a      xterm's replies to a query in the output then reach the pane).
 *                            query in the output.
 *   watchCanvasScale(term)   keeps the canvas renderer's backing store at CSS size x DPR (term._core._renderService      Fallback: a no-op stop function (the renderer is then as xterm left it).
 *                            .dimensions and its renderer's handleResize): Chrome's device-metrics emulation tells
 *                            xterm's observer the canvas is its CSS size at DPR 2 or 3, and the text then draws that
 *                            many times too large (#6596).
 *
 * tests/frontend/xterm_internals_census.test.mjs fails if any other module touches term._core (or the services under it), and xterm_internals_browser.test.js drives a real terminal of the bundled xterm and
 * fails loudly, naming the field, if one of the four is gone: an xterm upgrade cannot quietly break fit, cursor follow or the query filter. Update this file (and the fallbacks) with the vendor README. */

const core = (term) => (term && term._core) || null;

/** The .xterm-screen element, or null. */
export function screenElement(term) {
    return (term && term.element && term.element.querySelector('.xterm-screen')) || null;
}

/** One row's height in CSS pixels as the renderer measured it, or 0 when it has not (yet) or xterm has no such field. */
export function cellHeight(term) {
    const rs = core(term) && core(term)._renderService;
    const h = rs && rs.dimensions && rs.dimensions.css && rs.dimensions.css.cell ? rs.dimensions.css.cell.height : 0;
    return h > 0 ? h : 0;
}

/** Repaint from scratch before a resize. Returns whether xterm had a renderer service to ask. */
export function clearRenderer(term) {
    const rs = core(term) && core(term)._renderService;
    if (!rs || typeof rs.clear !== 'function') return false;
    rs.clear();
    return true;
}

/** Run fn just before onData for what a person did (a key, a paste, IME, a mouse report). Returns whether xterm can say so; when it cannot, nothing is subscribed. */
export function onUserInput(term, fn) {
    const cs = core(term) && core(term).coreService;
    if (!cs || typeof cs.onUserInput !== 'function') return false;
    cs.onUserInput(fn);
    return true;
}

/** Redraws the canvas at its device size when xterm's own record of it is far from the columns x device cell it drew with (an observer that reported CSS pixels at DPR 2 or 3). Within a column's width
 *  is xterm's own fractional-DPR rounding and is left alone, so this cannot fight it. Returns whether it redrew. */
export function syncCanvasScale(term) {
    const rs = core(term) && core(term)._renderService;
    const r = rs && rs._renderer && rs._renderer.value;
    const d = rs && rs.dimensions && rs.dimensions.device;
    if (!r || !d || typeof r.handleResize !== 'function' || !term.cols || !term.rows) return false;
    const want = term.cols * d.cell.width;
    if (!(want > 0) || Math.abs(d.canvas.width - want) <= term.cols) return false;
    r.handleResize(term.cols, term.rows);
    return true;
}

/** Calls syncCanvasScale whenever the drawn screen changes size, after xterm's own observer (made earlier, so it is told first). Returns a function that stops it. */
export function watchCanvasScale(term) {
    const el = screenElement(term);
    if (!el || typeof ResizeObserver !== 'function') return () => {};
    const ro = new ResizeObserver(() => { syncCanvasScale(term); });
    ro.observe(el);
    return () => ro.disconnect();
}

/** Which of the four this xterm has, for the test and the log: { screen, cellHeight, clear, userInput } (cellHeight is true once the renderer has measured a cell). */
export function probe(term) {
    const c = core(term);
    return {
        screen: !!screenElement(term),
        cellHeight: cellHeight(term) > 0,
        clear: !!(c && c._renderService && typeof c._renderService.clear === 'function'),
        userInput: !!(c && c.coreService && typeof c.coreService.onUserInput === 'function'),
    };
}
