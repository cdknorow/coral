/* The slim bar under the Terminal view (#4044): the connection state, the terminal's size in columns and rows, and the typing hint.
 * It finishes the view and keeps the last terminal line off the window's edge. Everything it shows is already known to the page. */

// The terminal socket's states, named as the terminal's (#4587, coral-hub's O2): the bar is in the Terminal view only, and the
// Chat view, which opens no terminal socket, never shows them.
const STATES = {
    connecting: 'Connecting to terminal…',
    connected: 'Terminal connected',
    reconnecting: 'Terminal disconnected',
    ended: 'Session ended',
    restarting: 'Restarting',
};

const bar = () => document.getElementById('terminal-statusbar');

import { startKeyboardWatch } from './keyboard_open.js';
import { importOnce } from './import_once.js';
import { registerActions } from './actions.js';
import { copyText, showToast } from './utils.js';

/** Show the bar with the terminal (the Terminal view) or hide it with it. */
let keyBarWired = false;

export function showTerminalStatusbar(on) {
    const b = bar();
    if (b) b.style.display = on ? '' : 'none';
    if (on) startKeyboardWatch();
    const copy = document.getElementById('terminal-copy-screen-btn');
    if (copy) copy.hidden = !on;
    // The phone's key bar (#6676) is part of the Terminal view: shown with it (its CSS shows it at phone width only, and not in the Chat view).
    const keys = document.getElementById('terminal-key-bar');
    if (keys) {
        keys.hidden = !on;
        if (!keyBarWired) { keyBarWired = true; keys.addEventListener('pointerdown', e => e.preventDefault()); } // a tap on a key leaves focus where it is: the on-screen keyboard stays open
    }
}

/** One of connecting, connected, reconnecting, ended, restarting. */
export function setTerminalState(name) {
    const b = bar();
    if (!b || !STATES[name]) return;
    b.dataset.state = name;
    const text = b.querySelector('.terminal-statusbar-state-text');
    if (text) text.textContent = STATES[name];
}

/** The state now shown, '' when there is no bar. */
export function terminalState() {
    return bar()?.dataset.state || '';
}

/** The terminal's size, as "cols × rows". */
export function setTerminalSize(cols, rows) {
    const el = document.getElementById('terminal-statusbar-size');
    if (!el) return;
    el.textContent = cols > 0 && rows > 0 ? `${cols} × ${rows}` : '';
    el.title = cols > 0 && rows > 0 ? `Terminal size: ${cols} columns, ${rows} rows` : '';
}

/** The terminal has the keyboard focus: the typing hint steps back. */
export function setTerminalFocused(on) {
    bar()?.classList.toggle('terminal-statusbar-focused', !!on);
    if (on) bar()?.classList.add('terminal-statusbar-typed'); // "Click to type" is for the first time only (#7064): after it the line is just the way out
}

registerActions({
    // Copy screen (#6694): the text on the screen, for a phone where a long press selects nothing.
    'terminal-copy-screen': async () => {
        const text = (await importOnce('./xterm_renderer.js')).terminalScreenText();
        if (!text) { showToast('The terminal screen is empty'); return; }
        { const ok = await copyText(text); showToast(ok ? 'Terminal text copied' : 'Could not copy the terminal text', !ok); };
    },
});
