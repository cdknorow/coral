/* The hook other modules call to redraw the sidebar's agent list (#3835). render.js owns the redraw (renderLiveSessions) and many
 * sections around it ask for one; if they imported it back from render.js they could not live in their own modules (an import
 * cycle). render.js registers its renderer here once; everyone else imports this small module, which imports nothing of render.js. */
import { state } from './state.js';

let _render = null;

/** render.js registers the function that draws the agent list from a sessions array. */
export function registerLiveListRenderer(fn) { _render = fn; }

/** Redraw the agent list from the current sessions (nothing happens before render.js has registered). */
export function redrawLiveList() {
    if (_render) _render(state.liveSessions || []);
}
