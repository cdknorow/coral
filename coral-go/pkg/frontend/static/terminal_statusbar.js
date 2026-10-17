/* The slim bar under the Terminal view (#4044): the connection state, the terminal's size in columns and rows, and the typing hint.
 * It finishes the view and keeps the last terminal line off the window's edge. Everything it shows is already known to the page. */

const STATES = {
    connecting: 'Connecting',
    connected: 'Connected',
    reconnecting: 'Reconnecting',
    ended: 'Session ended',
    restarting: 'Restarting',
};

const bar = () => document.getElementById('terminal-statusbar');

/** Show the bar with the terminal (the Terminal view) or hide it with it. */
export function showTerminalStatusbar(on) {
    const b = bar();
    if (b) b.style.display = on ? '' : 'none';
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
}
