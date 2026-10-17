/* Parts of the page that most visits never open are in it as <template data-lazy="<id>"> and built on first open (#4500): the
 * Settings, Launch, Job, Create task, Info, Default prompts and Custom view dialogs and the top-bar settings menu. Every load
 * built about 830 elements for them (and about three times that in DOM nodes), on a phone as on a desktop.
 *
 * ensureBuilt(id) puts the part in the page where its template was (so the page order, and with it which dialog is on top,
 * stays as it was), runs what onBuilt registered for it and what onAnyBuilt registered (dialog_focus.js wires a dialog's focus in
 * and back, as attachAllDialogFocus does at load), then returns the element. Until then getElementById(id) is null: code that
 * reads such a part at load guards for that, and its open path calls ensureBuilt first.
 *
 * No imports: many modules register hooks as they load, and a leaf module is evaluated before all of them. */
const hooks = new Map(); // id -> [fn(el)], run once that part is built
const anyHooks = []; // fn(el), run for every part built

/** The part with this id, built from its template first if it isn't in the page yet; null if there is neither. */
export function ensureBuilt(id) {
    const el = document.getElementById(id);
    if (el) return el;
    const tpl = document.querySelector(`template[data-lazy="${CSS.escape(id)}"]`);
    if (!tpl) return null;
    tpl.replaceWith(document.importNode(tpl.content, true));
    const built = document.getElementById(id);
    if (!built) return null;
    for (const fn of [...anyHooks, ...(hooks.get(id) || [])]) {
        try { fn(built); } catch (e) { console.error(`building #${id}:`, e); }
    }
    return built;
}

/** Runs fn(el) when the part is built, or now if it already is: for what code did to it at load before it was lazy. */
export function onBuilt(id, fn) {
    const el = typeof document !== 'undefined' ? document.getElementById(id) : null; // modules load in node tests too
    if (el) { fn(el); return; }
    if (!hooks.has(id)) hooks.set(id, []);
    hooks.get(id).push(fn);
}

/** Runs fn(el) for every part built from now on. */
export function onAnyBuilt(fn) {
    anyHooks.push(fn);
}

/** Builds every lazy part (tests that look at the whole page, e.g. every control has a name). */
export function buildAllLazy() {
    for (const tpl of [...document.querySelectorAll('template[data-lazy]')]) ensureBuilt(tpl.dataset.lazy);
}
if (typeof window !== 'undefined') (window.__coral ||= {}).buildAllLazy = buildAllLazy; // node tests import this without a page
