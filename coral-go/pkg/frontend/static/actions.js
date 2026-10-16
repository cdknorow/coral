/* Delegated event handlers (#3645, CSP stage 2): the dashboard's replacement for inline on*=
 * attributes, so script-src can drop 'unsafe-inline' (a page that runs no attribute text has
 * nothing for an injected attribute to run).
 *
 * Markup names an action per event, and passes its arguments as data attributes, which are
 * HTML-escaped values, never code:
 *
 *   <th data-on-click="cost-sort" data-table="agent" data-field="cost_usd">
 *
 * A module registers what each name does:
 *
 *   registerActions({ 'cost-sort': (el, event) => sortBy(el.dataset.table, el.dataset.field) });
 *
 * One listener per event type on document runs the actions from the event's target up through its
 * ancestors, innermost first, as inline handlers ran. event.stopPropagation() in an action stops
 * the walk and, as an inline handler's did, keeps the event from every later document listener
 * (a dropdown's close-on-outside-click, say): the dispatcher stops it immediately. An action that
 * replaces a link's navigation calls event.preventDefault() itself. An unknown name is ignored
 * (and logged once). Works the same on this computer and through Coral Hub. */

export const ACTION_EVENTS = ['click', 'change', 'input', 'keydown', 'submit', 'contextmenu', 'mousedown'];

const actions = new Map();
const warned = new Set();

/** registerActions adds named actions: { name: (element, event) => void }. A name is registered
 *  once; a second registration of the same name throws (two modules would fight over it). */
export function registerActions(map) {
    for (const [name, fn] of Object.entries(map)) {
        if (typeof fn !== 'function') throw new TypeError(`action ${name} is not a function`);
        if (actions.has(name)) throw new Error(`action ${name} is registered twice`);
        actions.set(name, fn);
    }
}

/** hasAction reports whether name is registered (tests and the census use it). */
export function hasAction(name) { return actions.has(name); }

function attrFor(type) { return 'data-on-' + type; }

function run(event) {
    const attr = attrFor(event.type);
    for (let el = event.target instanceof Element ? event.target : event.target?.parentElement; el && el !== document.documentElement; el = el.parentElement) {
        const name = el.getAttribute(attr);
        if (name === null) continue;
        const fn = actions.get(name);
        if (!fn) {
            if (!warned.has(name)) { warned.add(name); console.warn('[actions] no action registered:', name); }
        } else {
            try { fn(el, event); } catch (err) { console.error('[actions]', name, err); }
        }
        if (event.cancelBubble) {
            event.stopImmediatePropagation();
            break;
        }
    }
}

let installed = false;
/** installActions starts the delegated listeners (once). */
export function installActions(root = document) {
    if (installed) return;
    installed = true;
    for (const type of ACTION_EVENTS) root.addEventListener(type, run);
}

installActions();
