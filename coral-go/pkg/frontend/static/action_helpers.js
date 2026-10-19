/* Helpers the sidebar's data-on-* actions and menus share (moved out of render.js, #4996; no behaviour change). */
import { tokenOf, textOf } from './actions.js';

// ── The sidebar, its menus, the history list and the board chat panel (#3645) ──
// Markup names these actions in data-on-*; their values are data attributes, checked here (a bad
// one does nothing). Each calls the same global the inline handler did. A menu item stops the
// click and closes the menus first, as before. The destructive ones (agent-kill, agent-restart,
// agent-rename, group-kill, team-kill, team-reset) are on actions.js's DESTRUCTIVE_ACTIONS.
export const W = name => (...a) => window[name](...a);

export const agentArgs = el => {
    const name = textOf(el.dataset.name), type = tokenOf(el.dataset.agentType), sid = textOf(el.dataset.sessionId);
    return name && type ? [name, type, sid || ''] : null;
};

export const menu = f => (el, event) => { event.stopPropagation(); window.closeSidebarKebabs(); f(el, event); };

export const withAgent = (fn, modes) => menu(el => {
    const a = agentArgs(el);
    if (!a) return;
    if (modes) { if (modes.includes(el.dataset.mode)) W(fn)(...a, el.dataset.mode); } else W(fn)(...a);
});

export const withBoard = fn => menu(el => { const b = textOf(el.dataset.board); if (b) W(fn)(b); });

export const dirOf = el => textOf(el.dataset.dir, 4096);

export const UPDOWN = { up: true, down: true };
