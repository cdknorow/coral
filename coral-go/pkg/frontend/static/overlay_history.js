/* The back gesture and the overlays (#5219). A phone's overlays (the preview's full screen, the artifact viewer, the comments sheet) each take a history entry when they open, so Back closes
 * the one on top and leaves the ones under it, in the order they opened. One listener reads how many entries are ours (history.state.coralOverlay) and closes the overlays above that. An overlay
 * that is closed another way (its Back button, Escape) gives its entry back with popOverlay: the top one goes back through history, one under another overlay is only forgotten (its entry stays, and a
 * Back over it closes nothing), so the depth stays right. The router's own entries (nav.js) carry no coralOverlay: a Back onto one closes every overlay. */
const stack = []; // { id, close } for each overlay still open; close is null for one given back out of order
let seq = 0;
let ignore = 0;   // popstates we caused ourselves (popOverlay going back)

/** An overlay opened: its entry is pushed. close({popped: true}) is called when Back takes it away. Returns the id for popOverlay. */
export function pushOverlay(close) {
    const id = ++seq;
    stack.push({ id, close });
    history.pushState({ coralOverlay: stack.length }, '');
    return id;
}

/** The overlay `id` closed by itself (a button, Escape): its entry goes. */
export function popOverlay(id) {
    const i = stack.findIndex(o => o.id === id);
    if (i < 0) return;
    if (i === stack.length - 1) {
        stack.pop();
        if (history.state?.coralOverlay === stack.length + 1) { ignore++; history.back(); }
    } else {
        stack[i].close = null; // under another overlay: forgotten, its entry stays
    }
}

window.addEventListener('popstate', () => {
    if (ignore > 0) { ignore--; return; }
    const depth = history.state?.coralOverlay || 0;
    while (stack.length > depth) {
        const o = stack.pop();
        if (o.close) o.close({ popped: true });
    }
});
